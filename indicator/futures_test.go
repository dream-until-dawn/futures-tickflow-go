package indicator

import (
	"math"
	"testing"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
)

// 本文件是期货与 OKX 不一样的那几格（docs/design.md §十五「v0.8 起手」二：乙 丙 丁）。
// 锁板段用合成数据（用户 2026-09-17 定：只用合成数据；真实日线里 H == L 一根都没有，见 testdata/README.md）。

// lockLimitMaxWindow 是本文件被测指标参数里最大的窗口：MA / CCI / BOLL 用 20。
// ⛔ 锁板段至少这么长，否则「窗口内方差 / 均差为 0」那一格碰不到（评审方 2026-09-17 指出：9 根只够 KDJ）。
const lockLimitMaxWindow = 20

// lockLimitBars 造一段期货形状的合成行情：
//
//	随机游走 120 根 ⇒ 长假跳空（开盘跳 +7%）⇒ 随机游走 40 根 ⇒ 涨停锁板 30 根（O == H == L == C，同一个价）⇒ 随机游走 60 根
//
// 每根的 TradingDay 递增；长假那一处隔 10 个自然日（只为让「跳空」在日期上也像长假，指标本身不读日期）。
func lockLimitBars() []tickflow.Bar {
	walk := randomWalk(120+40+60, 20260917)
	var out []tickflow.Bar
	day := 20250102
	add := func(b tickflow.Bar, gap int) {
		day += gap
		b.TradingDay = tickflow.TradingDay(day)
		out = append(out, b)
	}
	for _, b := range walk[:120] {
		add(b, 1)
	}
	// 长假跳空：后面整段平移，使第一根开盘比节前收盘高 7%
	shift := out[len(out)-1].Close*1.07 - walk[120].Open
	for i, b := range walk[120:160] {
		b.Open, b.High, b.Low, b.Close = b.Open+shift, b.High+shift, b.Low+shift, b.Close+shift
		gap := 1
		if i == 0 {
			gap = 10
		}
		add(b, gap)
	}
	limit := out[len(out)-1].Close * 1.05
	for i := 0; i < 30; i++ {
		add(tickflow.Bar{Open: limit, High: limit, Low: limit, Close: limit, Volume: 1}, 1)
	}
	shift2 := limit - walk[160].Open
	for _, b := range walk[160:] {
		b.Open, b.High, b.Low, b.Close = b.Open+shift2, b.High+shift2, b.Low+shift2, b.Close+shift2
		add(b, 1)
	}
	return out
}

// requireFuturesShapes 是乙 / 丙 的判别力断言，**先于被测断言跑**：
// 数据里没有这两种形状，后面的「全绿」就证明不了什么。
func requireFuturesShapes(t *testing.T, cs []tickflow.Bar) (flatFrom, flatTo int) {
	t.Helper()
	bestLen, bestEnd, run := 0, -1, 0
	for i, c := range cs {
		flat := c.Open == c.High && c.High == c.Low && c.Low == c.Close
		if flat && i > 0 && run > 0 && c.Close == cs[i-1].Close {
			run++
		} else if flat {
			run = 1
		} else {
			run = 0
		}
		if run > bestLen {
			bestLen, bestEnd = run, i
		}
	}
	if bestLen < lockLimitMaxWindow {
		t.Fatalf("判别力不够：最长连续平盘（O==H==L==C 且同价）只有 %d 根，要 ≥ %d（本测试最大窗口）", bestLen, lockLimitMaxWindow)
	}
	jump := false
	for i := 1; i < len(cs); i++ {
		if math.Abs(cs[i].Open/cs[i-1].Close-1) >= 0.05 && dayGap(cs[i-1].TradingDay, cs[i].TradingDay) >= 7 {
			jump = true
			break
		}
	}
	if !jump {
		t.Fatalf("判别力不够：没有「隔 ≥7 个自然日、开盘跳 ≥5%%」的长假跳空")
	}
	return bestEnd - bestLen + 1, bestEnd
}

// TestReferenceOnLockLimitAndHolidayGap 是乙：「增量 == 批量」在锁板与长假跳空上仍逐根逐路成立。
// 容差照姊妹仓 reference_test 原值（cmpCol：相对 1e-9），不为新形状放宽。
func TestReferenceOnLockLimitAndHolidayGap(t *testing.T) {
	cs := lockLimitBars()
	requireFuturesShapes(t, cs)
	for _, c := range []Convention{TV, CN} {
		t.Run(c.String(), func(t *testing.T) {
			cmpCol(t, "ma20", MA(20, c), cs, 0, refMA(cs, 20))
			cmpCol(t, "ema20", EMA(20, c), cs, 0, refEMA(cs, 20, c))
			cmpCol(t, "rsi14", RSI(14, c), cs, 0, refRSI(cs, 14, c))
			cmpCol(t, "cci20", CCI(20, c), cs, 0, refCCI(cs, 20))
			mid, up, dn := refBOLL(cs, 20, 2)
			cmpCol(t, "boll.mid", BOLL(20, 2, c), cs, 0, mid)
			cmpCol(t, "boll.up", BOLL(20, 2, c), cs, 1, up)
			cmpCol(t, "boll.dn", BOLL(20, 2, c), cs, 2, dn)
			dif, dea, hist := refMACD(cs, 12, 26, 9, c)
			cmpCol(t, "macd.dif", MACD(12, 26, 9, c), cs, 0, dif)
			cmpCol(t, "macd.dea", MACD(12, 26, 9, c), cs, 1, dea)
			cmpCol(t, "macd.hist", MACD(12, 26, 9, c), cs, 2, hist)
			kv, dv, jv := refKDJ(cs, 9, 3, 3, c)
			cmpCol(t, "kdj.k", KDJ(9, 3, 3, c), cs, 0, kv)
			cmpCol(t, "kdj.d", KDJ(9, 3, 3, c), cs, 1, dv)
			cmpCol(t, "kdj.j", KDJ(9, 3, 3, c), cs, 2, jv)
		})
	}
}

