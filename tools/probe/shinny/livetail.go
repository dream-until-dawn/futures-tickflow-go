package main

// v0.10 起手 甲 / 乙 的读数（probe.md 6.36；判对先落文，评审方 2026-09-18 16:50:45 +0800 作证）。
//
//	go run . -only shinny-live-tail -tail-end 2026-09-21T15:05:00+08:00 -tail-out <文件>     联网：记推送帧，到墙钟那一刻自己断开
//	go run . -only shinny-live-analyze -tail-in <文件>                离线：只读落盘文件，算 A1 A2 A3 B1 B2 与判对
//	go run . -only shinny-live-analyze -tail-in <文件> -tail-calib     标定：造一次「收盘后 5 秒又改」，A1 / A2 必须各多报一根
//
// ⛔ 边界：只用行情通道 —— 鉴权 · 名称服务 · websocket 的 set_chart / subscribe_quote / peek_message；
// **不发任何交易 / 下单 / 资金类请求**（本文件里出现的 aid 只有这三个，TestLiveTailSendsOnlyMarketAids 钉着）。
// ⛔ 墙钟：一律毫秒时间戳比较，不经时区名；显示用固定偏移 +0800，读数里印偏移（6.36 墙钟规矩）。
//
// 本文件不 import 本库。

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
)

var (
	tailEnd   = flag.String("tail-end", "", "shinny-live-tail 到这一刻断开（RFC3339，带偏移，如 2026-09-21T15:05:00+08:00）；用户授权窗口的终点")
	tailOut   = flag.String("tail-out", "", "shinny-live-tail 的落盘文件（JSONL；不进仓）")
	tailIn    = flag.String("tail-in", "", "shinny-live-analyze 读的落盘文件")
	tailCalib = flag.Bool("tail-calib", false, "shinny-live-analyze 做标定")
)

const (
	tailSym   = "KQ.m@SHFE.rb"
	tailDur   = int64(60) * 1e9
	tailWidth = 10
)

// tailFrame 是落盘的一行：本机收到时刻（毫秒时间戳）＋ 原始帧。
type tailFrame struct {
	RecvMs int64           `json:"recv_ms"`
	Raw    json.RawMessage `json:"raw"`
}

// liveTailAids 是本文件会发的全部 aid —— 只有行情类。
var liveTailAids = []string{"set_chart", "subscribe_quote", "peek_message"}

func showMs(ms int64) string {
	return time.UnixMilli(ms).In(cst).Format("2006-01-02 15:04:05.000 -0700")
}

// outputInsideRepo 报告 path 是否落在仓库里（仓库根按 ../../.. 算，与 dotenv 同一取法；Windows 路径大小写不敏感）。
func outputInsideRepo(path string) bool {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		return true // 算不出仓库根就当在里面（失败方向是「不落盘」）
	}
	out, err := filepath.Abs(path)
	if err != nil {
		return true
	}
	return strings.HasPrefix(strings.ToLower(out), strings.ToLower(root+string(filepath.Separator)))
}

// openmdURL 是天勤公开的合约表（refdata/shinnyref 的 DefaultURL；公开端点，不鉴权、不走账户）。
var openmdURL = "https://openmd.shinnytech.com/t/md/symbols/latest.json"

// openmdTimeout 是取一次合约表的上限（评审方 2026-09-21 裁：20 秒，取不到就记「没取到」继续）。
// 6.37 读数：3 分钟的旧上限在这台机器上两次都耗尽、都没取到，且第一版把它放在连上行情之前，3 分钟算进了授权窗口。
var openmdTimeout = 20 * time.Second

// openmdUnderlying 取 KQ.m 主连【此刻】映射到的具体合约（合约表里的 underlying_symbol）。
// ⚠️ 它是取的那一刻的快照：取数开始、结束各取一次，两次一样才能说「这段时间映射没变」。
func openmdUnderlying() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), openmdTimeout)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, openmdURL, nil)
	req.Header.Set("User-Agent", uaSelf)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var all map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&all); err != nil {
		return "", fmt.Errorf("解合约表失败：%w", err)
	}
	var e struct {
		Underlying string `json:"underlying_symbol"`
	}
	raw, ok := all[tailSym]
	if !ok {
		return "", fmt.Errorf("合约表里没有 %s", tailSym)
	}
	if err := json.Unmarshal(raw, &e); err != nil || e.Underlying == "" {
		return "", fmt.Errorf("%s 那一条没有 underlying_symbol", tailSym)
	}
	return e.Underlying, nil
}

func showUnderlying() string {
	u, err := openmdUnderlying()
	if err != nil {
		return "没取到（" + err.Error() + "）"
	}
	return u
}

