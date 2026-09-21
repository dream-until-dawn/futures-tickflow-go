package shinnysource

import (
	"bufio"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/embedded"
)

// v0.10 L8 断线读数与 Live 实跑（probe.md 6.39；判对先落文）。连真天勤，点名才跑：
//
//	TICKFLOW_LIVE=1 TICKFLOW_L8_OUT=<仓库外的目录> TICKFLOW_L8_INS=<事先定的主力合约，如 SHFE.rb2701> \
//	TICKFLOW_L8_BEGIN=2026-09-22T08:59:30+08:00 TICKFLOW_L8_DROP=2026-09-22T09:15:00+08:00 \
//	TICKFLOW_L8_END=2026-09-22T09:30:10+08:00 TICKFLOW_L8_START=2026-09-21T21:00:00+08:00 \
//	go test ./source/shinnysource/ -run '^TestLiveL8Real$' -count=1 -v -timeout 0
//
// ⛔ -timeout 0（或 ≥ 14h）：这一格先等到 BEGIN 再跑半小时，go test 默认 10 分钟就 panic —— 测试开头断言 t.Deadline()。
// ⛔ 合约由 TICKFLOW_L8_INS 事先定（用户可见）；公开合约表只作对照，取不到不影响起跑。
//
// ⛔ 边界：只用行情通道（鉴权 · 名称服务 · set_chart / subscribe_quote / peek_message）＋ 一次公开合约表（不用账户）。
// ⛔ 落盘文件必须在仓库外；只报缺了哪几个键名，不打印任何凭证值。
// 两路：甲 原始记录（DROP 时关掉、立刻开第二条）· 乙 Live（DROP ＋ 30 秒时关掉它的推送连接 ⇒ L8 重连）。

const l8Chart = "l8raw"

// l8Rec 是甲落盘的一行：第几条连接 · 本机收到时刻 · 原始帧。
type l8Rec struct {
	Conn int             `json:"conn"`
	Recv int64           `json:"recv_ms"`
	Raw  json.RawMessage `json:"raw"`
}

// l8Out 是乙落盘的一行：交出时的本机时刻 ＋ 交出的根。
type l8Out struct {
	When int64   `json:"when_ms"`
	Ts   int64   `json:"ts"`
	Day  int     `json:"day"`
	O    float64 `json:"o"`
	H    float64 `json:"h"`
	L    float64 `json:"l"`
	C    float64 `json:"c"`
	V    float64 `json:"v"`
	OI   float64 `json:"oi"`
}

// —— 分析（离线；TestL8AnalyzeCalibration 标定）——

type l8Conn struct {
	final    map[int64]Row // 这条连接上每个 id 的最后值
	firstIDs []int64       // 这条连接上第一帧带 K 线的 id（升序）
	maxID    int64
}

func (c *l8Conn) done(id int64) bool { _, ok := c.final[id+1]; return ok }

// l8Parse 把甲的落盘行按连接拆开，逐帧并快照、记每个 id 的最后值（与 liveCore.feed 同一取法：只看这一帧碰到的 id）。
func l8Parse(recs []l8Rec, ins string) (map[int]*l8Conn, error) {
	conns := map[int]*l8Conn{}
	snaps := map[int]map[string]any{}
	for _, r := range recs {
		c := conns[r.Conn]
		if c == nil {
			c = &l8Conn{final: map[int64]Row{}, maxID: -1}
			conns[r.Conn], snaps[r.Conn] = c, map[string]any{}
		}
		dec := json.NewDecoder(strings.NewReader(string(r.Raw)))
		dec.UseNumber()
		var m struct {
			Aid  string           `json:"aid"`
			Data []map[string]any `json:"data"`
		}
		if err := dec.Decode(&m); err != nil {
			return nil, fmt.Errorf("连接 %d 本机 %s 的帧解不开：%w", r.Conn, fmtMs(r.Recv), err)
		}
		if m.Aid != "rtn_data" {
			continue
		}
		touched := map[int64]bool{}
		for _, d := range m.Data {
			if kd := obj(d, "klines", ins, minuteKey, "data"); kd != nil {
				for k := range kd {
					if id, err := strconv.ParseInt(k, 10, 64); err == nil {
						touched[id] = true
					}
				}
			}
			merge(snaps[r.Conn], d)
		}
		data := obj(snaps[r.Conn], "klines", ins, minuteKey, "data")
		var ids []int64
		for id := range touched {
			b := obj(data, strconv.FormatInt(id, 10))
			if b == nil {
				continue
			}
			row, err := parseRow(id, b)
			if err != nil {
				return nil, err
			}
			c.final[id] = row
			c.maxID = max(c.maxID, id)
			ids = append(ids, id)
		}
		if c.firstIDs == nil && len(ids) > 0 {
			sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
			c.firstIDs = ids
		}
	}
	return conns, nil
}

