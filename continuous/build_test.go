package continuous

import (
	"errors"
	"math"
	"testing"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
)

// —— 这一组守的是拼接本身：天轴 · 接缝 · 复权四种 · 到期日未知那一格 ——

func sym(ym int) tickflow.Symbol {
	return tickflow.Symbol{Exchange: tickflow.SHFE, Product: "rb", YearMon: ym}
}

// day 造一根：收盘价、量、持仓都由调用方给，其余取得能过 Bar 的形状即可。
func bar(d tickflow.TradingDay, s tickflow.Symbol, close, vol, oi float64) tickflow.Bar {
	_ = s // Bar 里没有 Symbol（按合约分文件存）——「这一根是谁的」由 Continuous.First ＋ Rolls 推出来
	return tickflow.Bar{
		TradingDay: d,
		Open:       close, High: close, Low: close, Close: close,
		Volume: vol, OpenInterest: oi,
	}
}

// 三天两合约：0803/0804 主力是 2601，0805 换到 2605（持仓反超）。
// 两合约同一天的收盘差 100 ⇒ 基差 100、因子 1.1。
func threeDays() []DayBars {
	a, b := sym(2601), sym(2605)
	return []DayBars{
		{Day: 20200803, Cands: []ContractDay{{Symbol: a, OpenInterest: 100, Volume: 100}, {Symbol: b, OpenInterest: 10, Volume: 10}},
			Bars: map[tickflow.Symbol]tickflow.Bar{a: bar(20200803, a, 1000, 5, 100), b: bar(20200803, b, 1100, 1, 10)}},
		{Day: 20200804, Cands: []ContractDay{{Symbol: a, OpenInterest: 100, Volume: 100}, {Symbol: b, OpenInterest: 10, Volume: 10}},
			Bars: map[tickflow.Symbol]tickflow.Bar{a: bar(20200804, a, 1000, 6, 100), b: bar(20200804, b, 1100, 2, 10)}},
		{Day: 20200805, Cands: []ContractDay{{Symbol: a, OpenInterest: 10, Volume: 10}, {Symbol: b, OpenInterest: 100, Volume: 100}},
			Bars: map[tickflow.Symbol]tickflow.Bar{a: bar(20200805, a, 1000, 3, 10), b: bar(20200805, b, 1100, 9, 100)}},
	}
}

// guard: 拼接交出三样 —— 序列 · 接缝（含基差与因子）· 天轴；天轴就是递进来的那些天。
func TestBuildGivesBarsRollsAndDays(t *testing.T) {
	c, err := Build(ContinuousSpec{Product: "SHFE.rb", Roll: ByOpenInterest{}}, threeDays())
	if err != nil {
		t.Fatalf("Build 出错：%v", err)
	}
	if got := len(c.Bars); got != 3 {
		t.Errorf("Bars %d 根，期望 3", got)
	}
	wantDays := []tickflow.TradingDay{20200803, 20200804, 20200805}
	if len(c.Days) != 3 || c.Days[0] != wantDays[0] || c.Days[2] != wantDays[2] {
		t.Errorf("Days=%v，期望 %v —— 天轴要原样交出去（两端即天轴范围）", c.Days, wantDays)
	}
	if len(c.Rolls) != 1 {
		t.Fatalf("接缝 %d 个，期望 1（0805 换月）：%+v", len(c.Rolls), c.Rolls)
	}
	r := c.Rolls[0]
	if r.Day != 20200805 || r.From != sym(2601) || r.To != sym(2605) {
		t.Errorf("接缝 %+v，期望 0805 从 2601 换到 2605", r)
	}
	if r.Basis != 100 || math.Abs(r.Factor-1.1) > 1e-9 {
		t.Errorf("基差 %v 因子 %v，期望 100 与 1.1", r.Basis, r.Factor)
	}
}