func probeLiveTail(md, tok string) {
	const name = "shinny-live-tail"
	if !optIn(name) {
		return
	}
	if *tailOut == "" {
		report(name, "FAIL", "要给 -tail-out（落盘文件，放 scratchpad，不进仓）")
		return
	}
	// 落盘路径在仓库里就拒绝（评审方 2026-09-18）：原始帧不进仓，一次 git add -A 就会把它带进去
	if outputInsideRepo(*tailOut) {
		report(name, "FAIL", "-tail-out 落在仓库里（"+*tailOut+"）—— 原始帧不进仓，换到仓库外")
		return
	}
	// 授权窗口的终点由墙钟硬截（评审方 2026-09-21 裁）：ctx 的截止时刻就是 -tail-end，连拨号在内都受它管；
	// 不再由「分钟数」推终点 —— 6.37 那天前面耽误的 3 分钟（openmd）就这样被加到了终点上，越过了对用户说的 15:05。
	end, err := tailDeadline(*tailEnd, time.Now())
	if err != nil {
		report(name, "FAIL", err.Error())
		return
	}
	ctx, cancel := context.WithDeadline(context.Background(), end)
	defer cancel()
	f, err := os.Create(*tailOut)
	if err != nil {
		report(name, "FAIL", err.Error())
		return
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	fmt.Printf("开始 %s（本机）· 到 %s 断开 · %s 1m · 只用行情通道\n", showMs(time.Now().UnixMilli()), showMs(end.UnixMilli()), tailSym)

	c, _, err := websocket.Dial(ctx, md, &websocket.DialOptions{
		CompressionMode: websocket.CompressionNoContextTakeover,
		HTTPHeader:      http.Header{"User-Agent": {uaSelf}, "Accept": {"application/json"}, "Authorization": {"Bearer " + tok}},
	})
	if err != nil {
		report(name, "FAIL", "连接失败: "+err.Error())
		return
	}
	defer c.CloseNow()
	c.SetReadLimit(256 << 20)
	send := func(v map[string]any) error { b, _ := json.Marshal(v); return c.Write(ctx, websocket.MessageText, b) }
	if err := send(map[string]any{"aid": "subscribe_quote", "ins_list": tailSym}); err != nil {
		report(name, "FAIL", err.Error())
		return
	}
	if err := send(map[string]any{"aid": "set_chart", "chart_id": "tail", "ins_list": tailSym, "duration": tailDur, "view_width": tailWidth}); err != nil {
		report(name, "FAIL", err.Error())
		return
	}
	// 取数前那次 openmd 挪到连上行情之后、放后台（评审方 2026-09-21 裁）：它不再占授权窗口的开头，也不挡收帧 ——
	// 在收帧循环里同步去取，服务器那边会攒帧，本机收到时刻就不是真的收到时刻了。
	before := make(chan string, 1)
	go func() { before <- showUnderlying() }()
	frames := 0
	for {
		if err := send(map[string]any{"aid": "peek_message"}); err != nil {
			break
		}
		_, msg, err := c.Read(ctx)
		if err != nil {
			break // 到点（ctx 截止）或断线；读数照记，断线本身也是乙要看的
		}
		line, _ := json.Marshal(tailFrame{RecvMs: time.Now().UnixMilli(), Raw: msg})
		w.Write(line)
		w.WriteByte('\n')
		frames++
	}
	c.CloseNow()
	fmt.Printf("结束 %s（本机）· 落盘 %d 帧 → %s\n", showMs(time.Now().UnixMilli()), frames, *tailOut)
	// 先落盘、关文件，再去取数后那次 openmd（公开端点、不走账户；6.37 那天它在 Flush 之前，文件 mtime 晚了 3 分钟）
	if err := w.Flush(); err != nil {
		report(name, "FAIL", "落盘失败: "+err.Error())
		return
	}
	f.Close()
	fmt.Printf("KQ.m 映射（openmd 合约表，连上行情之后）：%s\n", <-before)
	fmt.Printf("KQ.m 映射（openmd 合约表，断开之后）：%s\n", showUnderlying())
	report(name, "PASS", fmt.Sprintf("记了 %d 帧；读数用 -only shinny-live-analyze -tail-in 算", frames))
}

// tailDeadline 解析 -tail-end：必须给、必须是带偏移的 RFC3339、必须晚于现在。
// 不接受「分钟数」—— 终点是用户授权的那个墙钟时刻，不是从开工那一刻往后推的。
func tailDeadline(s string, now time.Time) (time.Time, error) {
	if s == "" {
		return time.Time{}, fmt.Errorf("要给 -tail-end（断开的墙钟时刻，RFC3339 带偏移，如 2026-09-21T15:05:00+08:00）")
	}
	end, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("-tail-end %q 不是带偏移的 RFC3339：%v", s, err)
	}
	if !end.After(now) {
		return time.Time{}, fmt.Errorf("-tail-end %s 不晚于现在 %s", showMs(end.UnixMilli()), showMs(now.UnixMilli()))
	}
	return end, nil
}

// ── 离线分析 ──

type barVal struct{ o, h, l, c, v, coi, ooi float64 }

// segEnd 是一处时段末根（6.37）：收盘时刻 C 落在 10:15 · 11:30 · 15:00 的那一根。
type segEnd struct {
	label      string // "10:15" …
	id         int64
	c          int64
	eLoc, eSrv int64 // 最后一次真改：本机收到 − C · quote.datetime − C
	post       int   // C 之后（按 quote.datetime > C）还改这一根的次数
	postLoc    int   // 同上，按本机收到时刻 > C
	hasNext    bool  // 下一根出现在记录里
}

