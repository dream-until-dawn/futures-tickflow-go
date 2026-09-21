package shinnysource

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/embedded"
)

// —— 离线的天勤：token 端点 ＋ 名称服务 ＋ 一个照 probe.md 读数写的 DIFF 服务 ——
//
// 它模仿的形状（每一条都有读数，不是凭印象）：
//
//	第一条消息 rsp_login（6.20）· 只在收到 peek_message 之后推 rtn_data
//	focus_datetime＋focus_position=0 ⇒ 窗从第一根 datetime ≥ focus 的起；没有这样的根 ⇒ left_id = last_id+1（6.20 E1/E5）
//	窗 [left, left+view_width-1]；data 里总捎上 last_id 那一根（6.13 a）
//	同一连接上发过的根不再发（6.18）
//	合约不存在 ⇒ chart ready=false、id 全 -1，之后不再推（6.20 E3）
//
// ⚠️ 它是**读数的复刻**，不是天勤本身：复刻没覆盖到的形状，离线测试照样绿 —— 那一格靠 live_test.go。

type fakeBar struct {
	dt                     int64 // 纳秒
	open, high, low, close float64
	volume, closeOI        float64
}

type fakeServer struct {
	t   *testing.T
	srv *httptest.Server

	mu         sync.Mutex
	series     map[string][]fakeBar // ins → 按 id 排好
	authHits   int
	nsHits     int
	handshakes int
	setCharts  []map[string]any
	uas        []string
	tokens     int
	// 行为开关
	authStatus     int  // 非 0 ⇒ token 端点回这个状态
	rejectFirstTok bool // 第一次发出去的 token 在握手时回 401
	stallAfterPage int  // >0 ⇒ 第 N 个 set_chart 之后不再回任何消息
	dropID         int64
	dropIDSet      bool // 发窗时故意漏掉这个 id
	// 推送模式（v0.10 P-c）：set_chart 既没有 focus_datetime 也没有 left_kline_id ⇒ 这条连接是推送通道，
	// 之后照 push 里的指令逐条发（所有推送连接共用一条指令队列，一条连接用到 drop 为止）
	push      chan pushCmd
	pushConns int
	mdDown    bool // 行情握手一律回 503（重连失败那一格）
}

// pushCmd 是推送连接上的一条指令：发一帧 rtn_data（data 是它的一个元素），或断开这条连接。
type pushCmd struct {
	data map[string]any
	drop bool
}

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	fs := &fakeServer{t: t, series: map[string][]fakeBar{}, push: make(chan pushCmd, 1024)}
	mux := http.NewServeMux()
	mux.HandleFunc("/auth", fs.handleAuth)
	mux.HandleFunc("/ns", fs.handleNS)
	mux.HandleFunc("/md", fs.handleMD)
	fs.srv = httptest.NewServer(mux)
	t.Cleanup(fs.srv.Close)
	return fs
}

func (fs *fakeServer) handleAuth(w http.ResponseWriter, r *http.Request) {
	fs.mu.Lock()
	fs.authHits++
	st := fs.authStatus
	fs.tokens++
	tok := "tok-" + strconv.Itoa(fs.tokens)
	fs.uas = append(fs.uas, r.Header.Get("User-Agent"))
	fs.mu.Unlock()
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", 400)
		return
	}
	for _, k := range []string{"grant_type", "client_id", "client_secret", "username", "password"} {
		if r.PostForm.Get(k) == "" {
			http.Error(w, "missing "+k, 400)
			return
		}
	}
	if st != 0 {
		w.WriteHeader(st)
		w.Write([]byte(`{"error":"invalid_client","echo":"` + r.PostForm.Get("client_secret") + `"}`))
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"access_token": tok})
}

func (fs *fakeServer) handleNS(w http.ResponseWriter, r *http.Request) {
	fs.mu.Lock()
	fs.nsHits++
	fs.mu.Unlock()
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer tok-") {
		http.Error(w, "no token", 401)
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"mdurl": "ws" + strings.TrimPrefix(fs.srv.URL, "http") + "/md"})
}

