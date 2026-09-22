package shinnysource

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/embedded"
)

// v0.11 停夜盘那一晚的实盘（probe.md 6.41；判对先落文）。连真天勤，点名才跑：
//
//	甲（9/30 晚）  TICKFLOW_LIVE=1 TICKFLOW_NN_OUT=<仓库外的目录> TICKFLOW_NN_INS=<事先定的主力合约> \
//	              TICKFLOW_NN_BEGIN=2026-09-30T20:59:00+08:00 TICKFLOW_NN_END=2026-09-30T21:10:00+08:00 \
//	              go test ./source/shinnysource/ -run '^TestNoNightRealEvening$' -count=1 -v -timeout 0
//	乙（10/8 收盘后）同样的 OUT / INS，BEGIN=2026-10-08T15:10:00+08:00 END=2026-10-08T15:15:00+08:00，-run '^TestNoNightRealFetch$'
//
// ⛔ 边界：只用行情通道（鉴权 · 名称服务 · set_chart / subscribe_quote / peek_message）。落盘文件必须在仓库外；只报缺的键名，不打印凭证值。
// ⛔ 运行面照 6.39：-timeout 0 ＋ 期限断言 · 按墙钟等 · 醒晚 30 秒不联网。

var (
	nnDays = []tickflow.TradingDay{20260929, 20260930, 20261008, 20261009}
	// 时刻用 time.Date 算，不手写毫秒数（第一版手算，把 02:30 算成了 02:10 —— TestNoNightCountCalibration 抓到的）
	nnEve       = nnAt(2026, 9, 30, 21, 0) // 夜盘本该开始的那一刻
	nnNightTo   = nnAt(2026, 10, 1, 2, 30) // 夜盘最晚的收盘（au / ag）；甲一只数 [nnEve, nnNightTo)
	nnFetchFrom = nnAt(2026, 9, 30, 20, 0)
	nnDayOpen   = nnAt(2026, 10, 8, 9, 0)
	nnDayClose  = nnAt(2026, 10, 8, 15, 0)
)

func nnAt(y int, mo time.Month, d, h, m int) int64 {
	return time.Date(y, mo, d, h, m, 0, 0, tickflow.CST).UnixMilli()
}

// nnCount：rows 里开盘时刻落在 [from, to) 的根数与成交量合计。
func nnCount(rows []Row, from, to int64) (n int, vol float64) {
	for _, r := range rows {
		if ts := r.Datetime / 1e6; ts >= from && ts < to {
			n++
			vol += r.Volume
		}
	}
	return n, vol
}

// guard: 6.41 的计数先标定：边界左闭右开 · 成交量累加 · 常量就是写在注释里的那几个时刻。
func TestNoNightCountCalibration(t *testing.T) {
	for _, c := range []struct {
		ms   int64
		want string
	}{{nnEve, "2026-09-30 21:00"}, {nnNightTo, "2026-10-01 02:30"}, {nnFetchFrom, "2026-09-30 20:00"}, {nnDayOpen, "2026-10-08 09:00"}, {nnDayClose, "2026-10-08 15:00"}} {
		if got := fmtTs(c.ms); got != c.want {
			t.Errorf("常量 %d ＝ %s，应为 %s", c.ms, got, c.want)
		}
	}
	rows := []Row{{Datetime: (nnEve - 60000) * 1e6, Volume: 5}, {Datetime: nnEve * 1e6, Volume: 7}, {Datetime: (nnNightTo - 60000) * 1e6, Volume: 0}, {Datetime: nnNightTo * 1e6, Volume: 9}}
	if n, v := nnCount(rows, nnEve, nnNightTo); n != 2 || v != 7 {
		t.Errorf("nnCount：%d 根、量 %g；应为 2 根（左闭右开）、量 7", n, v)
	}
}

func nnEnv(t *testing.T) map[string]string {
	t.Helper()
	v := map[string]string{}
	var missing []string
	for _, k := range []string{"TICKFLOW_NN_OUT", "TICKFLOW_NN_INS", "TICKFLOW_NN_BEGIN", "TICKFLOW_NN_END"} {
		if v[k] = os.Getenv(k); v[k] == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		t.Skipf("6.41 的实跑要给齐 %s", strings.Join(missing, " / "))
	}
	return v
}