// segEndLabels 是 rb 日盘的三个时段末根的收盘时刻（6.37 按 C 认，不按「下一根多久才来」认）。
var segEndLabels = map[string]bool{"10:15": true, "11:30": true, "15:00": true}

// rbSessions 是 rb 现行的交易时段（+0800 钟面，分钟）。本文件不 import 本库，这里写死；来源：
// 日盘 ＝ calendar/embedded/embedded.go 的 dayCommodity（09:00-10:15 / 10:30-11:30 / 13:30-15:00），
// 夜盘 ＝ 同文件 rb 的夜盘分钟数 120（21:00–23:00）。
var rbSessions = [][2]int{{9 * 60, 10*60 + 15}, {10*60 + 30, 11*60 + 30}, {13*60 + 30, 15 * 60}, {21 * 60, 23 * 60}}

// rbSegment 报告服务器时刻 ms 落在哪一段交易时段（段号 ＝ 自然日 × 10 ＋ 段序；两端都含）；不在任何一段里 ⇒ ok 为 false。
// 乙的 B1s 只比同一段里相邻的两次真改动：段与段之间的空档（小节休息、午休、日夜盘之间）不是「推送停了」。
// ⚠️ 6.37 读数：第一版按「记录期间最早开盘到最晚收盘」一整段取，日盘的午休 7200543 ms 被算成了时段内的空档。
func rbSegment(ms int64) (int64, bool) {
	t := time.UnixMilli(ms).In(cst)
	of := int64(((t.Hour()*60+t.Minute())*60+t.Second())*1000) + int64(t.Nanosecond()/1e6)
	for i, s := range rbSessions {
		if of >= int64(s[0])*60000 && of <= int64(s[1])*60000 {
			return int64(t.Year()*10000+int(t.Month())*100+t.Day())*10 + int64(i), true
		}
	}
	return 0, false
}

type tailResult struct {
	frames, rtn      int
	underlying       string
	bars             int     // 记录期间新出现的根（首帧那批历史不算）
	changes, resends int     // 真改了 · 重发未变
	a1, a2, a3       []int64 // L−C · L−N · 末根 L−C（毫秒；L 与 N 是本机收到时刻）
	a1Pos, a2Pos     int
	// 服务器时刻版（用户 2026-09-18 裁：以服务器时刻为准，不动本机系统设置）：
	// Ls ＝ 最后一次真改那一帧里 quote.datetime（交易所那笔成交的时刻）；不经本机时钟
	a1s, a3s []int64 // Ls−C · 末根 Ls−C
	a1sPos   int
	noSrv    int   // 真改动发生时还没有任何 quote.datetime 可用的次数（这些根不进 a1s / a3s）
	b1s      int64 // 交易时段内（按服务器时刻）相邻两次真改动的最大间隔（本机收到时刻之差）
	// 6.37：D ＝ 本机收到 − quote.datetime，只取本帧带了 quote.datetime、且它不早于记录期间第一根开盘的帧（旧报价排除，事先写死）
	d        []int64
	dStale   int // 被排除的旧报价帧数
	segs     []segEnd
	tdNote   string  // TradingDay（推论，见 tradingDayGuess）
	frameGap []int64 // 相邻帧间隔
	b1       int64   // 相邻两次「有真改动」的帧的最大间隔
	b2       []int64 // 本机收到 − 服务器行情时刻
	endIDs   []string
	calibID  int64
	calibOK  bool // 找到了标定目标（id 0 是合法值，不能拿 calibID == 0 当「没找到」）
}

// sameVal 按位比（字段缺失时 num 给 NaN，而 NaN != NaN —— 直接用 == 会把每次重发都算成「真改了」）。
func sameVal(a, b barVal) bool {
	x := [...]float64{a.o, a.h, a.l, a.c, a.v, a.coi, a.ooi}
	y := [...]float64{b.o, b.h, b.l, b.c, b.v, b.coi, b.ooi}
	for i := range x {
		if math.Float64bits(x[i]) != math.Float64bits(y[i]) {
			return false
		}
	}
	return true
}

func num(v any) float64 {
	f, ok := v.(float64)
	if !ok {
		return math.NaN()
	}
	return f
}