// TestLockLimitNoInfAndFlatConventions 是丙：锁板段上不出 ±Inf，窗口整个落在锁板里的那些根，输出照姊妹仓的平盘约定。
//
// ⚠️ 约定在期货上读反（记下不改，用户裁照搬）：锁涨停是最强的行情，而 KDJ 趋向 50「不强不弱」、CCI 给 0。
// contract.md 静默风险表有这一行。RSI 不在这里断言 50：锁板前的涨跌让均涨 / 均跌不为 0，只会慢慢衰减。
func TestLockLimitNoInfAndFlatConventions(t *testing.T) {
	cs := lockLimitBars()
	from, to := requireFuturesShapes(t, cs)
	limit := cs[from].Close

	for _, c := range []Convention{TV, CN} {
		for _, mk := range allIndicators() {
			ind := mk(c)
			for i, row := range Compute(ind, cs) {
				for k, v := range row {
					if math.IsInf(v, 0) {
						t.Fatalf("%s/%s 第 %d 根第 %d 路是 %v", ind.Name(), c, i, k, v)
					}
				}
			}
		}

		// 窗口 20 整个落在锁板里：从锁板第 20 根起
		cci := Compute(CCI(20, c), cs)
		boll := Compute(BOLL(20, 2, c), cs)
		for i := from + lockLimitMaxWindow - 1; i <= to; i++ {
			if cci[i][0] != 0 {
				t.Errorf("%s 锁板第 %d 根 CCI = %v，平盘约定是 0", c, i-from+1, cci[i][0])
			}
			if math.Abs(boll[i][0]-limit) > eps || boll[i][1] != boll[i][0] || boll[i][2] != boll[i][0] {
				t.Errorf("%s 锁板第 %d 根 BOLL = %v，平盘约定是三轨重合在 %v", c, i-from+1, boll[i], limit)
			}
		}
	}

	// KDJ：窗口 9 整个在锁板里 ⇒ RSV ＝ 50。
	// TV 口径 K ＝ MA(RSV,3)、D ＝ MA(K,3) ⇒ 再过 2 + 2 根，K ＝ D ＝ J ＝ 50 精确。
	tv := Compute(KDJ(9, 3, 3, TV), cs)
	for i := from + 9 - 1 + 4; i <= to; i++ {
		for k, v := range tv[i] {
			if math.Abs(v-50) > eps {
				t.Errorf("TV 锁板第 %d 根 KDJ 第 %d 路 = %v，平盘约定是 50", i-from+1, k, v)
			}
		}
	}
	// CN 口径是指数平滑：|K − 50| 从 RSV 就位起逐根不增
	cn := Compute(KDJ(9, 3, 3, CN), cs)
	for i := from + 9; i <= to; i++ {
		if math.Abs(cn[i][0]-50) > math.Abs(cn[i-1][0]-50)+eps {
			t.Errorf("CN 锁板第 %d 根 K = %v，离 50 反而比前一根（%v）远", i-from+1, cn[i][0], cn[i-1][0])
		}
	}
}

// TestNaNFieldsDoNotAffectBuiltins 是丁：内置七个只读 High / Low / Close。
// 同一份 bars，把 Turnover / Settle / Volume / OpenInterest 全换成 NaN 再喂，七个指标 × 两套口径逐位不变。
//
// 对照一格，缺了它这条测试没有判别力：只改一根的 Close，至少一个指标的输出要变。
func TestNaNFieldsDoNotAffectBuiltins(t *testing.T) {
	base := loadSinaDaily(t, "daily_RB2501.jsonp")
	for i := range base {
		base[i].Turnover = base[i].Volume * base[i].Close // 让基线里这四格都不是 NaN
		if math.IsNaN(base[i].Settle) {
			base[i].Settle = base[i].Close
		}
	}
	nanned := append([]tickflow.Bar(nil), base...)
	for i := range nanned {
		nanned[i].Turnover, nanned[i].Settle = math.NaN(), math.NaN()
		nanned[i].Volume, nanned[i].OpenInterest = math.NaN(), math.NaN()
	}
	for _, c := range []Convention{TV, CN} {
		for _, mk := range allIndicators() {
			a, b := fmtRows(Compute(mk(c), base)), fmtRows(Compute(mk(c), nanned))
			for i := range a {
				if a[i] != b[i] {
					t.Fatalf("%s/%s 第 %d 根：四个字段换成 NaN 后输出变了\n原 %s\n后 %s", mk(c).Name(), c, i, a[i], b[i])
				}
			}
		}
	}

	moved := append([]tickflow.Bar(nil), base...)
	moved[100].Close += 10
	changed := false
	for _, mk := range allIndicators() {
		a, b := fmtRows(Compute(mk(CN), base)), fmtRows(Compute(mk(CN), moved))
		for i := range a {
			if a[i] != b[i] {
				changed = true
			}
		}
	}
	if !changed {
		t.Fatalf("对照失败：改了一根 Close，七个指标一个都没变 —— 上面的「逐位不变」没有判别力")
	}
}