// guard: 复权只动价格 —— 四种方式下 Volume 与 OpenInterest 逐根不变（§八「三条容易踩的」之二）。
//
// ⛔ **基线取自【喂进去的那一份】，不取自 Build**（评审方 2026-09-16 造的 B3 格抓住的）：
// 上一版拿 `Build(..., NoAdjust)` 当基线，而 `NoAdjust` 也走 `adjust()` ⇒
// **一个对所有路径一视同仁的改动（连 NoAdjust 一起改 Volume），基线跟着一起变，断言恒真**：
//
//	只改复权那几支   ⇒ 红 ✅
//	连 NoAdjust 一起改 ⇒ 绿 ⛔ 量真的被改了，而测试说没事
//
// 📎 本仓那条「对照组与验证器自己也要验」的又一形态 —— 这次坏的不是断言，是**基线的出处**。
func TestAdjustNeverTouchesVolumeOrOpenInterest(t *testing.T) {
	in := threeDays()
	// 基线：喂进去的那一份里，每天【被选中那个合约】的量与持仓。与被测函数无关。
	wantVol := []float64{5, 6, 9}
	wantOI := []float64{100, 100, 100}
	for _, m := range []AdjustMethod{NoAdjust, RatioBack, RatioFwd, DiffBack, DiffFwd} {
		got, err := Build(ContinuousSpec{Roll: ByOpenInterest{}, Adjust: m}, in)
		if err != nil {
			t.Fatalf("Adjust=%d：%v", m, err)
		}
		if len(got.Bars) != len(wantVol) {
			t.Fatalf("Adjust=%d：%d 根，期望 %d", m, len(got.Bars), len(wantVol))
		}
		for i := range got.Bars {
			if got.Bars[i].Volume != wantVol[i] || got.Bars[i].OpenInterest != wantOI[i] {
				t.Errorf("Adjust=%d 第 %d 根：量/持仓 %v/%v，而喂进去的是 %v/%v —— 量是手数，没有复权的含义",
					m, i, got.Bars[i].Volume, got.Bars[i].OpenInterest, wantVol[i], wantOI[i])
			}
		}
	}
}

// guard: 后复权以【最早】为基准（历史不变），前复权以【最新】为基准（最新不变）。
//
// ⛔ 这一格钉的是 §八 那条「前复权每换一次月全部历史都会变 ⇒ 回测不可复现」：
// 两者的差别就在**哪一端不动**，而那正是可复现与否的分界。
func TestBackAdjustKeepsHistoryForwardAdjustKeepsLatest(t *testing.T) {
	in := threeDays()
	raw, _ := Build(ContinuousSpec{Roll: ByOpenInterest{}}, in)
	first, last := raw.Bars[0].Close, raw.Bars[len(raw.Bars)-1].Close

	for _, m := range []AdjustMethod{RatioBack, DiffBack} {
		got, err := Build(ContinuousSpec{Roll: ByOpenInterest{}, Adjust: m}, in)
		if err != nil {
			t.Fatal(err)
		}
		if got.Bars[0].Close != first {
			t.Errorf("后复权（%d）把最早那一根改了：%v ⇒ %v —— 后复权的基准是最早", m, first, got.Bars[0].Close)
		}
		if got.Bars[2].Close == last {
			t.Errorf("后复权（%d）没有改换月之后那一段（仍是 %v）—— 那样跳空还在", m, last)
		}
	}
	for _, m := range []AdjustMethod{RatioFwd, DiffFwd} {
		got, err := Build(ContinuousSpec{Roll: ByOpenInterest{}, Adjust: m}, in)
		if err != nil {
			t.Fatal(err)
		}
		if got.Bars[2].Close != last {
			t.Errorf("前复权（%d）把最新那一根改了：%v ⇒ %v —— 前复权的基准是最新", m, last, got.Bars[2].Close)
		}
		if got.Bars[0].Close == first {
			t.Errorf("前复权（%d）没有改换月之前那一段（仍是 %v）", m, first)
		}
	}
	// 📎 顺带钉住「前复权会改写历史」这件事本身：同一段历史，换月之前那一根的值在两族之间不同。
	back, _ := Build(ContinuousSpec{Roll: ByOpenInterest{}, Adjust: DiffBack}, in)
	fwd, _ := Build(ContinuousSpec{Roll: ByOpenInterest{}, Adjust: DiffFwd}, in)
	if back.Bars[0].Close == fwd.Bars[0].Close {
		t.Errorf("前复权与后复权在最早那一根上给出同一个值（%v）—— 那两族的分界没了", back.Bars[0].Close)
	}
}