func (fs *fakeServer) handleMD(w http.ResponseWriter, r *http.Request) {
	fs.mu.Lock()
	fs.handshakes++
	fs.uas = append(fs.uas, r.Header.Get("User-Agent"))
	reject := fs.rejectFirstTok && r.Header.Get("Authorization") == "Bearer tok-1"
	down := fs.mdDown
	fs.mu.Unlock()
	if down {
		http.Error(w, "md down", http.StatusServiceUnavailable)
		return
	}
	if reject {
		http.Error(w, "token expired", http.StatusUnauthorized)
		return
	}
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionNoContextTakeover})
	if err != nil {
		return
	}
	defer c.CloseNow()
	c.SetReadLimit(1 << 20)
	ctx := r.Context()
	write := func(v any) bool {
		b, _ := json.Marshal(v)
		return c.Write(ctx, websocket.MessageText, b) == nil
	}
	if !write(map[string]any{"aid": "rsp_login", "session_id": "MDSESSION"}) {
		return
	}
	sent := map[string]map[int64]bool{} // ins → 本连接已发过的 id（6.18）
	var pending []map[string]any        // 待推的 rtn_data.data
	peeking := false
	charts := 0
	stalled := false
	flush := func() bool {
		if stalled || !peeking || len(pending) == 0 {
			return true
		}
		peeking = false
		msg := map[string]any{"aid": "rtn_data", "data": pending}
		pending = nil
		return write(msg)
	}
	for {
		_, b, err := c.Read(ctx)
		if err != nil {
			return
		}
		var m map[string]any
		if json.Unmarshal(b, &m) != nil {
			continue
		}
		switch m["aid"] {
		case "peek_message":
			peeking = true
		case "set_chart":
			_, f := m["focus_datetime"]
			_, l := m["left_kline_id"]
			if !f && !l {
				fs.servePush(ctx, write)
				return
			}
			fs.mu.Lock()
			fs.setCharts = append(fs.setCharts, m)
			stallAfter := fs.stallAfterPage
			fs.mu.Unlock()
			charts++
			pending = append(pending, fs.window(m, sent))
			if stallAfter > 0 && charts > stallAfter {
				stalled = true
			}
		}
		if !flush() {
			return
		}
	}
}

// servePush 照指令队列发推送帧，遇 drop 断开（返回即 CloseNow）。
// ⚠️ 不按 peek_message 节流（真的服务端只在 peek 之后推）：被测的是 Live 怎么吃帧、断了怎么接，不是节流。
func (fs *fakeServer) servePush(ctx context.Context, write func(any) bool) {
	fs.mu.Lock()
	fs.pushConns++
	fs.mu.Unlock()
	for {
		select {
		case <-ctx.Done():
			return
		case cmd := <-fs.push:
			if cmd.drop {
				time.Sleep(20 * time.Millisecond) // 让已写出的帧先到
				return
			}
			if !write(map[string]any{"aid": "rtn_data", "data": []any{cmd.data}}) {
				return
			}
		}
	}
}

// window 算一张 chart 的答复（照 6.20 的形状）。
func (fs *fakeServer) window(m map[string]any, sent map[string]map[int64]bool) map[string]any {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	ins, _ := m["ins_list"].(string)
	cid, _ := m["chart_id"].(string)
	width := int64(m["view_width"].(float64))
	bars, exists := fs.series[ins]
	if !exists {
		return map[string]any{
			"charts": map[string]any{cid: map[string]any{"left_id": -1, "right_id": -1, "ready": false, "more_data": false}},
			"klines": map[string]any{ins: map[string]any{minuteKey: map[string]any{"last_id": -1, "data": map[string]any{}}}},
		}
	}
	last := int64(len(bars) - 1)
	var left int64
	if f, ok := m["focus_datetime"].(float64); ok {
		focus := int64(f)
		left = last + 1
		for i, b := range bars {
			if b.dt >= focus {
				left = int64(i)
				break
			}
		}
	} else {
		left = int64(m["left_kline_id"].(float64))
	}
	right := left + width - 1
	if sent[ins] == nil {
		sent[ins] = map[int64]bool{}
	}
	data := map[string]any{}
	add := func(id int64) {
		if id < 0 || id > last || sent[ins][id] || (fs.dropIDSet && id == fs.dropID) {
			return
		}
		sent[ins][id] = true
		b := bars[id]
		data[strconv.FormatInt(id, 10)] = map[string]any{
			"datetime": b.dt, "open": b.open, "high": b.high, "low": b.low, "close": b.close,
			"volume": b.volume, "open_oi": b.closeOI, "close_oi": b.closeOI,
		}
	}
	for id := left; id <= right && id <= last; id++ {
		add(id)
	}
	add(last)
	return map[string]any{
		"charts": map[string]any{cid: map[string]any{"left_id": left, "right_id": right, "ready": true, "more_data": false}},
		"klines": map[string]any{ins: map[string]any{minuteKey: map[string]any{"last_id": last, "data": data}}},
	}
}