func analyzeTail(frames []tailFrame) tailResult {
	var r tailResult
	snap := map[string]any{}
	durKey := strconv.FormatInt(tailDur, 10)
	last := map[int64]barVal{} // 每根最近一次的值
	lastL := map[int64]int64{} // 每根最后一次真改的本机时刻
	lastS := map[int64]int64{} // 每根最后一次真改时的服务器时刻（quote.datetime）；0 ＝ 那时还没有
	var srvNow int64           // 最近一次解析出的 quote.datetime（毫秒）
	type chg struct{ recv, srv int64 }
	var changeFrames []chg
	events := map[int64][]chg{} // 每根：出现与每次真改的（本机收到, quote.datetime）
	var quoteFrames []chg       // 本帧带了 quote.datetime 的帧
	born := map[int64]int64{}   // 每根第一次出现的本机时刻
	dt := map[int64]int64{}     // 每根 datetime（毫秒）
	initial := map[int64]bool{} // 开始记录时已经收盘的根（view_width 带来的历史，不算记录期间的）
	var lastChangeFrame int64
	for i, fr := range frames {
		r.frames++
		if i > 0 {
			r.frameGap = append(r.frameGap, fr.RecvMs-frames[i-1].RecvMs)
		}
		var m struct {
			Aid  string           `json:"aid"`
			Data []map[string]any `json:"data"`
		}
		if json.Unmarshal(fr.Raw, &m) != nil || m.Aid != "rtn_data" {
			continue
		}
		r.rtn++
		touched := map[int64]bool{}
		quoteHere := false
		for _, d := range m.Data {
			if q := obj(d, "quotes", tailSym); q != nil {
				if _, ok := q["datetime"]; ok {
					quoteHere = true
				}
			}
			if kd := obj(d, "klines", tailSym, durKey, "data"); kd != nil {
				for k := range kd {
					if id, err := strconv.ParseInt(k, 10, 64); err == nil {
						touched[id] = true
					}
				}
			}
			merge(snap, d)
		}
		if q := obj(snap, "quotes", tailSym); q != nil {
			if u, ok := q["underlying_symbol"].(string); ok {
				r.underlying = u
			}
			if s, ok := q["datetime"].(string); ok {
				if t, err := time.ParseInLocation("2006-01-02 15:04:05.000000", s, cst); err == nil {
					r.b2 = append(r.b2, fr.RecvMs-t.UnixMilli())
					srvNow = t.UnixMilli()
					if quoteHere {
						quoteFrames = append(quoteFrames, chg{fr.RecvMs, srvNow})
					}
				}
			}
		}
		data := obj(snap, "klines", tailSym, durKey, "data")
		changed := false
		for id := range touched {
			b := obj(data, strconv.FormatInt(id, 10))
			if b == nil {
				continue
			}
			v := barVal{num(b["open"]), num(b["high"]), num(b["low"]), num(b["close"]), num(b["volume"]), num(b["close_oi"]), num(b["open_oi"])}
			if _, seen := born[id]; !seen {
				born[id] = fr.RecvMs
				dt[id] = int64(num(b["datetime"])) / 1e6
				if dt[id]+60000 <= frames[0].RecvMs {
					initial[id] = true
				}
				last[id], lastL[id], lastS[id] = v, fr.RecvMs, srvNow
				events[id] = append(events[id], chg{fr.RecvMs, srvNow})
				changed = true
				continue
			}
			if sameVal(v, last[id]) {
				r.resends++
				continue
			}
			r.changes++
			last[id], lastL[id], lastS[id] = v, fr.RecvMs, srvNow
			events[id] = append(events[id], chg{fr.RecvMs, srvNow})
			if srvNow == 0 {
				r.noSrv++
			}
			changed = true
		}
		if changed {
			if lastChangeFrame != 0 && fr.RecvMs-lastChangeFrame > r.b1 {
				r.b1 = fr.RecvMs - lastChangeFrame
			}
			lastChangeFrame = fr.RecvMs
			changeFrames = append(changeFrames, chg{fr.RecvMs, srvNow})
		}
		if ser := obj(snap, "klines", tailSym, durKey); ser != nil {
			if e, ok := ser["trading_day_end_id"].(float64); ok {
				s := strconv.FormatInt(int64(e), 10)
				if len(r.endIDs) == 0 || r.endIDs[len(r.endIDs)-1] != s {
					r.endIDs = append(r.endIDs, s)
				}
			}
		}
	}
	var ids []int64
	for id := range born {
		if !initial[id] {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	r.bars = len(ids)
	// 交易时段（按服务器时刻）：记录期间出现的根里最早的开盘到最晚的收盘，且两次改动落在 rb 同一段时段里（rbSegment）
	if len(ids) > 0 {
		lo, hi := dt[ids[0]], dt[ids[0]]+60000 // 按 datetime 取最早开盘与最晚收盘，不依赖 id 的顺序
		for _, id := range ids {
			lo, hi = min(lo, dt[id]), max(hi, dt[id]+60000)
		}
		for i := 1; i < len(changeFrames); i++ {
			a, b := changeFrames[i-1], changeFrames[i]
			sa, okA := rbSegment(a.srv)
			sb, okB := rbSegment(b.srv)
			if a.srv >= lo && b.srv <= hi && a.srv != 0 && okA && okB && sa == sb && b.recv-a.recv > r.b1s {
				r.b1s = b.recv - a.recv
			}
		}
		// D：旧报价（quote.datetime 早于记录期间第一根开盘）不进（6.37 事先写死；6.36 那个 21598052 ms 就是它）
		for _, q := range quoteFrames {
			if q.srv < lo {
				r.dStale++
				continue
			}
			r.d = append(r.d, q.recv-q.srv)
		}
		r.tdNote = tradingDayGuess(lo)
		for _, id := range ids {
			C := dt[id] + 60000
			label := time.UnixMilli(C).In(cst).Format("15:04")
			if !segEndLabels[label] {
				continue
			}
			sg := segEnd{label: label, id: id, c: C, eLoc: lastL[id] - C, eSrv: lastS[id] - C}
			for _, e := range events[id][1:] { // [0] 是出现，不算改
				if e.srv > C {
					sg.post++
				}
				if e.recv > C {
					sg.postLoc++
				}
			}
			_, sg.hasNext = born[id+1]
			r.segs = append(r.segs, sg)
		}
	}
	for _, id := range ids {
		C := dt[id] + 60000
		L := lastL[id]
		Ls, hasS := lastS[id], lastS[id] != 0
		if n, ok := born[id+1]; ok {
			if hasS {
				r.a1s = append(r.a1s, Ls-C)
				if Ls-C > 0 {
					r.a1sPos++
				}
			}
			r.a1 = append(r.a1, L-C)
			if L-C > 0 {
				r.a1Pos++
			}
			r.a2 = append(r.a2, L-n)
			if L-n > 0 {
				r.a2Pos++
			}
			if !r.calibOK {
				r.calibID, r.calibOK = id, true
			}
		} else {
			r.a3 = append(r.a3, L-C)
			if hasS {
				r.a3s = append(r.a3s, Ls-C)
			}
		}
	}
	return r
}

func dist(xs []int64) string {
	if len(xs) == 0 {
		return "（无）"
	}
	s := append([]int64(nil), xs...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	q := func(p float64) int64 { return s[int(math.Min(float64(len(s)-1), math.Floor(p*float64(len(s)))))] }
	return fmt.Sprintf("n=%d 最小 %d · 中位 %d · 95 分位 %d · 最大 %d（毫秒）", len(s), s[0], q(0.5), q(0.95), s[len(s)-1])
}

// pct 取分位（p ∈ [0,1]）；空切片给 0。
func pct(xs []int64, p float64) int64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]int64(nil), xs...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[int(math.Min(float64(len(s)-1), math.Floor(p*float64(len(s)))))]
}

func minOf(xs []int64) int64 {
	m := int64(math.MaxInt64)
	for _, x := range xs {
		m = min(m, x)
	}
	return m
}

func abs64(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

// segGrace 是 6.37 三的宽限公式（事先写死）：G ＝ 2 × ceil_sec( max(0, E_loc) ＋ max(0, 最大 D) ＋ |最小 D| )。
func segGrace(eLoc int64, d []int64) int64 {
	return 2 * ceilSec(max(0, eLoc)+max(0, maxOf(d))+abs64(minOf(d)))
}

// tradingDayGuess 按「18:00 之后的根属于下一个工作日」给出记录期间第一根的交易日 —— **推论**：
// 没查日历（本工具不 import 本库），长假前后会错；读数里照实标「推论」。
func tradingDayGuess(firstOpenMs int64) string {
	t := time.UnixMilli(firstOpenMs).In(cst)
	d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, cst)
	if t.Hour() >= 18 {
		d = d.AddDate(0, 0, 1)
	}
	for d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
		d = d.AddDate(0, 0, 1)
	}
	return d.Format("2006-01-02") + "（推论：18:00 之后属下一个工作日，没查日历）"
}