// guard: 递进来的天不是严格升序 ⇒ 拒，不自己排序。
func TestBuildRejectsNonAscendingDays(t *testing.T) {
	in := threeDays()
	in[1], in[2] = in[2], in[1]
	if _, err := Build(ContinuousSpec{Roll: ByOpenInterest{}}, in); !errors.Is(err, ErrDaysNotAscending) {
		t.Errorf("err=%v，期望 ErrDaysNotAscending —— 自己排序会把「上游拼错了」变成一次静默修复", err)
	}
}

// guard: 到期日是 ExpiryUnknown ⇒ **返回错误**，不是「跳过这个候选」。
//
// ⛔ 跳过会把「不知道它什么时候到期」悄悄变成「这个合约不参与换月」—— 把未知升级成一个肯定的答案。
func TestFixedBarDaysRejectsUnknownExpiry(t *testing.T) {
	r := &FixedBarDaysBeforeExpiry{N: 3}
	cands := []ContractDay{
		{Symbol: sym(2601), Expiry: 20200810, OpenInterest: 100},
		{Symbol: sym(2605), Expiry: ExpiryUnknown, OpenInterest: 10},
	}
	if _, _, err := r.PickErr(20200803, cands); !errors.Is(err, ErrExpiryUnknown) {
		t.Errorf("err=%v，期望 ErrExpiryUnknown", err)
	}
	if got := r.Counted(); got.Days != 0 {
		t.Errorf("判不了的时候 Counted=%+v，期望空 —— 那一段没有数过", got)
	}
	// ⚠️ 排序那一格：ExpiryUnknown ＝ 0，比任何真实交易日都小 ⇒ 任何按 Expiry 排序的代码都会把它排到最前。
	// 本规则不排序、直接拒 —— 这一句断言的就是「它没有把 0 当成最早的到期日去用」。
	if sym, ok, err := r.PickErr(20200803, cands[1:]); err == nil || ok || sym != (tickflow.Symbol{}) {
		t.Errorf("只剩一个 ExpiryUnknown 的候选时给了 (%v,%v,%v)，期望 (零值,false,ErrExpiryUnknown)", sym, ok, err)
	}
}

// guard: 按天数换月的规则要把【实际数到的那一段】交出去，而 Build 要把它填进 Roll。
//
// 构造：天轴 0803..0807；a（到期 0805）只在 0803..0805 有根，b（到期很远）五天都有。N=1。
//
//	0803  a 离到期还有 {0804,0805} 两天 > 1 ⇒ 留；持仓 100 ＞ b 的 10 ⇒ 选 a
//	0804  a 只剩 {0805} 一天，不 > 1 ⇒ 排除 ⇒ 换到 b ⇒ 这一天是换月点
//	⇒ 这一次换月时为 b 数到的那一段是 {0805,0806,0807}
func TestCountedSpanReachesTheRoll(t *testing.T) {
	a, b := sym(2601), sym(2605)
	var in []DayBars
	for _, d := range []tickflow.TradingDay{20200803, 20200804, 20200805, 20200806, 20200807} {
		day := DayBars{Day: d, Bars: map[tickflow.Symbol]tickflow.Bar{b: bar(d, b, 1100, 1, 10)}}
		day.Cands = []ContractDay{{Symbol: b, Expiry: 20201215, OpenInterest: 10}}
		if d <= 20200805 { // a 到期之后就不在市了
			day.Cands = append([]ContractDay{{Symbol: a, Expiry: 20200805, OpenInterest: 100}}, day.Cands...)
			day.Bars[a] = bar(d, a, 1000, 1, 100)
		}
		in = append(in, day)
	}
	c, err := Build(ContinuousSpec{Roll: &FixedBarDaysBeforeExpiry{N: 1}, Adjust: NoAdjust}, in)
	if err != nil {
		t.Fatalf("Build 出错：%v", err)
	}
	if c.First != a {
		t.Errorf("First=%v，期望 %v —— 第一段的合约要交出去（Bar 里没有 Symbol）", c.First, a)
	}
	if len(c.Rolls) != 1 || c.Rolls[0].Day != 20200804 {
		t.Fatalf("接缝 %+v，期望一个、落在 0804", c.Rolls)
	}
	got := c.Rolls[0].Counted
	if got.Days != 3 || got.From != 20200805 || got.To != 20200807 {
		t.Errorf("Counted=%+v，期望 {0805 0807 3} —— 不交出去的话，下游看不出「它数的那几天里少了几天」", got)
	}
}

