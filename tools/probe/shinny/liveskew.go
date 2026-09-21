package main

// v0.10 P-b0：本机偏差告警（design.md L6）用什么统计量 —— probe.md 6.38 的离线回放（判对 ec724e9 冻结）。
//
//	go run . -only shinny-live-skew -tail-in <9/21 文件> -skew-other <9/18 文件>
//
// 只读两份落盘文件，不联网、不要凭证。
//
//	样本  D ＝ 本机收到 − quote.datetime；只取本帧带 quote.datetime 的帧，且 quote.datetime 不早于【当前段段首】
//	      （当前段按本机收到时刻问 rb 的时段表 rbSegBounds；不在任何段里的帧不进）
//	窗口  本机时刻往回 W 秒、只含当前段的样本（段首一到清空）；样本 < K ⇒ 这一刻不判
//	判    每来一个样本判一次：窗口低端 > Bhi ⇒ 停；< Blo ⇒ 告警
//
// 本文件不 import 本库。

import (
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"sort"
	"time"
)

var skewOther = flag.String("skew-other", "", "shinny-live-skew：另一天的落盘文件（丁：跨天那一格；6.38 用 9/18 夜盘）")

// skewSample 是一个进窗的样本。
type skewSample struct {
	recv int64 // 本机收到（毫秒）
	d    int64 // 本机收到 − quote.datetime（毫秒）
	seg  int64 // 段号（rbSegBounds 的 key）
	s0   int64 // 段首（毫秒）
}

// skewSamples 从帧里取样本（6.38 一「样本」）；shift 把本机收到时刻整体平移（戊：模拟本机偏快 shift 毫秒）。
func skewSamples(frames []tailFrame, shift int64) []skewSample {
	var out []skewSample
	for _, fr := range frames {
		var m struct {
			Aid  string           `json:"aid"`
			Data []map[string]any `json:"data"`
		}
		if json.Unmarshal(fr.Raw, &m) != nil || m.Aid != "rtn_data" {
			continue
		}
		var qdt string
		for _, d := range m.Data {
			if q := obj(d, "quotes", tailSym); q != nil {
				if s, ok := q["datetime"].(string); ok {
					qdt = s
				}
			}
		}
		if qdt == "" {
			continue
		}
		t, err := time.ParseInLocation("2006-01-02 15:04:05.000000", qdt, cst)
		if err != nil {
			continue
		}
		recv := fr.RecvMs + shift
		key, s0, _, ok := rbSegBounds(recv)
		if !ok || t.UnixMilli() < s0 {
			continue // 不在任何段里 · 或上一段遗留的旧报价（quote.datetime 早于段首）
		}
		out = append(out, skewSample{recv: recv, d: recv - t.UnixMilli(), seg: key, s0: s0})
	}
	return out
}

// skewCand 是一个候选：窗口 W 秒 · 统计量（0 ＝ 最小值，其余是分位 p，如 0.05）· 最少样本数 K。
type skewCand struct {
	w int64
	p float64
	k int
}

func (c skewCand) String() string {
	st := "最小值"
	if c.p > 0 {
		st = fmt.Sprintf("%g 分位", c.p*100)
	}
	return fmt.Sprintf("W %3ds · %-7s · K %2d", c.w/1000, st, c.k)
}

// skewCands 是 6.38 一「候选」的全部 24 个。
func skewCands() []skewCand {
	var out []skewCand
	for _, w := range []int64{30, 60, 120, 300} {
		for _, p := range []float64{0, 0.05, 0.10} {
			for _, k := range []int{10, 30} {
				out = append(out, skewCand{w: w * 1000, p: p, k: k})
			}
		}
	}
	return out
}

// skewEval 是一次判定：在第 i 个样本到来时，窗口低端 low（judged 为假 ⇒ 样本不够、不判）。
type skewEval struct {
	recv   int64
	seg    int64
	s0     int64
	low    int64
	judged bool
	gap    int64 // 到同一段里下一次判定的本机间隔（算「不判」时长用；段里最后一个为 0）
}