type l8Result struct {
	// R1
	prevMax, firstMin, firstMax int64
	firstN                      int
	// R2
	r2Compared int
	r2Diff     []string
	// R3
	r3Compared, r3IDDiff int
	r3ValDiff            []string
	// R4
	r4N, r4NotInHist int
	r4First          int64
	r4Contiguous     bool
	r4Diff           []string
}

func sameVals(a, b [6]float64) bool {
	for i := range a {
		if math.Float64bits(a[i]) != math.Float64bits(b[i]) {
			return false
		}
	}
	return true
}

func rowVals(r Row) [6]float64 {
	return [6]float64{r.Open, r.High, r.Low, r.Close, r.Volume, r.CloseOI}
}
func outVals(o l8Out) [6]float64 { return [6]float64{o.O, o.H, o.L, o.C, o.V, o.OI} }

// l8Analyze 算 R1 – R4（probe.md 6.39 二）。hist 是历史通道的真值；live 是乙交出的根。
func l8Analyze(conns map[int]*l8Conn, hist []Row, live []l8Out) l8Result {
	var r l8Result
	c1, c2 := conns[1], conns[2]
	if c1 != nil && c2 != nil && len(c2.firstIDs) > 0 {
		r.prevMax, r.firstMin, r.firstMax, r.firstN = c1.maxID, c2.firstIDs[0], c2.firstIDs[len(c2.firstIDs)-1], len(c2.firstIDs)
		var ids []int64
		for id := range c1.final {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		for _, id := range ids {
			b, ok := c2.final[id]
			if !ok || !c1.done(id) || !c2.done(id) {
				continue
			}
			r.r2Compared++
			if a := c1.final[id]; !sameVals(rowVals(a), rowVals(b)) || a.Datetime != b.Datetime {
				r.r2Diff = append(r.r2Diff, fmt.Sprintf("id %d：断前 %s · 重发 %s", id, showRow(a), showRow(b)))
			}
		}
	}
	// 甲里已完结的根（第二条连接优先），按开盘时刻
	push := map[int64]Row{}
	for _, n := range []int{1, 2} {
		if c := conns[n]; c != nil {
			for id, row := range c.final {
				if c.done(id) {
					push[row.Datetime] = row
				}
			}
		}
	}
	byTs := map[int64]int{}
	for i, h := range hist {
		byTs[h.Datetime/1e6] = i
		p, ok := push[h.Datetime]
		if !ok {
			continue
		}
		r.r3Compared++
		if p.ID != h.ID {
			r.r3IDDiff++
			continue
		}
		if !sameVals(rowVals(p), rowVals(h)) {
			r.r3ValDiff = append(r.r3ValDiff, fmt.Sprintf("id %d：推送 %s · 历史 %s", h.ID, showRow(p), showRow(h)))
		}
	}
	r.r4N, r.r4Contiguous = len(live), true
	if len(live) > 0 {
		r.r4First = live[0].Ts
	}
	prev := -1
	for _, o := range live {
		i, ok := byTs[o.Ts]
		if !ok {
			r.r4NotInHist++
			r.r4Contiguous = false
			continue
		}
		if prev >= 0 && i != prev+1 {
			r.r4Contiguous = false
		}
		prev = i
		if !sameVals(outVals(o), rowVals(hist[i])) {
			r.r4Diff = append(r.r4Diff, fmt.Sprintf("%s：交出 %v · 历史 %s", fmtTs(o.Ts), outVals(o), showRow(hist[i])))
		}
	}
	return r
}

// guard: 6.39 的分析函数先标定：全等 ⇒ 各格 0；改一根断前的收盘 ⇒ R2 报 1；真值挪一根 id ⇒ R3 报 1；Live 交出的改一根 ⇒ R4 报 1。
func TestL8AnalyzeCalibration(t *testing.T) {
	m := at2(2026, 9, 4, 9, 30, 0, 0)
	row := func(id int64) Row {
		c := 3000 + float64(id)
		return Row{ID: id, Datetime: (m + (id-100)*60000) * 1e6, Open: c, High: c + 1, Low: c - 1, Close: c, Volume: 1, CloseOI: 100}
	}
	frame := func(ids []int64, over map[int64]float64) json.RawMessage {
		data := map[string]any{}
		for _, id := range ids {
			r := row(id)
			c := r.Close
			if v, ok := over[id]; ok {
				c = v
			}
			data[strconv.FormatInt(id, 10)] = map[string]any{"datetime": r.Datetime, "open": r.Open, "high": r.High, "low": r.Low, "close": c, "volume": r.Volume, "close_oi": r.CloseOI}
		}
		b, _ := json.Marshal(map[string]any{"aid": "rtn_data", "data": []any{map[string]any{"klines": map[string]any{liveSym.Native(): map[string]any{minuteKey: map[string]any{"data": data}}}}}})
		return b
	}
	var hist []Row
	for id := int64(100); id <= 120; id++ {
		hist = append(hist, row(id))
	}
	build := func(over1 map[int64]float64) []l8Rec {
		return []l8Rec{
			{1, 1, frame(span(100, 110), over1)},
			{2, 2, frame(span(101, 115), nil)},
		}
	}
	var live []l8Out
	for id := int64(100); id <= 114; id++ {
		r := row(id)
		live = append(live, l8Out{Ts: r.Datetime / 1e6, O: r.Open, H: r.High, L: r.Low, C: r.Close, V: r.Volume, OI: r.CloseOI})
	}
	run := func(recs []l8Rec, hist []Row, live []l8Out) l8Result {
		conns, err := l8Parse(recs, liveSym.Native())
		if err != nil {
			t.Fatal(err)
		}
		return l8Analyze(conns, hist, live)
	}
	base := run(build(nil), hist, live)
	if base.prevMax != 110 || base.firstMin != 101 || base.firstMax != 115 || base.r2Compared != 9 || len(base.r2Diff) != 0 ||
		base.r3Compared != 15 || base.r3IDDiff != 0 || len(base.r3ValDiff) != 0 || base.r4N != 15 || !base.r4Contiguous || len(base.r4Diff) != 0 || base.r4NotInHist != 0 {
		t.Fatalf("全等基线：%+v", base)
	}
	if r := run(build(map[int64]float64{105: 9999}), hist, live); len(r.r2Diff) != 1 || !strings.Contains(r.r2Diff[0], "id 105") {
		t.Errorf("断前 105 改收盘：R2 %v，应报 id 105 一根", r.r2Diff)
	}
	h2 := append([]Row(nil), hist...)
	h2[7].ID = 999
	if r := run(build(nil), h2, live); r.r3IDDiff != 1 {
		t.Errorf("真值里挪一根 id：R3 id 不同 %d，应为 1", r.r3IDDiff)
	}
	l2 := append([]l8Out(nil), live...)
	l2[3].C = 1
	if r := run(build(nil), hist, l2); len(r.r4Diff) != 1 {
		t.Errorf("Live 交出的改一根：R4 %v，应报 1 根", r.r4Diff)
	}
	l3 := append(append([]l8Out(nil), live[:5]...), live[6:]...)
	if r := run(build(nil), hist, l3); r.r4Contiguous {
		t.Errorf("Live 交出的中间缺一根：R4 紧接应为假")
	}
}

// —— 联网 ——

type l8Counter struct {
	next http.RoundTripper
	ws   atomic.Int64
}

func (c *l8Counter) RoundTrip(r *http.Request) (*http.Response, error) {
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		c.ws.Add(1)
	}
	return c.next.RoundTrip(r)
}

