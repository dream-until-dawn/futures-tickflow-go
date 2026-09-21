package main

// v0.10 L6 重议之后（评审方 2026-09-21 裁「甲：直接守 G 的前提」）—— probe.md 6.38 六的离线验证（只验证、不再选）。
//
//	go run . -only shinny-live-guard -tail-in <9/21 文件> -skew-other <9/18 文件>
//
// 只读两份落盘文件，不联网、不要凭证。样本与窗口同 6.38（skewSamples；窗口只含当前段）。
//
// 本文件不 import 本库。

import (
	"fmt"
	"sort"
)

// 参数由评审方直接定（design.md L6 📌）：不从读数里挑，也不许事后按读数调。
const (
	guardG    = int64(4000)  // 末根宽限 G（probe.md 6.37：三处末根同值 4 秒）
	guardKLag = int64(200)   // 投递延迟余量：K 线帧比报价帧晚（6.37 kLag 99.9 分位 173 ms ⇒ 取 200）
	guardW    = int64(60000) // 窗口 60 秒，只含当前段
	guardK    = 30           // 样本 < 30 ⇒ 不判
	guardHiP  = 0.99         // 高端：99 分位
	guardLoP  = 0.01         // 偏慢告警看 1 分位
)

// guardEval 是一次判定：高端 ＝ 99 分位 ＋ guardKLag；低端 ＝ 1 分位。
type guardEval struct {
	recv, s0 int64
	hi, lo   int64
	judged   bool
	gap      int64
}

// guardEvals 逐样本滑窗（与 skewLows 同一种窗口：本机时刻往回 guardW、只含当前段、升序数组二分进出）。
func guardEvals(ss []skewSample) []guardEval {
	out := make([]guardEval, 0, len(ss))
	lo := 0
	win := make([]int64, 0, 1024)
	for i, s := range ss {
		for lo < i && (ss[lo].seg != s.seg || ss[lo].recv <= s.recv-guardW) {
			j := sort.Search(len(win), func(j int) bool { return win[j] >= ss[lo].d })
			win = append(win[:j], win[j+1:]...)
			lo++
		}
		j := sort.Search(len(win), func(j int) bool { return win[j] >= s.d })
		win = append(win, 0)
		copy(win[j+1:], win[j:])
		win[j] = s.d
		e := guardEval{recv: s.recv, s0: s.s0}
		if len(win) >= guardK {
			e.hi, e.lo, e.judged = lowSorted(win, guardHiP)+guardKLag, lowSorted(win, guardLoP), true
		}
		out = append(out, e)
	}
	for i := range out {
		if i+1 < len(out) && out[i+1].s0 == out[i].s0 {
			out[i].gap = out[i+1].recv - out[i].recv
		}
	}
	return out
}

// guardSummary 是一天的读数。
type guardSummary struct {
	stops, warns int
	maxHi, minLo int64
	judged       int
	unjudgedSec  float64
	firstStop    int64 // 第一次停距第一次判定的毫秒数；没停 ⇒ −1
}

// guardJudge：高端 > G/2 ⇒ 停；1 分位 < −G/2 ⇒ 告警（不停）。两者同一刻都成立时两样都计（停优先生效，告警照记）。
func guardJudge(es []guardEval) guardSummary {
	g := guardSummary{maxHi: -1 << 62, minLo: 1 << 62, firstStop: -1}
	var t0 int64
	for _, e := range es {
		if !e.judged {
			g.unjudgedSec += float64(e.gap) / 1000
			continue
		}
		if g.judged == 0 {
			t0 = e.recv
		}
		g.judged++
		g.maxHi, g.minLo = max(g.maxHi, e.hi), min(g.minLo, e.lo)
		if e.hi > guardG/2 {
			if g.stops == 0 {
				g.firstStop = e.recv - t0
			}
			g.stops++
		}
		if e.lo < -guardG/2 {
			g.warns++
		}
	}
	return g
}

// guardCalib：本机时刻整体平移 ＋X（100 起、每 100、到 8000），第一次出现停的 X 与那次停在第几毫秒；到 8000 都不停 ⇒ 0。
func guardCalib(frames []tailFrame) (int64, int64) {
	for x := int64(100); x <= 8000; x += 100 {
		if g := guardJudge(guardEvals(skewSamples(frames, x))); g.stops > 0 {
			return x, g.firstStop
		}
	}
	return 0, -1
}

func probeLiveGuard() {
	const name = "shinny-live-guard"
	if !optIn(name) {
		return
	}
	if *tailIn == "" || *skewOther == "" {
		report(name, "FAIL", "要给 -tail-in（9/21 日盘）与 -skew-other（9/18 夜盘）")
		return
	}
	fmt.Printf("参数（评审方定）G %d · 余量 %d · 窗口 %ds · K %d · 高端 %g 分位 ＋ 余量 > G/2 ⇒ 停 · %g 分位 < −G/2 ⇒ 告警\n",
		guardG, guardKLag, guardW/1000, guardK, guardHiP*100, guardLoP*100)
	calibOK := false
	for i, path := range []string{*tailIn, *skewOther} {
		frames, err := readFrames(path)
		if err != nil {
			report(name, "FAIL", err.Error())
			return
		}
		ss := skewSamples(frames, 0)
		g := guardJudge(guardEvals(ss))
		x, first := guardCalib(frames)
		label := []string{"-tail-in", "-skew-other"}[i]
		cal := "到 8000 未停"
		if x > 0 {
			cal = fmt.Sprintf("%d（第 %.0f 秒）", x, float64(first)/1000)
		}
		fmt.Printf("%s  样本 %d · 判 %d 次 · 不判 %.0f 秒 · 停 %d · 告警 %d · 高端全天最大 %d · 1 分位全天最小 %d · 标定第一次停的 X %s\n",
			label, len(ss), g.judged, g.unjudgedSec, g.stops, g.warns, g.maxHi, g.minLo, cal)
		if i == 0 {
			calibOK = x > 0 && x <= 2000
		}
	}
	if !calibOK {
		fmt.Println("⛔ 判据：-tail-in 那份在 X ≤ 2000 时没有停 ⇒ 高端取法压得太低，回到评审方（probe.md 6.38 六）")
		report(name, "PASS", "读数见上（判据不过）")
		return
	}
	fmt.Println("判据：-tail-in 那份在 X ≤ 2000 时能停（probe.md 6.38 六）")
	report(name, "PASS", "读数见上")
}
