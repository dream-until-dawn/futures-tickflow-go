package main

import (
	"encoding/json"
	"testing"
	"time"
)

// v0.10 P-b0（probe.md 6.38）回放工具的合成测试 —— 不读封存文件，只验取法接对了。

// skewFrame 造一帧只带报价的推送：本机收到 recv、quote.datetime ＝ srv。
func skewFrame(recv, srv int64) tailFrame {
	raw, _ := json.Marshal(map[string]any{"aid": "rtn_data", "data": []any{map[string]any{
		"quotes": map[string]any{tailSym: map[string]any{"datetime": time.UnixMilli(srv).In(cst).Format("2006-01-02 15:04:05.000000")}}}}})
	return tailFrame{RecvMs: recv, Raw: raw}
}

func ms(h, m, s int) int64 { return time.Date(2026, 9, 21, h, m, s, 0, cst).UnixMilli() }

// guard: 样本取法（6.38 一）—— quote.datetime 早于当前段段首的旧报价不进；本机时刻不在任何段里的帧不进；
// 平移 shift 挪的是本机时刻（D 跟着变大 shift）。
func TestSkewSamplesExcludeStaleAndOutOfSession(t *testing.T) {
	fr := []tailFrame{
		skewFrame(ms(13, 30, 1), ms(11, 29, 59)), // 下午开盘后收到的午休前旧报价 ⇒ 不进
		skewFrame(ms(12, 0, 0), ms(12, 0, 0)),    // 午休中 ⇒ 不进
		skewFrame(ms(13, 30, 2), ms(13, 30, 1)),  // 进：D ＝ 1000
	}
	ss := skewSamples(fr, 0)
	if len(ss) != 1 || ss[0].d != 1000 {
		t.Fatalf("样本 %+v，应只有一个、D 1000", ss)
	}
	if sh := skewSamples(fr, 500); len(sh) != 1 || sh[0].d != 1500 || sh[0].recv != ms(13, 30, 2)+500 {
		t.Errorf("平移 500：%+v，应 D 1500、本机时刻 ＋500", sh)
	}
}

// guard: 窗口只含当前段、段首一到清空（评审方批准信 ①）—— 上午末尾 D 都是 −100、下午开头 D 都是 ＋200：
// 下午第一次判定的窗口低端必须是 200（不许把午休前的 −100 带过来）；K 以下不判。
func TestSkewWindowClearsAtSegmentStart(t *testing.T) {
	var fr []tailFrame
	for i := 0; i < 20; i++ {
		r := ms(11, 29, 40) + int64(i)*500
		fr = append(fr, skewFrame(r, r+100))
	}
	for i := 0; i < 20; i++ {
		r := ms(13, 30, 0) + int64(i)*500
		fr = append(fr, skewFrame(r, r-200))
	}
	es := skewLows(skewSamples(fr, 0), skewCand{w: 300000, p: 0, k: 10})
	var pm []skewEval
	for _, e := range es {
		if e.recv >= ms(13, 30, 0) {
			pm = append(pm, e)
		}
	}
	for i, e := range pm {
		if i < 9 && e.judged {
			t.Errorf("下午第 %d 个样本：窗口里只有 %d 个，应不判", i+1, i+1)
		}
		if i >= 9 && (!e.judged || e.low != 200) {
			t.Errorf("下午第 %d 个样本：判 %v 低端 %d，应判、低端 200（午休前的 −100 不许进窗）", i+1, e.judged, e.low)
		}
	}
	if sec, segs := skewUnjudged(pm); sec != 4.5 || len(segs) != 1 {
		t.Errorf("下午不判 %.1f 秒 %v，应为前 9 个样本之间的 4.5 秒、一段", sec, segs)
	}
	// 上面那个窗口 300 秒，午休前的样本按时间就出窗了 —— 「段首清空」那一条在它身上看不见（rb 最短的休息 15 分钟 > 最大的 W）。
	// 用一个长过午休的窗口（3 小时）把它隔离出来：时间上午休前的样本还在窗里，段号不同必须清掉
	long := skewLows(skewSamples(fr, 0), skewCand{w: 3 * 3600000, p: 0, k: 1})
	for _, e := range long {
		if e.recv >= ms(13, 30, 0) && e.low != 200 {
			t.Fatalf("3 小时窗口：下午 %s 的低端 %d，应为 200（窗口按段清空，不按时间）", time.UnixMilli(e.recv).In(cst).Format("15:04:05"), e.low)
		}
	}
}