func maxOf(xs []int64) int64 {
	m := int64(math.MinInt64)
	for _, x := range xs {
		m = max(m, x)
	}
	return m
}

func ceilSec(ms int64) int64 { return (ms + 999) / 1000 }

// calibrate 造一帧：id 那根在「收盘 ＋ 5 秒」又改了一次（收盘价改成 1e9）—— 必须让 A1、A2、A1s 各多报一根。
// 造出来的值是持续的（见 persist）：之后记录里凡带这根 close 的帧，close 都是 1e9。
func calibrate(frames []tailFrame, id, closeMs int64) []tailFrame {
	durKey := strconv.FormatInt(tailDur, 10)
	srv := time.UnixMilli(closeMs + 5000).In(cst).Format("2006-01-02 15:04:05.000000")
	patch := map[string]any{"aid": "rtn_data", "data": []any{map[string]any{
		"quotes": map[string]any{tailSym: map[string]any{"datetime": srv}},
		"klines": map[string]any{tailSym: map[string]any{durKey: map[string]any{
			"data": map[string]any{strconv.FormatInt(id, 10): map[string]any{"close": 1e9}}}}}}}}
	raw, _ := json.Marshal(patch)
	at := closeMs + 5000
	return insertFrame(persist(frames, id, at, 1e9), tailFrame{RecvMs: at, Raw: raw})
}

// persist 让造出来的值「持续」（2026-09-21 评审方裁「乙」）：at 之后，记录里凡带 id 那根 close 的帧，close 一律改成 v；
// null 帧照旧（离窗）。这才像服务器真改了值 —— 之后的重发带的是新值。
// 不这样做，真实数据里的「整根原值重发」（6.37 读数：11:30 那根在 13:29:59.850 被整根重发）会把造出来的值冲回原值，
// 分析器照实把这次「改回来」数成第二次改动，标定就红在构造上。
func persist(frames []tailFrame, id, at int64, v float64) []tailFrame {
	durKey := strconv.FormatInt(tailDur, 10)
	key := strconv.FormatInt(id, 10)
	out := append([]tailFrame(nil), frames...)
	for i := range out {
		if out[i].RecvMs <= at {
			continue
		}
		var m map[string]any
		if json.Unmarshal(out[i].Raw, &m) != nil {
			continue
		}
		hit := false
		data, _ := m["data"].([]any)
		for _, d := range data {
			dm, _ := d.(map[string]any)
			if b := obj(dm, "klines", tailSym, durKey, "data", key); b != nil {
				if _, ok := b["close"]; ok {
					b["close"] = v
					hit = true
				}
			}
		}
		if hit {
			out[i].Raw, _ = json.Marshal(m)
		}
	}
	return out
}

