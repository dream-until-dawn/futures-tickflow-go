package main

// v0.10 起手 甲 / 乙 的读数（probe.md 6.36；判对先落文，评审方 2026-09-18 16:50:45 +0800 作证）。
//
//	go run . -only shinny-live-tail -tail-min 75 -tail-out <文件>     联网：记一个连续时段的推送帧（到点自己断开）
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
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
)

var (
	tailMin   = flag.Int("tail-min", 75, "shinny-live-tail 记多少分钟（到点自己断开）")
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

func w32tm() string {
	out, err := exec.Command("w32tm", "/query", "/status").CombinedOutput()
	if err != nil {
		return "w32tm 失败：" + err.Error() + " " + strings.TrimSpace(string(out))
	}
	return strings.TrimSpace(string(out))
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
	f, err := os.Create(*tailOut)
	if err != nil {
		report(name, "FAIL", err.Error())
		return
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	defer w.Flush()
	fmt.Printf("开始 %s（本机）· 记 %d 分钟 · %s 1m · 只用行情通道\n", showMs(time.Now().UnixMilli()), *tailMin, tailSym)
	fmt.Printf("w32tm（取数前）：\n%s\n", w32tm())

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*tailMin)*time.Minute)
	defer cancel()
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
	frames := 0
	for {
		if err := send(map[string]any{"aid": "peek_message"}); err != nil {
			break
		}
		_, msg, err := c.Read(ctx)
		if err != nil {
			break // 到点（ctx 超时）或断线；读数照记，断线本身也是乙要看的
		}
		line, _ := json.Marshal(tailFrame{RecvMs: time.Now().UnixMilli(), Raw: msg})
		w.Write(line)
		w.WriteByte('\n')
		frames++
	}
	fmt.Printf("结束 %s（本机）· 落盘 %d 帧 → %s\n", showMs(time.Now().UnixMilli()), frames, *tailOut)
	fmt.Printf("w32tm（取数后）：\n%s\n", w32tm())
	report(name, "PASS", fmt.Sprintf("记了 %d 帧；读数用 -only shinny-live-analyze -tail-in 算", frames))
}

// ── 离线分析 ──

type barVal struct{ o, h, l, c, v, coi, ooi float64 }