// guard: 统计量（6.38 一）—— 最小值；5 / 10 分位按最近秩（排序后第 ceil(p·n) 个）。
func TestSkewLowOf(t *testing.T) {
	xs := func() []int64 {
		out := make([]int64, 40)
		for i := range out {
			out[i] = int64(40 - i) // 40 … 1
		}
		return out
	}
	if got := lowOf(xs(), 0); got != 1 {
		t.Errorf("最小值 %d，应为 1", got)
	}
	if got := lowOf(xs(), 0.05); got != 2 {
		t.Errorf("5 分位（40 个，第 ceil(2)＝2 个）%d，应为 2", got)
	}
	if got := lowOf(xs(), 0.10); got != 4 {
		t.Errorf("10 分位（第 4 个）%d，应为 4", got)
	}
	// 40 个时 5% / 10% 恰是整数秩，ceil 与 floor 分不开 ⇒ 再取 30 个：5% ＝ 1.5 ⇒ 第 2 个（ceil），不是第 1 个
	if got := lowOf(xs()[10:], 0.05); got != 2 {
		t.Errorf("30 个的 5 分位 %d，应为 2（ceil(1.5)＝2）", got)
	}
}

// guard: 交叉标定（6.38 二戊，评审方改过的版本）—— 决定可检量的是【窗口低端】自己的散布，不是 D 的散布：
// 上午 30 分钟里 D 的底从 0 慢慢升到 300 ms、下午从 0 升到 150 ms（每 2 秒另抖一个 0 / 50）。
// ⇒ 上午基线 ≈ [0, 300]、下午 ≈ [0, 150]：
//
//	上→下  未平移 0 停（下午整个在上午的范围里）；X＝100 不停（≈ 250 < 300）、X＝200 停（≈ 350 > 300）
//	下→上  未平移就停（上午越过了下午的 150）；X＝100 已停
//	两向在 X＝1000 都停
//
// ⚠️ 第一版造的是「D 以 2 秒为周期在 0–150 里循环」—— 窗口最小值恒为 0、统计量的范围塌成 [0, 0]，任何 X 都停；
// 那是我把「D 的散布」当成了「统计量的散布」（测试的构造错，工具是对的）。
func TestSkewCrossDetectsShift(t *testing.T) {
	var fr []tailFrame
	add := func(from int64, top float64) {
		const n = 3600 // 30 分钟，每 0.5 秒一帧
		for i := 0; i < n; i++ {
			r := from + int64(i)*500
			d := int64(top*float64(i)/n) + int64(i%4/2)*50
			fr = append(fr, skewFrame(r, r-d))
		}
	}
	add(ms(10, 30, 0), 300)
	add(ms(13, 30, 0), 150)
	rows := skewAnalyze(fr, fr, ms(12, 0, 0))
	for _, r := range rows {
		if r.amToPmStop != 0 || r.pmToAmStop == 0 {
			t.Errorf("%s：未平移 上→下 停 %d、下→上 停 %d，应为 0 与 > 0", r.c, r.amToPmStop, r.pmToAmStop)
		}
		if r.minXamToPm != 200 {
			t.Errorf("%s：上→下 最小可检 X %d，应为 200（上午基线 ≈ 300、下午 ≈ 150）", r.c, r.minXamToPm)
		}
		if r.minXpmToAm != 100 {
			t.Errorf("%s：下→上 最小可检 X %d，应为 100", r.c, r.minXpmToAm)
		}
		if r.minX() != 200 {
			t.Errorf("%s：交叉之后的最小可检 X %d，应为两向较大者 200", r.c, r.minX())
		}
		if !r.stop1000Both {
			t.Errorf("%s：X＝1000 没有两向都停", r.c)
		}
	}
}

// guard: 选法（6.38 三）—— 合格 ⇔ 丙两向 0 停 ∧ 丁 0 停 ∧ 戊两向 X＝1000 都停；合格里取最小可检 X 最小的，
// 并列取 W 小，再并列取 K 大；一个方向到 2000 都没停 ⇒ 最小可检 X 取不到（不合格也不会被选）。都不合格 ⇒ −1。
func TestSkewPick(t *testing.T) {
	ok := func(w int64, k int, x1, x2 int64) skewRow {
		return skewRow{c: skewCand{w: w, k: k}, minXamToPm: x1, minXpmToAm: x2, stop1000Both: true}
	}
	rows := []skewRow{
		ok(60000, 10, 300, 500),  // 最小可检 500
		ok(30000, 10, 500, 300),  // 500，W 更小
		ok(30000, 30, 400, 500),  // 500，W 同、K 更大 ⇒ 选它（若最小可检错取两向较小者：这一行 400，第 1 行 300 ⇒ 选到第 1 行）
		ok(120000, 30, 100, 200), // 200，但丙停了 ⇒ 不合格
	}
	rows[3].amToPmStop = 1
	if got := skewPick(rows); got != 2 {
		t.Errorf("选中第 %d 行，应为第 2 行（最小可检并列 500 ⇒ W 30s ⇒ K 30）", got)
	}
	if r := (skewRow{minXamToPm: 300}); r.minX() != 0 {
		t.Errorf("一个方向到 2000 没停：最小可检 %d，应取不到（0）", r.minX())
	}
	none := []skewRow{ok(30000, 10, 300, 300)}
	none[0].otherStop = 1
	if got := skewPick(none); got != -1 {
		t.Errorf("都不合格（丁停了）：选中 %d，应为 −1", got)
	}
}