// calibTarget 挑 6.36 标定的目标（2026-09-21 评审方裁「甲」）：按 id 升序，第一根满足
//
//	开盘时刻不早于记录的第一帧（录制开头那根只看到了半截，view_width 带来的历史也在这里排除）
//	下一根出现在记录里
//	A1 ≤ 0、A2 ≤ 0、A1s ≤ 0（有服务器时刻时）—— 三个计数各有可加的余地
//
// 旧挑法是「第一根下一根出现过的」：6.37 那天它挑中的正好是本来就 A1 > 0 的那根，造帧后加不上去。
// 这里独立重放一遍（只读 merge / obj / sameVal / num，不经 analyzeTail）：挑错只会让标定红，不会让它假绿 ——
// 判绿的仍是「造帧后三个计数各 ＋1」。
func calibTarget(frames []tailFrame) (id, closeMs int64, ok bool) {
	if len(frames) == 0 {
		return 0, 0, false
	}
	durKey := strconv.FormatInt(tailDur, 10)
	type bar struct {
		v            barVal
		born, open   int64
		lastL, lastS int64
	}
	bars := map[int64]*bar{}
	snap := map[string]any{}
	var srvNow int64
	for _, fr := range frames {
		var m struct {
			Aid  string           `json:"aid"`
			Data []map[string]any `json:"data"`
		}
		if json.Unmarshal(fr.Raw, &m) != nil || m.Aid != "rtn_data" {
			continue
		}
		touched := map[int64]bool{}
		for _, d := range m.Data {
			if kd := obj(d, "klines", tailSym, durKey, "data"); kd != nil {
				for k := range kd {
					if x, err := strconv.ParseInt(k, 10, 64); err == nil {
						touched[x] = true
					}
				}
			}
			merge(snap, d)
		}
		if q := obj(snap, "quotes", tailSym); q != nil {
			if s, ok := q["datetime"].(string); ok {
				if t, err := time.ParseInLocation("2006-01-02 15:04:05.000000", s, cst); err == nil {
					srvNow = t.UnixMilli()
				}
			}
		}
		data := obj(snap, "klines", tailSym, durKey, "data")
		for x := range touched {
			b := obj(data, strconv.FormatInt(x, 10))
			if b == nil {
				continue
			}
			v := barVal{num(b["open"]), num(b["high"]), num(b["low"]), num(b["close"]), num(b["volume"]), num(b["close_oi"]), num(b["open_oi"])}
			s, seen := bars[x]
			if !seen {
				bars[x] = &bar{v: v, born: fr.RecvMs, open: int64(num(b["datetime"])) / 1e6, lastL: fr.RecvMs, lastS: srvNow}
				continue
			}
			if sameVal(v, s.v) {
				continue
			}
			s.v, s.lastL, s.lastS = v, fr.RecvMs, srvNow
		}
	}
	ids := make([]int64, 0, len(bars))
	for x := range bars {
		ids = append(ids, x)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, x := range ids {
		b := bars[x]
		next, has := bars[x+1]
		if !has || b.open < frames[0].RecvMs {
			continue
		}
		c := b.open + 60000
		if b.lastL-c > 0 || b.lastL-next.born > 0 || (b.lastS != 0 && b.lastS-c > 0) {
			continue
		}
		return x, c, true
	}
	return 0, 0, false
}

// calib636 做 6.36 的标定：挑目标（calibTarget）→ 造帧（calibrate）→ A1、A2、A1s 必须各多报一根。
func calib636(frames []tailFrame, base tailResult) (found, ok bool, why string) {
	id, closeMs, found := calibTarget(frames)
	if !found {
		return false, false, "标定目标找不到（没有一根：开盘不早于第一帧 · 下一根出现过 · A1 ≤ 0 · A2 ≤ 0 · A1s ≤ 0）⇒ 标定作废"
	}
	c := analyzeTail(calibrate(frames, id, closeMs))
	// 真改动恰好 ＋1：造的是一次改动。三个计数是「每根一个布尔」，之后的原值重发把值冲回去时它们照样 ＋1，
	// 只有这一条看得见（persist 漏了 ⇒ ＋2）。
	ok = c.a1Pos == base.a1Pos+1 && c.a2Pos == base.a2Pos+1 && c.a1sPos == base.a1sPos+1 && c.changes == base.changes+1
	why = fmt.Sprintf("目标 id %d（收盘 %s）· 基线 A1>0 %d · A2>0 %d · A1s>0 %d · 真改动 %d ⇒ 造帧后 %d · %d · %d · %d（必须各 ＋1）",
		id, showMs(closeMs), base.a1Pos, base.a2Pos, base.a1sPos, base.changes, c.a1Pos, c.a2Pos, c.a1sPos, c.changes)
	return true, ok, why
}

// calibrate637 做 6.37 的两格标定：
//
//	一  第 2 处时段末根在 C ＋ 3 秒又改一次（带 quote.datetime ＝ C ＋ 3 秒）⇒ 它的 post 必须 0 → 1、E_loc 必须变大
//	二  一帧旧报价（quote.datetime 比第一根开盘早一小时）⇒ 不许进 D（D 的条数不变、被排除的旧报价 ＋1）
func calibrate637(frames []tailFrame, base tailResult) (bool, string) {
	durKey := strconv.FormatInt(tailDur, 10)
	sg := base.segs[1]
	at := sg.c + 3000
	patch := map[string]any{"aid": "rtn_data", "data": []any{map[string]any{
		"quotes": map[string]any{tailSym: map[string]any{"datetime": time.UnixMilli(at).In(cst).Format("2006-01-02 15:04:05.000000")}},
		"klines": map[string]any{tailSym: map[string]any{durKey: map[string]any{
			"data": map[string]any{strconv.FormatInt(sg.id, 10): map[string]any{"close": 1e9}}}}}}}}
	raw, _ := json.Marshal(patch)
	fr := insertFrame(persist(frames, sg.id, at, 1e9), tailFrame{RecvMs: at, Raw: raw})
	c1 := analyzeTail(fr)
	var got segEnd
	for _, x := range c1.segs {
		if x.id == sg.id {
			got = x
		}
	}
	ok1 := got.post == sg.post+1 && got.eLoc > sg.eLoc
	// 二：旧报价（放在记录中段，本机收到时刻取中位帧）
	mid := frames[len(frames)/2].RecvMs + 1
	stale := time.UnixMilli(frames[0].RecvMs - 3600000).In(cst).Format("2006-01-02 15:04:05.000000")
	raw2, _ := json.Marshal(map[string]any{"aid": "rtn_data", "data": []any{map[string]any{
		"quotes": map[string]any{tailSym: map[string]any{"datetime": stale}}}}})
	c2 := analyzeTail(insertFrame(frames, tailFrame{RecvMs: mid, Raw: raw2}))
	ok2 := len(c2.d) == len(base.d) && c2.dStale == base.dStale+1
	why := fmt.Sprintf("第 2 处末根（%s）造帧后 post %d→%d、E_loc %d→%d；旧报价造帧后 D %d→%d 条、排除 %d→%d",
		sg.label, sg.post, got.post, sg.eLoc, got.eLoc, len(base.d), len(c2.d), base.dStale, c2.dStale)
	return ok1 && ok2, why
}

// insertFrame 按本机收到时刻插入一帧（保持升序）。
func insertFrame(frames []tailFrame, x tailFrame) []tailFrame {
	out := append([]tailFrame(nil), frames...)
	i := sort.Search(len(out), func(i int) bool { return out[i].RecvMs > x.RecvMs })
	return append(out[:i], append([]tailFrame{x}, out[i:]...)...)
}

func readFrames(path string) ([]tailFrame, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []tailFrame
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 256<<20)
	for sc.Scan() {
		var fr tailFrame
		if err := json.Unmarshal(sc.Bytes(), &fr); err != nil {
			return nil, err
		}
		out = append(out, fr)
	}
	return out, sc.Err()
}