// nnPrepare：凭证 · 仓库外的落盘目录 · 期限断言 · 按墙钟等到 BEGIN（醒晚 30 秒不联网）。返回 plain / 注入 两个日历、合约、落盘目录、END。
func nnPrepare(t *testing.T) (Config, tickflow.Calendar, tickflow.Calendar, tickflow.Symbol, string, int64) {
	t.Helper()
	env := nnEnv(t)
	plain, err := embedded.New(nnDays)
	if err != nil {
		t.Fatal(err)
	}
	inj, err := embedded.New(nnDays, embedded.NoNightAfter(20260930))
	if err != nil {
		t.Fatal(err)
	}
	cfg := liveConfig(t, plain)
	out, err := filepath.Abs(env["TICKFLOW_NN_OUT"])
	if err != nil {
		t.Fatal(err)
	}
	repo, _ := filepath.Abs(filepath.Join("..", ".."))
	if rel, err := filepath.Rel(strings.ToLower(repo), strings.ToLower(out)); err == nil && !strings.HasPrefix(rel, "..") {
		t.Fatalf("落盘目录 %s 在仓库里 —— 原始帧不进仓，换一个仓库外的目录", out)
	}
	begin, end := l8Time(t, env["TICKFLOW_NN_BEGIN"]), l8Time(t, env["TICKFLOW_NN_END"])
	if begin >= end {
		t.Fatal("BEGIN 必须早于 END")
	}
	if dl, ok := t.Deadline(); ok && dl.Before(time.UnixMilli(end).Add(5*time.Minute)) {
		t.Fatalf("go test 的期限 %s 早于 END ＋ 5 分钟 —— 用 -timeout 0", dl.Format(time.RFC3339))
	}
	sym, err := tickflow.ParseSymbol(env["TICKFLOW_NN_INS"])
	if err != nil {
		t.Fatalf("TICKFLOW_NN_INS：%v", err)
	}
	if time.Now().UnixMilli() < begin {
		t.Logf("等到 %s（按墙钟每秒看一次）", fmtMs(begin))
	}
	for time.Now().UnixMilli() < begin {
		time.Sleep(time.Second)
	}
	if now := time.Now().UnixMilli(); now > begin+30000 {
		t.Fatalf("醒来时已是 %s，晚于 BEGIN ＋ 30 秒 —— 窗口错位，不联网", fmtMs(now))
	}
	return cfg, plain, inj, sym, out, end
}

// nnEvent 是甲二 / 甲三落盘的一行：交出的根或报错，连同本机时刻。
type nnEvent struct {
	When  int64  `json:"when_ms"`
	Kind  string `json:"kind"` // bar · err
	Ts    int64  `json:"ts,omitempty"`
	Msg   string `json:"msg,omitempty"`
	Layer string `json:"layer,omitempty"` // 报错属于哪一层（nnLayer）
}

// nnLayer 把 Live 的报错分层（评审方 F3：连接层的错不能读成修法失败）：
// 判定层 ＝ Live 自己按日历 / 数据判出来的（冻结 · 快照过旧 · 时钟 · 修正 · 跳号 · 起步补齐 · 两通道不一致）；
// 连接层 ＝ 断线重连失败，以及没有归到判定层的一切（拨号、鉴权、读写）；调用方 ＝ 太久没取帧。
func nnLayer(err error) string {
	for _, x := range []struct {
		e    error
		name string
	}{
		{ErrSuspectedFreeze, "判定：冻结"}, {ErrStaleSnapshot, "判定：快照过旧"}, {ErrClockSkew, "判定：时钟偏快"},
		{ErrCorrectedAfterDelivery, "判定：交出后被改"}, {ErrIDGap, "判定：跳号"}, {ErrStartGap, "判定：起步补不齐"},
		{ErrChannelsDisagree, "判定：两通道不一致"}, {ErrConsumerStalled, "调用方：太久没取"}, {ErrDisconnected, "连接：重连失败"},
	} {
		if errors.Is(err, x.e) {
			return x.name
		}
	}
	return "连接：其它（未归到判定层）"
}