// skewLows 按候选 c 逐样本算窗口低端。
func skewLows(ss []skewSample, c skewCand) []skewEval {
	out := make([]skewEval, 0, len(ss))
	lo := 0                       // 窗口左端（下标）
	win := make([]int64, 0, 1024) // 窗口里的 D，升序（滑动：进一个、出若干，二分定位）
	for i, s := range ss {
		for lo < i && (ss[lo].seg != s.seg || ss[lo].recv <= s.recv-c.w) {
			j := sort.Search(len(win), func(j int) bool { return win[j] >= ss[lo].d })
			win = append(win[:j], win[j+1:]...)
			lo++
		}
		j := sort.Search(len(win), func(j int) bool { return win[j] >= s.d })
		win = append(win, 0)
		copy(win[j+1:], win[j:])
		win[j] = s.d
		e := skewEval{recv: s.recv, seg: s.seg, s0: s.s0}
		if len(win) >= c.k {
			e.low, e.judged = lowSorted(win, c.p), true
		}
		out = append(out, e)
	}
	for i := range out {
		if i+1 < len(out) && out[i+1].seg == out[i].seg {
			out[i].gap = out[i+1].recv - out[i].recv
		}
	}
	return out
}

// lowOf：p＝0 ⇒ 最小值；否则最近秩分位（排序后第 ceil(p·n) 个，至少第 1 个）。会改 xs 的顺序。
func lowOf(xs []int64, p float64) int64 {
	sort.Slice(xs, func(i, j int) bool { return xs[i] < xs[j] })
	return lowSorted(xs, p)
}

// lowSorted 同 lowOf，xs 已升序。
func lowSorted(xs []int64, p float64) int64 {
	if p == 0 {
		return xs[0]
	}
	r := int(math.Ceil(p * float64(len(xs))))
	return xs[max(r, 1)-1]
}

// skewRange 是判过的那些时刻的窗口低端范围。
func skewRange(es []skewEval) (lo, hi int64, n int) {
	lo, hi = math.MaxInt64, math.MinInt64
	for _, e := range es {
		if e.judged {
			lo, hi, n = min(lo, e.low), max(hi, e.low), n+1
		}
	}
	return
}

// skewJudge 拿基线 [blo, bhi] 判 es：停几次（> bhi）· 告警几次（< blo）· 第一次停在第一次判定之后多少毫秒（没停 ⇒ −1）。
func skewJudge(es []skewEval, blo, bhi int64) (stops, warns int, firstStop int64) {
	firstStop = -1
	var t0 int64
	started := false
	for _, e := range es {
		if !e.judged {
			continue
		}
		if !started {
			t0, started = e.recv, true
		}
		switch {
		case e.low > bhi:
			if stops == 0 {
				firstStop = e.recv - t0
			}
			stops++
		case e.low < blo:
			warns++
		}
	}
	return
}

// skewHalf 取某个本机时刻区间 [from, to) 里的判定（丙 / 戊：上午、下午两半）。
func skewHalf(es []skewEval, from, to int64) []skewEval {
	var out []skewEval
	for _, e := range es {
		if e.recv >= from && e.recv < to {
			out = append(out, e)
		}
	}
	return out
}

// skewMedian 判过的窗口低端的中位。
func skewMedian(es []skewEval) int64 {
	var xs []int64
	for _, e := range es {
		if e.judged {
			xs = append(xs, e.low)
		}
	}
	if len(xs) == 0 {
		return 0
	}
	sort.Slice(xs, func(i, j int) bool { return xs[i] < xs[j] })
	return xs[len(xs)/2]
}

// skewUnjudged：不判的累计秒数（按到下一次判定的间隔累加），以及发生不判的段（按段首时刻列出）。
func skewUnjudged(es []skewEval) (sec float64, segs []string) {
	seen := map[int64]bool{}
	for _, e := range es {
		if e.judged {
			continue
		}
		sec += float64(e.gap) / 1000
		if !seen[e.s0] {
			seen[e.s0] = true
			segs = append(segs, time.UnixMilli(e.s0).In(cst).Format("01-02 15:04"))
		}
	}
	return
}