func probeLiveAnalyze() {
	const name = "shinny-live-analyze"
	if !optIn(name) {
		return
	}
	frames, err := readFrames(*tailIn)
	if err != nil {
		report(name, "FAIL", err.Error())
		return
	}
	r := analyzeTail(frames)
	if *tailCalib {
		found, ok, why := calib636(frames, r)
		if !found {
			report(name, "FAIL", why)
			return
		}
		st := "PASS"
		if !ok {
			st = "FAIL"
		}
		report(name+"-calib", st, why)
		if len(r.segs) >= 2 {
			ok637, why := calibrate637(frames, r)
			st := "PASS"
			if !ok637 {
				st = "FAIL"
			}
			report(name+"-calib-637", st, why)
		} else {
			fmt.Printf("（6.37 的标定要至少两处时段末根，这份记录里只有 %d 处 ⇒ 跳过）\n", len(r.segs))
		}
		return
	}
	fmt.Printf("帧 %d（rtn_data %d）· 首帧 %s · 末帧 %s\n", r.frames, r.rtn, showMs(frames[0].RecvMs), showMs(frames[len(frames)-1].RecvMs))
	fmt.Printf("KQ.m 映射到 %q · trading_day_end_id 依次 %v\n", r.underlying, r.endIDs)
	fmt.Printf("记录期间新出现的根 %d · 真改动 %d 次 · 重发未变 %d 次\n", r.bars, r.changes, r.resends)
	fmt.Printf("帧间隔     %s\n", dist(r.frameGap))
	fmt.Printf("A1 L−C     %s · L−C>0 的根 %d\n", dist(r.a1), r.a1Pos)
	fmt.Printf("A2 L−N     %s · L−N>0 的根 %d\n", dist(r.a2), r.a2Pos)
	fmt.Printf("A3 末根 L−C %s\n", dist(r.a3))
	fmt.Printf("B1 相邻两次真改动的最大间隔 %d 毫秒（不分时段）· 交易时段内（按服务器时刻）%d 毫秒\n", r.b1, r.b1s)
	fmt.Printf("A1s Ls−C（服务器时刻）%s · Ls−C>0 的根 %d · 真改动时还没有服务器时刻 %d 次\n", dist(r.a1s), r.a1sPos, r.noSrv)
	fmt.Printf("A3s 末根 Ls−C（服务器时刻）%s\n", dist(r.a3s))
	fmt.Printf("B2 本机收到 − 服务器行情时刻（全部帧，含旧报价）%s\n", dist(r.b2))
	fmt.Printf("D  本机收到 − quote.datetime（本帧带 quote、且不早于第一根开盘）%s · 99 分位 %d · 排除的旧报价帧 %d\n", dist(r.d), pct(r.d, 0.99), r.dStale)
	fmt.Printf("TradingDay %s\n", r.tdNote)
	for i, sg := range r.segs {
		fmt.Printf("末根 %d  C %s · id %d · E_loc %d · E_srv %d · C 之后还改 %d 次（按 quote）/ %d 次（按本机）· 下一根出现 %v · G ＝ %d 秒\n",
			i+1, showMs(sg.c), sg.id, sg.eLoc, sg.eSrv, sg.post, sg.postLoc, sg.hasNext, segGrace(sg.eLoc, r.d))
	}
	// 判别力
	if r.bars < 30 {
		report(name, "FAIL", fmt.Sprintf("判别力不在场：记录期间新出现的根 %d < 30 ⇒ 整次作废", r.bars))
		return
	}
	if r.a1sPos == 0 {
		fmt.Println("⚠️ 没见过「收盘之后才到」的改动（射程：一个时段、一个品种、一天）")
	}
	// 判对（6.36 三，事先写死）
	if r.a2Pos == 0 {
		fmt.Println("判对 甲：A2 里「下一根出现后仍被改」0 根 ⇒ 判据一（下一根已出现 ⇒ 上一根不再变）在这次取数里成立")
	} else {
		fmt.Printf("判对 甲：A2 里「下一根出现后仍被改」%d 根 ⇒ 判据一不成立；判据二容差 ＝ ceil(A1s 最大 %d ms) ×2 ＝ %d 秒（服务器时刻，用户裁）\n", r.a2Pos, maxOf(r.a1s), 2*ceilSec(max(0, maxOf(r.a1s))))
	}
	if len(r.a3s) > 0 {
		fmt.Printf("判对 末根宽限 ＝ ceil(A3s 最大 %d ms) ×2 ＝ %d 秒（服务器时刻；负数取 0）\n", maxOf(r.a3s), 2*ceilSec(max(0, maxOf(r.a3s))))
	}
	fmt.Printf("判对 乙 N ＝ ceil(时段内 B1 %d ms 折分钟) ×2 ＝ %d 分钟\n", r.b1s, 2*int64(math.Ceil(float64(r.b1s)/60000)))
	// 6.37 的判别力（只在这份记录里出现了时段末根时才印）
	if len(r.segs) > 0 {
		if miss := power637(r); len(miss) > 0 {
			fmt.Printf("⛔ 6.37 判别力不在场：%s ⇒ 这次的 G 作废\n", strings.Join(miss, " · "))
		} else {
			fmt.Printf("6.37 判别力在场：三处末根都在 · 前两处的下一根都出现 · 判据一复核的根 %d ≥ %d · 进 D 的帧 ≥ 1000\n", len(r.a2), minA2For637)
		}
	}
	report(name, "PASS", "读数见上")
}