// guard: 报错分层先标定 —— 每个哨兵归到它该在的那一层；包了一层的照样认；不认识的一律算连接层（不许被读成「修法失败」）。
func TestNoNightLayerCalibration(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{ErrSuspectedFreeze, "判定：冻结"}, {fmt.Errorf("包一层：%w", ErrStaleSnapshot), "判定：快照过旧"},
		{ErrDisconnected, "连接：重连失败"}, {errors.New("dial tcp: i/o timeout"), "连接：其它（未归到判定层）"},
		{ErrConsumerStalled, "调用方：太久没取"},
	} {
		if got := nnLayer(c.err); got != c.want {
			t.Errorf("nnLayer(%v) ＝ %s，应为 %s", c.err, got, c.want)
		}
	}
}

func TestNoNightRealEvening(t *testing.T) {
	cfg, plain, inj, sym, out, end := nnPrepare(t)
	ins := sym.Native()
	startOf := func(cal tickflow.Calendar) int64 {
		d, err := cal.DayOf(sym.ProductKey(), 20261008)
		if err != nil {
			t.Fatal(err)
		}
		return d.Sessions[0].Start
	}
	startInj, startPlain := startOf(inj), startOf(plain)
	t.Logf("合约 %s · 起始格（两路同用）%s · 不注入日历下 10/08 第一段 %s（只印，不用）· 结束 %s", ins, fmtMs(startInj), fmtMs(startPlain), fmtMs(end))
	mk := func(cal tickflow.Calendar) *Client {
		c := cfg
		c.Calendar = cal
		c.HTTPClient = &http.Client{Timeout: 30 * time.Second}
		cl, err := New(c)
		if err != nil {
			t.Fatal(err)
		}
		return cl
	}
	rawPath := filepath.Join(out, "nn_raw.jsonl")
	ctx := context.Background()
	var wg sync.WaitGroup
	var rawErr error
	var rawFrames int
	wg.Add(1)
	go func() { // 甲一：原始记录
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
		conn, err := mk(plain).dial(ctx)
		if err != nil {
			rawErr = err
			return
		}
		defer conn.CloseNow()
		for _, rq := range []map[string]any{
			{"aid": "subscribe_quote", "ins_list": ins},
			{"aid": "set_chart", "chart_id": l8Chart, "ins_list": ins, "duration": minuteNs, "view_width": liveWidth},
			{"aid": "peek_message"},
		} {
			b, _ := json.Marshal(rq)
			if err := conn.Write(ctx, websocket.MessageText, b); err != nil {
				rawErr = err
				return
			}
		}
		rctx, cancel := context.WithDeadline(ctx, time.UnixMilli(end))
		defer cancel()
		peek, _ := json.Marshal(map[string]any{"aid": "peek_message"})
		for {
			_, msg, err := conn.Read(rctx)
			recv := time.Now().UnixMilli()
			if err != nil {
				if rctx.Err() == nil {
					rawErr = fmt.Errorf("读断：%w", err)
				}
				return
			}
			rawFrames++
			enc.Encode(l8Rec{Conn: 1, Recv: recv, Raw: msg})
			if strings.Contains(string(msg), `"rtn_data"`) {
				conn.Write(rctx, websocket.MessageText, peek)
			}
		}
	}()
	run := func(name string, cal tickflow.Calendar, start int64) { // 甲二 / 甲三
		defer wg.Done()
		p := filepath.Join(out, "nn_live_"+name+".jsonl")
		f, err := os.Create(p)
		if err != nil {
			t.Errorf("%s：%v", name, err)
			return
		}
		defer f.Close()
		enc := json.NewEncoder(f)
		live, err := mk(cal).Live(sym, start, LiveOptions{})
		if err != nil {
			enc.Encode(nnEvent{When: time.Now().UnixMilli(), Kind: "err", Msg: err.Error(), Layer: nnLayer(err)})
			return
		}
		defer live.Close()
		nctx, cancel := context.WithDeadline(ctx, time.UnixMilli(end))
		defer cancel()
		for {
			b, err := live.Next(nctx)
			now := time.Now().UnixMilli()
			if err == nil {
				enc.Encode(nnEvent{When: now, Kind: "bar", Ts: b.Ts})
				continue
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				enc.Encode(nnEvent{When: now, Kind: "err", Msg: err.Error(), Layer: nnLayer(err)})
			}
			return
		}
	}
	wg.Add(2)
	go run("inj", inj, startInj)
	go run("plain", plain, startInj) // 评审方 F1：对照只差日历这一个变量（起始格同为 10/08 09:00；不注入日历下它也是交易分钟）
	wg.Wait()
	t.Logf("落盘 md5：甲一 %s · 甲二 %s · 甲三 %s", fileMD5(rawPath), fileMD5(filepath.Join(out, "nn_live_inj.jsonl")), fileMD5(filepath.Join(out, "nn_live_plain.jsonl")))
	nnReport(t, out, rawPath, ins, rawFrames, rawErr)
}