// countingRT 数「经过它的请求有几次」。
type countingRT struct {
	next http.RoundTripper
	mu   sync.Mutex
	n    int
}

func (r *countingRT) RoundTrip(q *http.Request) (*http.Response, error) {
	r.mu.Lock()
	r.n++
	r.mu.Unlock()
	return r.next.RoundTrip(q)
}

func (r *countingRT) load() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.n
}

// —— 测试用的日历与数据 ——

// testDays 手列：2026-09-03（周四）… 09-08（周二），中间隔一个周末，没有节假日。
// ⚠️ 手列，不从数据里抽 —— 拿数据造日历再用它验那份数据是循环论证。
// 首日 20260903 在 embedded 里没有夜盘段（左端无上一交易日可挂，embedded.go DayOf 注释）。
var testDays = []tickflow.TradingDay{20260903, 20260904, 20260907, 20260908}

var rbKey = tickflow.ProductKey{Exchange: tickflow.SHFE, Product: "rb"}

func testCalendar(t *testing.T) tickflow.Calendar {
	t.Helper()
	cal, err := embedded.New(testDays)
	if err != nil {
		t.Fatal(err)
	}
	return cal
}

// minuteBars 给 [from, to] 里每个交易日的每个交易分钟造一根（取时段用日历 —— 这里造的是【服务端的数据】，
// 被测的是翻页与组装，不是日历；归日的对错另由手写的格子钉）。
func minuteBars(t *testing.T, cal tickflow.Calendar, k tickflow.ProductKey, from, to tickflow.TradingDay) []fakeBar {
	t.Helper()
	var out []fakeBar
	i := 0
	if err := cal.Walk(k, from, to, func(d tickflow.Day) bool {
		for _, s := range d.Sessions {
			for ts := s.Start; ts < s.End; ts += 60000 {
				p := 3000 + float64(i%50)
				out = append(out, fakeBar{dt: ts * 1e6, open: p, high: p + 2, low: p - 2, close: p + 1, volume: float64(i % 7), closeOI: 100000 + float64(i)})
				i++
			}
		}
		return true
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func cst(y int, m time.Month, d, hh, mm int) int64 {
	return time.Date(y, m, d, hh, mm, 0, 0, tickflow.CST).UnixMilli()
}

func (fs *fakeServer) config(cal tickflow.Calendar, now int64) Config {
	return Config{
		User: "u", Password: "p", ClientID: "cid", ClientSecret: "s3cr3t-value",
		Calendar:    cal,
		HTTPClient:  &http.Client{Timeout: 5 * time.Second},
		ReadTimeout: 2 * time.Second,
		Now:         func() int64 { return now },
		AuthURL:     fs.srv.URL + "/auth",
		NSURL:       fs.srv.URL + "/ns",
	}
}

func rbReq(ym int, from, to tickflow.TradingDay) tickflow.BarRequest {
	return tickflow.BarRequest{
		Symbol: tickflow.Symbol{Exchange: tickflow.SHFE, Product: "rb", YearMon: ym},
		Period: tickflow.MustIntraday(1), From: from, To: to,
	}
}