// guard: 天轴上缺一天 ⇒ 数到的那一段**当场少一天**，而换月日跟着提前。
//
// ⛔ 这一格钉的是那条约定的**可查性**：库里缺一天是调用方违反约定（那一段里有「没拉过」），
// 本包不替它检查（一检查就要日历）——**而它必须看得出来**。
func TestMissingDayShowsUpInCountedSpan(t *testing.T) {
	a, b := sym(2601), sym(2605)
	mk := func(days []tickflow.TradingDay) Continuous {
		var in []DayBars
		for _, d := range days {
			day := DayBars{Day: d, Bars: map[tickflow.Symbol]tickflow.Bar{b: bar(d, b, 1100, 1, 10)}}
			day.Cands = []ContractDay{{Symbol: b, Expiry: 20201215, OpenInterest: 10}}
			if d <= 20200805 {
				day.Cands = append([]ContractDay{{Symbol: a, Expiry: 20200805, OpenInterest: 100}}, day.Cands...)
				day.Bars[a] = bar(d, a, 1000, 1, 100)
			}
			in = append(in, day)
		}
		c, err := Build(ContinuousSpec{Roll: &FixedBarDaysBeforeExpiry{N: 1}, Adjust: NoAdjust}, in)
		if err != nil {
			t.Fatalf("Build 出错：%v", err)
		}
		return c
	}
	full := mk([]tickflow.TradingDay{20200803, 20200804, 20200805, 20200806, 20200807})
	hole := mk([]tickflow.TradingDay{20200803, 20200804, 20200805, 20200807}) // 少了 0806
	if len(full.Rolls) != 1 || len(hole.Rolls) != 1 {
		t.Fatalf("接缝数不对：full=%d hole=%d", len(full.Rolls), len(hole.Rolls))
	}
	if hole.Rolls[0].Counted.Days != full.Rolls[0].Counted.Days-1 {
		t.Errorf("缺一天之后数到 %d 天，完整时 %d 天 —— 期望正好少一天；\n"+
			"  ⇒ 看不出少了几天的话，违反在上游、报错在下游、中间没有痕迹",
			hole.Rolls[0].Counted.Days, full.Rolls[0].Counted.Days)
	}
}

// guard: 换月这一刻的价格用不了（0 或非有限）⇒ **源头拒**，不让四种复权各自改编。
//
// ⛔ 由来（评审方 2026-09-16 造输入量的）：同一份「旧合约收盘 ＝ 0」的输入，四种方式各错各的 ——
// RatioBack 静默变成没复权 · RatioFwd 把早段乘成 0 · DiffBack 把最新那根变成 0 · DiffFwd 把早段抬高。
// ⇒ 「给 Factor 兜底」不够：Basis 同样被毒到。而 **0 是一个看起来正常的价格**，
// 它会变成一整条被清零或被抬高的序列，**全程不报错**。
func TestBuildRejectsUnusableRollPrice(t *testing.T) {
	a, b := sym(2601), sym(2605)
	in := threeDays()
	// 换月那天（0805）旧合约的收盘改成 0。
	old := in[2].Bars[a]
	old.Close = 0
	in[2].Bars[a] = old
	for _, m := range []AdjustMethod{NoAdjust, RatioBack, RatioFwd, DiffBack, DiffFwd} {
		_, err := Build(ContinuousSpec{Roll: ByOpenInterest{}, Adjust: m}, in)
		if !errors.Is(err, ErrRollPriceUnusable) {
			t.Errorf("Adjust=%d：err=%v，期望 ErrRollPriceUnusable —— 四种方式都该在同一处停下", m, err)
		}
	}
	_ = b
}