// nnReport 从落盘文件读回来算 N1 – N3（与离线复算同一条路）。
func nnReport(t *testing.T, out, rawPath, ins string, rawFrames int, rawErr error) {
	t.Helper()
	var recs []l8Rec
	f, err := os.Open(rawPath)
	if err != nil {
		t.Fatal(err)
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<20), 64<<20)
	for sc.Scan() {
		var r l8Rec
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		recs = append(recs, r)
	}
	f.Close()
	conns, err := l8Parse(recs, ins)
	if err != nil {
		t.Fatal(err)
	}
	// 所有连接的根都并进来（评审方：甲一若中途断线，第二条连接上的夜盘根不该漏掉；本工具的甲一不重连，断了就停、连接报错照印）
	var rows []Row
	for _, c := range conns {
		for _, r := range c.final {
			rows = append(rows, r)
		}
	}
	n, vol := nnCount(rows, nnEve, nnNightTo)
	afterEve, firstK := 0, int64(0)
	for _, r := range recs {
		if r.Recv >= nnEve {
			afterEve++
		}
		if firstK == 0 && strings.Contains(string(r.Raw), `"klines"`) {
			firstK = r.Recv
		}
	}
	t.Logf("N1 甲一：连接 %d 条 · 帧 %d（21:00 之后 %d）· 连接报错 %v · 夜盘时段的根 %d（量 %g）", len(conns), rawFrames, afterEve, rawErr, n, vol)
	// 评审方 F2：第一帧带根的到达时刻决定 Live 的启动自检落在 21:00 之前还是之后（之后 ⇒ 不注入那一路报的是快照过旧而不是冻结）；
	// Live 自己的帧不落盘，这里用甲一（同一服务器、同时连上）的时刻作近似，照实标「近似」
	if firstK != 0 {
		t.Logf("甲一第一帧带 K 线的到达时刻（近似甲二 / 甲三的）：%s", fmtMs(firstK))
	}
	for _, name := range []string{"inj", "plain"} {
		b, _ := os.ReadFile(filepath.Join(out, "nn_live_"+name+".jsonl"))
		bars, errs := 0, []string{}
		for _, ln := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			var e nnEvent
			if json.Unmarshal([]byte(ln), &e) != nil {
				continue
			}
			if e.Kind == "bar" {
				bars++
			} else {
				errs = append(errs, fmtMs(e.When)+" 〔"+e.Layer+"〕 "+e.Msg)
			}
		}
		t.Logf("%s Live（%s）：交出 %d 根 · 报错 %v", map[string]string{"inj": "N2 甲二", "plain": "N3 甲三"}[name], name, bars, errs)
	}
}

func TestNoNightRealFetch(t *testing.T) {
	cfg, _, _, sym, out, end := nnPrepare(t)
	cfg.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	cl, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithDeadline(context.Background(), time.UnixMilli(end))
	defer cancel()
	rows, err := cl.fetch(ctx, sym.Native(), nnFetchFrom, nnDayClose, viewWidth(600))
	if err != nil {
		t.Fatalf("乙：历史通道：%v", err)
	}
	p := filepath.Join(out, "nn_fetch.jsonl")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	enc := json.NewEncoder(f)
	for _, r := range rows {
		enc.Encode(r)
	}
	f.Close()
	night, nvol := nnCount(rows, nnFetchFrom, nnDayOpen-5*3600000) // [9/30 20:00, 10/8 04:00)
	day, _ := nnCount(rows, nnDayOpen, nnDayClose)
	t.Logf("落盘 md5：乙 %s · 共 %d 行", fileMD5(p), len(rows))
	t.Logf("N4 乙：夜盘时段 [9/30 20:00, 10/8 04:00) 的根 %d（量 %g）· 10/8 日盘 [09:00, 15:00) 的根 %d", night, nvol, day)
}