func l8Env(t *testing.T) map[string]string {
	t.Helper()
	keys := []string{"TICKFLOW_L8_OUT", "TICKFLOW_L8_INS", "TICKFLOW_L8_BEGIN", "TICKFLOW_L8_DROP", "TICKFLOW_L8_END", "TICKFLOW_L8_START"}
	v := map[string]string{}
	var missing []string
	for _, k := range keys {
		if v[k] = os.Getenv(k); v[k] == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		t.Skipf("6.39 的实跑要给齐 %s", strings.Join(missing, " / "))
	}
	return v
}

func l8Time(t *testing.T, s string) int64 {
	t.Helper()
	x, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("时刻 %q：%v", s, err)
	}
	return x.UnixMilli()
}

// l8Underlying 从公开合约表取 KQ.m@SHFE.rb 的 underlying_symbol（不用账户）。
func l8Underlying(ctx context.Context) (tickflow.Symbol, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://openmd.shinnytech.com/t/md/symbols/latest.json", nil)
	req.Header.Set("User-Agent", DefaultUserAgent)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return tickflow.Symbol{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return tickflow.Symbol{}, fmt.Errorf("合约表 HTTP %d", resp.StatusCode)
	}
	var all map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&all); err != nil {
		return tickflow.Symbol{}, err
	}
	var e struct {
		U string `json:"underlying_symbol"`
	}
	if err := json.Unmarshal(all["KQ.m@SHFE.rb"], &e); err != nil || e.U == "" {
		return tickflow.Symbol{}, fmt.Errorf("合约表里 KQ.m@SHFE.rb 没有 underlying_symbol")
	}
	return tickflow.ParseSymbol(e.U)
}

