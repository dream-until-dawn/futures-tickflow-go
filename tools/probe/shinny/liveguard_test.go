package main

import "testing"

// v0.10 L6 重议之后（probe.md 6.38 六）守 G 的前提的合成测试 —— 不读封存文件。

// guardSeg 在 13:30 那一段里、从 13:40 起造 n 个样本（每 0.5 秒一个），D 由 d(i) 给。
// 从 13:40 起而不是段首：D 为正几秒时 quote.datetime 早于本机时刻，贴着段首造会被当成旧报价排除（6.38 的样本口径）——
// 第一版就贴着段首造，「样本 < 30 不判」那格当场红在这上面（测试的构造错）。
func guardSeg(n int, d func(i int) int64) []tailFrame {
	var fr []tailFrame
	for i := 0; i < n; i++ {
		r := ms(13, 40, 0) + int64(i)*500
		fr = append(fr, skewFrame(r, r-d(i)))
	}
	return fr
}

// guard: 单帧尖峰不停 —— 120 个样本（60 秒）D＝0，其中一个 ＋5000：99 分位取到的是第二大的那个（0）⇒ 高端 200，不停。
// 评审方要求：「99 分位换成最大值」这条突变由这一格红。
func TestGuardSingleSpikeDoesNotStop(t *testing.T) {
	g := guardJudge(guardEvals(skewSamples(guardSeg(120, func(i int) int64 {
		if i == 100 {
			return 5000
		}
		return 0
	}), 0)))
	if g.stops != 0 {
		t.Errorf("单帧尖峰：停 %d 次、高端最大 %d，应不停", g.stops, g.maxHi)
	}
	if g.maxHi != guardKLag {
		t.Errorf("高端最大 %d，应为 0 ＋ 余量 %d", g.maxHi, guardKLag)
	}
}

// guard: 样本刚够 K（n＝30）时单帧尖峰也不停 —— 30 个样本 D＝0、其中一个 ＋5000：高端取第 min(ceil(0.99·30), 30−1)＝29 小的（0）⇒ 不停。
// ca41201 的「99 分位」在 n ≤ 100 时就是最大值（ceil(0.99·n)＝n），这一格在那里红（评审方 09-21 修正，6.38 六的读数暴露）。
func TestGuardSingleSpikeAtThirtyDoesNotStop(t *testing.T) {
	g := guardJudge(guardEvals(skewSamples(guardSeg(30, func(i int) int64 {
		if i == 5 {
			return 5000
		}
		return 0
	}), 0)))
	if g.judged != 1 || g.stops != 0 || g.maxHi != guardKLag {
		t.Errorf("n＝30、单帧 ＋5000：判 %d 次 · 停 %d 次 · 高端 %d，应判 1 次、不停、高端 0 ＋ %d", g.judged, g.stops, g.maxHi, guardKLag)
	}
}

// guard: 低端对称 —— n＝30、D＝0、其中一个 −5000：低端取第 max(ceil(0.01·30), 2)＝2 小的（0）⇒ 不告警。
func TestGuardSingleDipAtThirtyDoesNotWarn(t *testing.T) {
	g := guardJudge(guardEvals(skewSamples(guardSeg(30, func(i int) int64 {
		if i == 5 {
			return -5000
		}
		return 0
	}), 0)))
	if g.judged != 1 || g.warns != 0 || g.minLo != 0 {
		t.Errorf("n＝30、单帧 −5000：判 %d 次 · 告警 %d 次 · 低端 %d，应判 1 次、不告警、低端 0", g.judged, g.warns, g.minLo)
	}
}

// guard: 持续的高端要停 —— D 恒为 1900：高端 ＝ 1900 ＋ 200 ＝ 2100 > G/2 ＝ 2000 ⇒ 停；
// 去掉余量（1900 ≤ 2000）或门槛换成 G（2100 < 4000）都不停 —— 评审方要求的头两条突变由这一格红。
func TestGuardSustainedHighStops(t *testing.T) {
	g := guardJudge(guardEvals(skewSamples(guardSeg(120, func(int) int64 { return 1900 }), 0)))
	if g.stops == 0 || g.maxHi != 2100 {
		t.Errorf("D 恒 1900：停 %d 次、高端最大 %d，应停、高端 2100", g.stops, g.maxHi)
	}
	if g.warns != 0 {
		t.Errorf("D 恒 1900：告警 %d 次，应为 0", g.warns)
	}
}

// guard: 偏慢只告警、不停 —— D 恒为 −2500：1 分位 < −G/2 ⇒ 告警；高端 −2300 远低于门槛 ⇒ 不停。
// 对照：D 恒为 −1900（没慢过 G 的一半）⇒ 不告警。
func TestGuardSlowWarnsOnly(t *testing.T) {
	g := guardJudge(guardEvals(skewSamples(guardSeg(120, func(int) int64 { return -2500 }), 0)))
	if g.stops != 0 || g.warns == 0 {
		t.Errorf("D 恒 −2500：停 %d · 告警 %d，应 0 停、告警 > 0", g.stops, g.warns)
	}
	if c := guardJudge(guardEvals(skewSamples(guardSeg(120, func(int) int64 { return -1900 }), 0))); c.warns != 0 {
		t.Errorf("对照 D 恒 −1900：告警 %d，应为 0", c.warns)
	}
}

// guard: 样本 < 30 不判 —— 一段开头 29 个样本、D 恒为 5000：一次都不判、不停；第 30 个起判、停。
func TestGuardNeedsThirtySamples(t *testing.T) {
	es := guardEvals(skewSamples(guardSeg(40, func(int) int64 { return 5000 }), 0))
	for i, e := range es {
		if i < 29 && e.judged {
			t.Errorf("第 %d 个样本：窗口里只有 %d 个，应不判", i+1, i+1)
		}
		if i >= 29 && !e.judged {
			t.Errorf("第 %d 个样本：应判", i+1)
		}
	}
	if g := guardJudge(es); g.judged != 11 || g.stops != 11 {
		t.Errorf("判 %d 次、停 %d 次，应为 11 · 11", g.judged, g.stops)
	}
}