// skewSegOpenMax（乙）：每段段首后前 W 秒里判过的窗口低端的最大值（没有判过的 ⇒ 不列）。
func skewSegOpenMax(es []skewEval, w int64) (hi int64, any bool) {
	hi = math.MinInt64
	for _, e := range es {
		if e.judged && e.recv-e.s0 < w {
			hi, any = max(hi, e.low), true
		}
	}
	return
}

var skewXs = []int64{100, 200, 300, 500, 1000, 2000}

// skewRow 是全表的一行（6.38 二的甲 — 戊）。
type skewRow struct {
	c                                              skewCand
	blo, bhi, med                                  int64
	nJudged                                        int
	unjudgedSec                                    float64
	unjudgedSegs                                   []string
	openHi                                         int64
	openAny                                        bool
	amBase, pmBase                                 [2]int64 // [blo, bhi]
	amToPmStop, amToPmWarn, pmToAmStop, pmToAmWarn int
	otherStop, otherWarn                           int
	minXamToPm, minXpmToAm                         int64 // 0 ＝ 到 2000 都没停
	firstAmToPm, firstPmToAm                       int64 // 那个最小 X 下，第一次停在平移后第几毫秒
	stop1000Both                                   bool
}

// skewAnalyze 算全部 24 行。noon 是上午 / 下午的分界（本机时刻，毫秒）：[−∞, noon) 为上午、[noon, ∞) 为下午。
func skewAnalyze(day, other []tailFrame, noon int64) []skewRow {
	base := skewSamples(day, 0)
	oth := skewSamples(other, 0)
	shifted := map[int64][]skewSample{}
	for _, x := range skewXs {
		shifted[x] = skewSamples(day, x)
	}
	var rows []skewRow
	for _, c := range skewCands() {
		r := skewRow{c: c}
		es := skewLows(base, c)
		r.blo, r.bhi, r.nJudged = skewRange(es)
		r.med = skewMedian(es)
		r.unjudgedSec, r.unjudgedSegs = skewUnjudged(es)
		r.openHi, r.openAny = skewSegOpenMax(es, c.w)
		am, pm := skewHalf(es, math.MinInt64, noon), skewHalf(es, noon, math.MaxInt64)
		r.amBase[0], r.amBase[1], _ = skewRange(am)
		r.pmBase[0], r.pmBase[1], _ = skewRange(pm)
		r.amToPmStop, r.amToPmWarn, _ = skewJudge(pm, r.amBase[0], r.amBase[1])
		r.pmToAmStop, r.pmToAmWarn, _ = skewJudge(am, r.pmBase[0], r.pmBase[1])
		r.otherStop, r.otherWarn, _ = skewJudge(skewLows(oth, c), r.blo, r.bhi)
		for _, x := range skewXs {
			sh := skewLows(shifted[x], c)
			// 平移后仍按「本机时刻」切上午 / 下午：分界跟着平移 X，保证切的是同一批帧
			s1, _, f1 := skewJudge(skewHalf(sh, noon+x, math.MaxInt64), r.amBase[0], r.amBase[1])
			s2, _, f2 := skewJudge(skewHalf(sh, math.MinInt64, noon+x), r.pmBase[0], r.pmBase[1])
			if s1 > 0 && r.minXamToPm == 0 {
				r.minXamToPm, r.firstAmToPm = x, f1
			}
			if s2 > 0 && r.minXpmToAm == 0 {
				r.minXpmToAm, r.firstPmToAm = x, f2
			}
			if x == 1000 {
				r.stop1000Both = s1 > 0 && s2 > 0
			}
		}
		rows = append(rows, r)
	}
	return rows
}

// qualifies：6.38 三「合格」—— 丙两向 0 停 ∧ 丁 0 停 ∧ 戊两向在 X＝1000 都停。
func (r skewRow) qualifies() bool {
	return r.amToPmStop == 0 && r.pmToAmStop == 0 && r.otherStop == 0 && r.stop1000Both
}

// minX：交叉之后的最小可检 X ＝ 两方向里较大的那个；任一方向到 2000 都没停 ⇒ 0（查不出）。
func (r skewRow) minX() int64 {
	if r.minXamToPm == 0 || r.minXpmToAm == 0 {
		return 0
	}
	return max(r.minXamToPm, r.minXpmToAm)
}