// guard: 规则说「这一天没有主力」⇒ 那一天要**留声**（NoPick），而不是与「那天真没数据」同形。
//
// ⛔ 这一格钉的是 v0.6 勘误三那一族：下游（derived）要从「这一天有没有根」判夜盘，
// 规则造成的空洞若不留声，它会把「规则说不清」读成「那天没开」。
func TestNoPickDaysAreNamed(t *testing.T) {
	a, b := sym(2601), sym(2605)
	in := threeDays()
	// 0804：持仓最大是 a，成交最大是 b ⇒ ByOIAndVolume 说不清。
	in[1].Cands = []ContractDay{{Symbol: a, OpenInterest: 100, Volume: 10}, {Symbol: b, OpenInterest: 10, Volume: 100}}
	c, err := Build(ContinuousSpec{Roll: ByOIAndVolume{}, Adjust: NoAdjust}, in)
	if err != nil {
		t.Fatalf("Build 出错：%v", err)
	}
	if len(c.NoPick) != 1 || c.NoPick[0] != 20200804 {
		t.Errorf("NoPick=%v，期望恰好点名 0804 —— 不留声的话，它与「那天真没数据」在下游同形", c.NoPick)
	}
	if len(c.Days) != 3 {
		t.Errorf("Days=%v，期望三天都在 —— 天轴记的是「库里有这一天」，不是「这一天有主力」", c.Days)
	}
	if len(c.Bars) != 2 {
		t.Errorf("Bars=%d 根，期望 2（0804 不产出）", len(c.Bars))
	}
}

// guard: `Basis` 的两种取法**不是同一件事** —— 换月日旧合约还有根 vs 已经没根，各算各的。
//
// ⚠️ 这一格不判哪个对，只把差别钉住：两条路给出的 Basis/Factor 必须不等；
// 相等的话，那条分支就是死的（而注释里却写着它有意义）。
func TestBasisDependsOnWhetherOldContractStillHasABar(t *testing.T) {
	a, b := sym(2601), sym(2605)
	withOld := threeDays() // 0805 两个合约都有根：旧 1000 新 1100 ⇒ Basis 100
	noOld := threeDays()   // 0805 旧合约没根 ⇒ 退回它前一天的收盘
	noOld[2].Bars = map[tickflow.Symbol]tickflow.Bar{b: bar(20200805, b, 1100, 9, 100)}
	old := withOld[2].Bars[a]
	old.Close = 900 // 让两条路真的分开：当天 900，而前一天是 1000
	withOld[2].Bars[a] = old

	c1, err1 := Build(ContinuousSpec{Roll: ByOpenInterest{}}, withOld)
	c2, err2 := Build(ContinuousSpec{Roll: ByOpenInterest{}}, noOld)
	if err1 != nil || err2 != nil {
		t.Fatalf("Build 出错：%v / %v", err1, err2)
	}
	if len(c1.Rolls) != 1 || len(c2.Rolls) != 1 {
		t.Fatalf("接缝数不对：%d / %d", len(c1.Rolls), len(c2.Rolls))
	}
	if c1.Rolls[0].Basis == c2.Rolls[0].Basis {
		t.Errorf("两条路算出同一个 Basis（%v）—— 那条分支是死的，而注释里写着它有意义", c1.Rolls[0].Basis)
	}
	if c1.Rolls[0].Basis != 200 { // 1100 − 900（当天旧合约的收盘）
		t.Errorf("旧合约当天有根时 Basis=%v，期望 200（1100 − 900）", c1.Rolls[0].Basis)
	}
	if c2.Rolls[0].Basis != 100 { // 1100 − 1000（旧合约前一天的收盘）
		t.Errorf("旧合约当天没根时 Basis=%v，期望 100（1100 − 1000）", c2.Rolls[0].Basis)
	}
}