type tailResult struct {
	frames, rtn      int
	underlying       string
	bars             int     // 记录期间新出现的根（首帧那批历史不算）
	changes, resends int     // 真改了 · 重发未变
	a1, a2, a3       []int64 // L−C · L−N · 末根 L−C（毫秒）
	a1Pos, a2Pos     int
	frameGap         []int64 // 相邻帧间隔
	b1               int64   // 相邻两次「有真改动」的帧的最大间隔
	b2               []int64 // 本机收到 − 服务器行情时刻
	endIDs           []string
	calibID          int64
	calibOK          bool // 找到了标定目标（id 0 是合法值，不能拿 calibID == 0 当「没找到」）
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
	last := map[int64]barVal{}  // 每根最近一次的值
	lastL := map[int64]int64{}  // 每根最后一次真改的本机时刻
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
		for _, d := range m.Data {
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
				last[id], lastL[id] = v, fr.RecvMs
				changed = true
				continue
			}
			if sameVal(v, last[id]) {
				r.resends++
				continue
			}
			r.changes++
			last[id], lastL[id] = v, fr.RecvMs
			changed = true
		}
		if changed {
			if lastChangeFrame != 0 && fr.RecvMs-lastChangeFrame > r.b1 {
				r.b1 = fr.RecvMs - lastChangeFrame
			}
			lastChangeFrame = fr.RecvMs
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
	for _, id := range ids {
		C := dt[id] + 60000
		L := lastL[id]
		if n, ok := born[id+1]; ok {
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

func maxOf(xs []int64) int64 {
	m := int64(math.MinInt64)
	for _, x := range xs {
		m = max(m, x)
	}
	return m
}

func ceilSec(ms int64) int64 { return (ms + 999) / 1000 }

// calibrate 造一帧：calibID 那根在「收盘 ＋ 5 秒」又改了一次（收盘价 ＋1）—— 必须让 A1、A2 各多报一根。
func calibrate(frames []tailFrame, id, closeMs int64) []tailFrame {
	durKey := strconv.FormatInt(tailDur, 10)
	patch := map[string]any{"aid": "rtn_data", "data": []any{map[string]any{"klines": map[string]any{tailSym: map[string]any{durKey: map[string]any{
		"data": map[string]any{strconv.FormatInt(id, 10): map[string]any{"close": 1e9}}}}}}}}
	raw, _ := json.Marshal(patch)
	at := closeMs + 5000
	out := append([]tailFrame(nil), frames...)
	i := sort.Search(len(out), func(i int) bool { return out[i].RecvMs > at })
	out = append(out[:i], append([]tailFrame{{RecvMs: at, Raw: raw}}, out[i:]...)...)
	return out
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
		if !r.calibOK {
			report(name, "FAIL", "标定目标找不到（没有一根的下一根出现过）⇒ 标定作废")
			return
		}
		// 找目标那根的收盘时刻：重放一次拿 datetime
		var closeMs int64
		for _, fr := range frames {
			var m struct {
				Data []map[string]any `json:"data"`
			}
			json.Unmarshal(fr.Raw, &m)
			for _, d := range m.Data {
				if b := obj(d, "klines", tailSym, strconv.FormatInt(tailDur, 10), "data", strconv.FormatInt(r.calibID, 10)); b != nil {
					if v, ok := b["datetime"].(float64); ok {
						closeMs = int64(v)/1e6 + 60000
					}
				}
			}
		}
		c := analyzeTail(calibrate(frames, r.calibID, closeMs))
		fmt.Printf("标定 目标 id %d（收盘 %s）· 基线 A1>0 %d · A2>0 %d ⇒ 造帧后 A1>0 %d · A2>0 %d\n",
			r.calibID, showMs(closeMs), r.a1Pos, r.a2Pos, c.a1Pos, c.a2Pos)
		st := "PASS"
		if !(c.a1Pos == r.a1Pos+1 && c.a2Pos == r.a2Pos+1) {
			st = "FAIL"
		}
		report(name+"-calib", st, "A1 / A2 必须各多报一根")
		return
	}
	fmt.Printf("帧 %d（rtn_data %d）· 首帧 %s · 末帧 %s\n", r.frames, r.rtn, showMs(frames[0].RecvMs), showMs(frames[len(frames)-1].RecvMs))
	fmt.Printf("KQ.m 映射到 %q · trading_day_end_id 依次 %v\n", r.underlying, r.endIDs)
	fmt.Printf("记录期间新出现的根 %d · 真改动 %d 次 · 重发未变 %d 次\n", r.bars, r.changes, r.resends)
	fmt.Printf("帧间隔     %s\n", dist(r.frameGap))
	fmt.Printf("A1 L−C     %s · L−C>0 的根 %d\n", dist(r.a1), r.a1Pos)
	fmt.Printf("A2 L−N     %s · L−N>0 的根 %d\n", dist(r.a2), r.a2Pos)
	fmt.Printf("A3 末根 L−C %s\n", dist(r.a3))
	fmt.Printf("B1 相邻两次真改动的最大间隔 %d 毫秒\n", r.b1)
	fmt.Printf("B2 本机收到 − 服务器行情时刻 %s\n", dist(r.b2))
	// 判别力
	if r.bars < 30 {
		report(name, "FAIL", fmt.Sprintf("判别力不在场：记录期间新出现的根 %d < 30 ⇒ 整次作废", r.bars))
		return
	}
	if r.a1Pos == 0 {
		fmt.Println("⚠️ 没见过「收盘之后才到」的改动（射程：一个时段、一个品种、一天）")
	}
	// 判对（6.36 三，事先写死）
	if r.a2Pos == 0 {
		fmt.Println("判对 甲：A2 里「下一根出现后仍被改」0 根 ⇒ 判据一（下一根已出现 ⇒ 上一根不再变）在这次取数里成立")
	} else {
		fmt.Printf("判对 甲：A2 里「下一根出现后仍被改」%d 根 ⇒ 判据一不成立；判据二容差 ＝ ceil(A1 最大 %d ms) ×2 ＝ %d 秒\n", r.a2Pos, maxOf(r.a1), 2*ceilSec(maxOf(r.a1)))
	}
	if len(r.a3) > 0 {
		fmt.Printf("判对 末根宽限 ＝ ceil(A3 最大 %d ms) ×2 ＝ %d 秒\n", maxOf(r.a3), 2*ceilSec(maxOf(r.a3)))
	}
	fmt.Printf("判对 乙 N ＝ ceil(B1 %d ms 折分钟) ×2 ＝ %d 分钟\n", r.b1, 2*int64(math.Ceil(float64(r.b1)/60000)))
	report(name, "PASS", "读数见上")
}