func fileMD5(p string) string {
	f, err := os.Open(p)
	if err != nil {
		return "（读不到：" + err.Error() + "）"
	}
	defer f.Close()
	h := md5.New()
	io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil))
}

func TestLiveL8Real(t *testing.T) {
	env := l8Env(t)
	cal, err := embedded.New([]tickflow.TradingDay{20260921, 20260922})
	if err != nil {
		t.Fatal(err)
	}
	cfg := liveConfig(t, cal)
	out, err := filepath.Abs(env["TICKFLOW_L8_OUT"])
	if err != nil {
		t.Fatal(err)
	}
	repo, _ := filepath.Abs(filepath.Join("..", ".."))
	if rel, err := filepath.Rel(strings.ToLower(repo), strings.ToLower(out)); err == nil && !strings.HasPrefix(rel, "..") {
		t.Fatalf("落盘目录 %s 在仓库里 —— 原始帧不进仓，换一个仓库外的目录", out)
	}
	begin, drop, end, start := l8Time(t, env["TICKFLOW_L8_BEGIN"]), l8Time(t, env["TICKFLOW_L8_DROP"]), l8Time(t, env["TICKFLOW_L8_END"]), l8Time(t, env["TICKFLOW_L8_START"])
	if !(begin < drop && drop+30000 < end) {
		t.Fatalf("时刻次序不对：BEGIN < DROP、DROP ＋ 30 秒 < END")
	}
	if dl, ok := t.Deadline(); ok && dl.Before(time.UnixMilli(end).Add(5*time.Minute)) {
		t.Fatalf("go test 的期限 %s 早于 END ＋ 5 分钟 —— 用 -timeout 0（或 ≥ 14h）", dl.Format(time.RFC3339))
	}
	sym, err := tickflow.ParseSymbol(env["TICKFLOW_L8_INS"])
	if err != nil {
		t.Fatalf("TICKFLOW_L8_INS：%v", err)
	}
	// 按墙钟等（每秒看一次）：time.Sleep 走单调时钟，机器夜里挂起过的话会醒晚
	if time.Now().UnixMilli() < begin {
		t.Logf("等到 %s（按墙钟每秒看一次）", fmtMs(begin))
	}
	for time.Now().UnixMilli() < begin {
		time.Sleep(time.Second)
	}
	if now := time.Now().UnixMilli(); now > begin+30000 {
		t.Fatalf("醒来时已是 %s，晚于 BEGIN ＋ 30 秒 —— 窗口错位，不联网", fmtMs(now))
	}
	ctx := context.Background()
	uctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	if u, err := l8Underlying(uctx); err != nil {
		t.Logf("公开合约表（只作对照）没取到：%v —— 照 TICKFLOW_L8_INS 跑", err)
	} else if u != sym {
		t.Logf("⚠️ 公开合约表说主力是 %s，与 TICKFLOW_L8_INS %s 不同 —— 照 TICKFLOW_L8_INS 跑（写进读数）", u.Native(), sym.Native())
	} else {
		t.Logf("公开合约表与 TICKFLOW_L8_INS 一致：%s", sym.Native())
	}
	cancel()
	ins := sym.Native()
	t.Logf("合约 %s · 起始格 %s · 甲断 %s · 乙断 %s · 结束 %s", ins, fmtMs(start), fmtMs(drop), fmtMs(drop+30000), fmtMs(end))

	mk := func() (*Client, *l8Counter) {
		cnt := &l8Counter{next: http.DefaultTransport}
		c := cfg
		c.HTTPClient = &http.Client{Timeout: 30 * time.Second, Transport: cnt}
		cl, err := New(c)
		if err != nil {
			t.Fatal(err)
		}
		return cl, cnt
	}
	rawCl, rawCnt := mk()
	liveCl, liveCnt := mk()
	rawPath, livePath, histPath := filepath.Join(out, "l8_raw.jsonl"), filepath.Join(out, "l8_live.jsonl"), filepath.Join(out, "l8_hist.jsonl")

	var wg sync.WaitGroup
	var rawErr, liveErr error
	var liveN, liveWarn, liveConns int
	wg.Add(2)
	go func() { // 甲
		defer wg.Done()
		f, err := os.Create(rawPath)
		if err != nil {
			rawErr = err
			return
		}
		defer f.Close()
		w := bufio.NewWriter(f)
		defer w.Flush()
		enc := json.NewEncoder(w)
		for n, until := range []int64{drop, end} {
			conn, err := rawCl.dial(ctx)
			if err != nil {
				rawErr = fmt.Errorf("第 %d 条连接：%w", n+1, err)
				return
			}
			for _, rq := range []map[string]any{
				{"aid": "subscribe_quote", "ins_list": ins},
				{"aid": "set_chart", "chart_id": l8Chart, "ins_list": ins, "duration": minuteNs, "view_width": liveWidth},
				{"aid": "peek_message"},
			} {
				b, _ := json.Marshal(rq)
				if err := conn.Write(ctx, websocket.MessageText, b); err != nil {
					rawErr = err
					conn.CloseNow()
					return
				}
			}
			rctx, cancel := context.WithDeadline(ctx, time.UnixMilli(until))
			peek, _ := json.Marshal(map[string]any{"aid": "peek_message"})
			for {
				_, msg, err := conn.Read(rctx)
				recv := time.Now().UnixMilli()
				if err != nil {
					if rctx.Err() == nil {
						rawErr = fmt.Errorf("第 %d 条连接读断：%w", n+1, err)
					}
					break
				}
				enc.Encode(l8Rec{Conn: n + 1, Recv: recv, Raw: msg})
				if strings.Contains(string(msg), `"rtn_data"`) {
					conn.Write(rctx, websocket.MessageText, peek)
				}
			}
			cancel()
			conn.CloseNow()
			if rawErr != nil {
				return
			}
		}
	}()
	go func() { // 乙
		defer wg.Done()
		f, err := os.Create(livePath)
		if err != nil {
			liveErr = err
			return
		}
		defer f.Close()
		w := bufio.NewWriter(f)
		defer w.Flush()
		enc := json.NewEncoder(w)
		live, err := liveCl.Live(sym, start, LiveOptions{})
		if err != nil {
			liveErr = err
			return
		}
		defer live.Close()
		dropped := false
		for time.Now().UnixMilli() < end {
			// 到点就断，不等 Next 超时（那一刻若一直有根交出，Next 不会超时）
			if !dropped && time.Now().UnixMilli() >= drop+30000 && live.conn != nil {
				live.conn.CloseNow() // 主动断一次（L8）
				dropped = true
			}
			dl := end
			if !dropped {
				dl = drop + 30000
			}
			nctx, cancel := context.WithDeadline(ctx, time.UnixMilli(dl))
			b, err := live.Next(nctx)
			cancel()
			if err == nil {
				enc.Encode(l8Out{When: time.Now().UnixMilli(), Ts: b.Ts, Day: int(b.TradingDay), O: b.Open, H: b.High, L: b.Low, C: b.Close, V: b.Volume, OI: b.OpenInterest})
				liveN++
				continue
			}
			if errors.Is(err, context.DeadlineExceeded) {
				if !dropped && time.Now().UnixMilli() >= drop+30000 && live.conn != nil {
					live.conn.CloseNow() // 主动断一次（L8）
					dropped = true
				}
				continue
			}
			liveErr = err
			break
		}
		liveWarn, _ = live.Warnings()
		liveConns = live.conns
	}()
	wg.Wait()

	hctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	histEnd := end / 60000 * 60000 // 只要已收盘的：END 所在那一分钟那根还在变
	hist, err := rawCl.fetch(hctx, ins, start, histEnd, viewWidth(int((histEnd-start)/60000)))
	cancel()
	if err != nil {
		t.Fatalf("真值（历史通道）：%v", err)
	}
	if f, err := os.Create(histPath); err == nil {
		enc := json.NewEncoder(f)
		for _, h := range hist {
			enc.Encode(h)
		}
		f.Close()
	}
	t.Logf("落盘 md5：甲 %s · 乙 %s · 真值 %s", fileMD5(rawPath), fileMD5(livePath), fileMD5(histPath))
	t.Logf("甲 报错 %v · 行情握手 %d ｜ 乙 报错 %v · 交出 %d · 推送连接 %d · 行情握手 %d · 偏慢告警 %d", rawErr, rawCnt.ws.Load(), liveErr, liveN, liveConns, liveCnt.ws.Load(), liveWarn)
	res, err := l8AnalyzeFiles(rawPath, livePath, hist, ins)
	if err != nil {
		t.Fatalf("分析：%v", err)
	}
	l8Report(t, res)
	l8Lag(t, cal, sym, livePath)
}