// minA2For637 是判据一复核那一格要的样本：「下一根出现在记录里」的根（即 A2 的条数）。
//
// 它替掉了原先的「根数 ≥ 300」（2026-09-21 取数期间、分析之前改，评审方裁；封存 md5 见 probe.md 6.37）：
// rb 日盘三段共 225 分钟，r.bars 又不含首帧那批历史 ⇒ 日盘一根不漏也只有 225 根，300 在授权窗口里恒不可满足。
// 这是算术，与当天的数据无关。100 按算术定（给 A2 一个与 6.36 夜盘同量级的样本），不是按当天的读数。
const minA2For637 = 100

// power637 列出 6.37 判别力缺的项（空 ⇒ 在场）：三处末根都在 · 前两处的下一根都出现 · A2 的根 ≥ minA2For637 · 进 D 的帧 ≥ 1000。
func power637(r tailResult) []string {
	miss := []string{}
	for _, want := range []string{"10:15", "11:30", "15:00"} {
		found := false
		for _, sg := range r.segs {
			if sg.label == want {
				found = true
				if want != "15:00" && !sg.hasNext {
					miss = append(miss, want+" 的下一根没出现")
				}
			}
		}
		if !found {
			miss = append(miss, want+" 那根不在记录里")
		}
	}
	if len(r.a2) < minA2For637 {
		miss = append(miss, fmt.Sprintf("判据一复核的根（下一根出现在记录里）%d < %d", len(r.a2), minA2For637))
	}
	if len(r.d) < 1000 {
		miss = append(miss, fmt.Sprintf("进 D 的帧 %d < 1000", len(r.d)))
	}
	return miss
}