// skewPick：6.38 三「选」—— 合格的里取 minX 最小；并列 ⇒ W 小；再并列 ⇒ K 大。都不合格 ⇒ −1。
func skewPick(rows []skewRow) int {
	best := -1
	for i, r := range rows {
		if !r.qualifies() {
			continue
		}
		if best < 0 {
			best = i
			continue
		}
		b := rows[best]
		switch {
		case r.minX() != b.minX():
			if r.minX() < b.minX() {
				best = i
			}
		case r.c.w != b.c.w:
			if r.c.w < b.c.w {
				best = i
			}
		case r.c.k > b.c.k:
			best = i
		}
	}
	return best
}

func probeLiveSkew() {
	const name = "shinny-live-skew"
	if !optIn(name) {
		return
	}
	if *tailIn == "" || *skewOther == "" {
		report(name, "FAIL", "要给 -tail-in（9/21 日盘）与 -skew-other（9/18 夜盘）")
		return
	}
	day, err := readFrames(*tailIn)
	if err != nil {
		report(name, "FAIL", err.Error())
		return
	}
	other, err := readFrames(*skewOther)
	if err != nil {
		report(name, "FAIL", err.Error())
		return
	}
	if len(day) == 0 {
		report(name, "FAIL", "-tail-in 一帧都没有")
		return
	}
	// 上午 / 下午的分界：记录那天的 12:00（本机时刻）—— 午休中间，两半各是完整的段
	d0 := time.UnixMilli(day[0].RecvMs).In(cst)
	noon := time.Date(d0.Year(), d0.Month(), d0.Day(), 12, 0, 0, 0, cst).UnixMilli()
	rows := skewAnalyze(day, other, noon)
	fmt.Printf("样本  %s 那份 %d 个 · 另一份 %d 个（本帧带 quote、不早于当前段段首）· 上午 / 下午分界 %s\n",
		d0.Format("2006-01-02"), len(skewSamples(day, 0)), len(skewSamples(other, 0)), showMs(noon))
	fmt.Println("全表（24 个候选，6.38 二：甲 全天范围 · 乙 段首 · 丙 半天交叉 · 丁 跨天 · 戊 交叉标定；毫秒）")
	for _, r := range rows {
		open := "—"
		if r.openAny {
			open = fmt.Sprint(r.openHi)
		}
		mx := func(x, f int64) string {
			if x == 0 {
				return "到 2000 未停"
			}
			return fmt.Sprintf("%d（第 %.0f 秒）", x, float64(f)/1000)
		}
		fmt.Printf("%s | 甲 [%d, %d] 中位 %d · 判 %d 次 · 不判 %.0f 秒 %v | 乙 段首最大 %s | 丙 上→下 停 %d 告警 %d · 下→上 停 %d 告警 %d | 丁 停 %d 告警 %d | 戊 上→下 %s · 下→上 %s · 最小可检 %s | %s\n",
			r.c, r.blo, r.bhi, r.med, r.nJudged, r.unjudgedSec, r.unjudgedSegs, open,
			r.amToPmStop, r.amToPmWarn, r.pmToAmStop, r.pmToAmWarn, r.otherStop, r.otherWarn,
			mx(r.minXamToPm, r.firstAmToPm), mx(r.minXpmToAm, r.firstPmToAm), map[bool]string{true: fmt.Sprint(r.minX()), false: "—"}[r.minX() > 0],
			map[bool]string{true: "合格", false: "不合格"}[r.qualifies()])
	}
	i := skewPick(rows)
	if i < 0 {
		fmt.Println("选：没有合格的候选 ⇒ 不选，照实报；回到评审方重议 L6（6.38 三）")
		report(name, "PASS", "读数见上（无合格候选）")
		return
	}
	r := rows[i]
	fmt.Printf("选：%s · 最小可检 X %d ms · 运行时基线 [Blo, Bhi] ＝ [%d, %d]（它在这一天全天的范围，不另加余量）\n", r.c, r.minX(), r.blo, r.bhi)
	report(name, "PASS", "读数见上")
}