// l8Lag：L4 在实盘上的第一份读数（只印，不作判据）—— 交出时刻 − 收盘，非末根与时段末根分开（与回放那一行同一口径）。
func l8Lag(t *testing.T, cal tickflow.Calendar, sym tickflow.Symbol, livePath string) {
	t.Helper()
	f, err := os.Open(livePath)
	if err != nil {
		t.Logf("L4 读数：%v", err)
		return
	}
	defer f.Close()
	c := newLiveCore(cal, sym, 0, LiveOptions{})
	var lag, seg []int64
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var o l8Out
		if json.Unmarshal(sc.Bytes(), &o) != nil {
			continue
		}
		d := o.When - (o.Ts + 60000)
		if end, _ := c.isSegmentEnd(tickflow.Bar{TsEnd: o.Ts + 60000, TradingDay: tickflow.TradingDay(o.Day)}); end {
			seg = append(seg, d)
		} else {
			lag = append(lag, d)
		}
	}
	t.Logf("L4 读数（交出时刻 − 收盘，本机，毫秒；起步补齐那一批是一次交出的，偏大属实）：非末根 %s · 时段末根 %v", distMs(lag), seg)
}

// l8AnalyzeFiles 从落盘文件读回来再分析（与离线复算同一条路）。
func l8AnalyzeFiles(rawPath, livePath string, hist []Row, ins string) (l8Result, error) {
	var recs []l8Rec
	var live []l8Out
	for _, x := range []struct {
		path string
		add  func([]byte) error
	}{
		{rawPath, func(b []byte) error { var r l8Rec; err := json.Unmarshal(b, &r); recs = append(recs, r); return err }},
		{livePath, func(b []byte) error { var o l8Out; err := json.Unmarshal(b, &o); live = append(live, o); return err }},
	} {
		f, err := os.Open(x.path)
		if err != nil {
			return l8Result{}, err
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64<<20), 64<<20)
		for sc.Scan() {
			if err := x.add(sc.Bytes()); err != nil {
				f.Close()
				return l8Result{}, err
			}
		}
		f.Close()
	}
	conns, err := l8Parse(recs, ins)
	if err != nil {
		return l8Result{}, err
	}
	return l8Analyze(conns, hist, live), nil
}

func l8Report(t *testing.T, r l8Result) {
	t.Helper()
	t.Logf("R1 重发起点：断前最大 id %d · 重连后第一帧 id %d – %d（%d 根）⇒ 最小 − 断前最大 ＝ %d", r.prevMax, r.firstMin, r.firstMax, r.firstN, r.firstMin-r.prevMax)
	t.Logf("R2 重发相等：比了 %d 根 · 不等 %d 根 %v", r.r2Compared, len(r.r2Diff), r.r2Diff)
	t.Logf("R3 编号同一套：比了 %d 根 · id 不同 %d · 值不等 %d %v", r.r3Compared, r.r3IDDiff, len(r.r3ValDiff), r.r3ValDiff)
	t.Logf("R4 Live：交出 %d · 首根 %s · 紧接 %v · 不在真值里 %d · 与真值不等 %d %v", r.r4N, fmtTs(r.r4First), r.r4Contiguous, r.r4NotInHist, len(r.r4Diff), r.r4Diff)
}
