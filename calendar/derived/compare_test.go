package derived

import (
	"errors"
	"testing"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
)

// —— 差异清单的零点：probe.md 6.24 × 6.30 ——
//
// ⛔ 期望值取自两张读数表，不取自跑出来的输出：
//
//	6.24  base（calendar/embedded ＋ 253 天真实交易日表）在那 6 天上**都给出一段夜盘**（首段起点是前一交易日 21:00）；
//	      rb 在 embedded 里标称每个交易日都有夜盘 ⇒ 窗口里 241 天 base 都说有夜盘
//	6.30  本库主连：c 6 天（正是那 6 天）· a 230 · NoPick 5
//	⇒ 差异清单 ＝ 恰好那 6 天，种类都是「base 有夜盘 · 观测没有夜盘根」；NoPick 那 5 天**不是**差异
func TestCompareReproducesTables624And630(t *testing.T) {
	rep, err := Judge(build630(t))
	if err != nil {
		t.Fatal(err)
	}
	base := make([]BaseDay, 0, len(table630Days))
	for _, d := range table630Days {
		base = append(base, BaseDay{Day: d, Night: true})
	}
	diffs, err := Compare(rep, base)
	if err != nil {
		t.Fatal(err)
	}
	if len(diffs) != len(table630C) {
		t.Fatalf("差异 %d 条 %+v，要 %d 条（那 6 天）", len(diffs), diffs, len(table630C))
	}
	for i, d := range diffs {
		if d.Day != table630C[i] || d.Kind != BaseNightObservedAbsent || d.Verdict != NightAbsent {
			t.Errorf("第 %d 条 %+v，要 %s · base 有夜盘 · 观测没有夜盘根", i+1, d, table630C[i])
		}
		if table630NoPick[d.Day] {
			t.Errorf("%s 是 NoPick 日（无结论），不该出现在差异清单里", d.Day)
		}
	}
}

// 五种差异各一格，外加「一致」与「无结论不是差异」各一格。
func TestCompareKinds(t *testing.T) {
	mk := func(d tickflow.TradingDay, v Verdict, r NoVerdictReason) DayVerdict {
		dv, err := NewDayVerdict(d, v, r)
		if err != nil {
			t.Fatal(err)
		}
		return dv
	}
	rep := Report{Days: []DayVerdict{
		mk(20260302, NightAbsent, ReasonNone),       // base 有夜盘 ⇒ 差异
		mk(20260303, NightZeroVolume, ReasonNone),   // base 有夜盘 ⇒ 差异（与上一种分开）
		mk(20260304, NightTraded, ReasonNone),       // base 无夜盘 ⇒ 差异
		mk(20260305, NightTraded, ReasonNone),       // base 有夜盘 ⇒ 一致
		mk(20260306, NightAbsent, ReasonNone),       // base 无夜盘 ⇒ 一致
		mk(20260307, NightTraded, ReasonNone),       // 周六，base 不认 ⇒ 差异
		mk(20260310, NoVerdict, ReasonNoPick),       // 无结论 ⇒ 不是差异
		mk(20260311, NoVerdict, ReasonHeldBack),     // 无结论 ⇒ 不是差异（base 认这一天）
		mk(20260308, NoVerdict, ReasonNeverFetched), // ⛔ 无结论而 base **不认**（周日）⇒ 仍不是差异 —— 跳过无结论那一支只在这种输入上才起作用（突变 C1 第一次全绿就是缺这一格）
		mk(20260401, NightAbsent, ReasonNone),       // base 窗口之外 ⇒ 不参与
	}}
	base := []BaseDay{
		{Day: 20260302, Night: true},
		{Day: 20260303, Night: true},
		{Day: 20260304, Night: false},
		{Day: 20260305, Night: true},
		{Day: 20260306, Night: false},
		{Day: 20260309, Night: true}, // 报告里没有这一天 ⇒ base 是交易日而观测一根都没有
		{Day: 20260310, Night: true},
		{Day: 20260311, Night: true},
	}
	diffs, err := Compare(rep, base)
	if err != nil {
		t.Fatal(err)
	}
	want := []Diff{
		{Day: 20260302, Kind: BaseNightObservedAbsent, Verdict: NightAbsent},
		{Day: 20260303, Kind: BaseNightObservedZeroVolume, Verdict: NightZeroVolume},
		{Day: 20260304, Kind: BaseNoNightObservedTraded, Verdict: NightTraded},
		{Day: 20260307, Kind: ObservedNotBaseTradingDay, Verdict: NightTraded},
		{Day: 20260309, Kind: BaseTradingDayNoObservation, Verdict: VerdictUnset},
	}
	if len(diffs) != len(want) {
		t.Fatalf("差异 %d 条 %+v，要 %d 条 %+v", len(diffs), diffs, len(want), want)
	}
	for i := range want {
		if diffs[i] != want[i] {
			t.Errorf("第 %d 条 %+v，要 %+v", i+1, diffs[i], want[i])
		}
	}
}

func TestCompareRejectsBadBase(t *testing.T) {
	rep := Report{}
	if _, err := Compare(rep, nil); !errors.Is(err, ErrBaseEmpty) {
		t.Errorf("空快照：期望 ErrBaseEmpty，实得 %v", err)
	}
	if _, err := Compare(rep, []BaseDay{{Day: 20260302}, {Day: 20260302}}); !errors.Is(err, ErrBaseDuplicate) {
		t.Errorf("重复日：期望 ErrBaseDuplicate，实得 %v", err)
	}
	if _, err := Compare(rep, []BaseDay{{Day: 0}}); !errors.Is(err, ErrBaseDayInvalid) {
		t.Errorf("非法日：期望 ErrBaseDayInvalid，实得 %v", err)
	}
}
