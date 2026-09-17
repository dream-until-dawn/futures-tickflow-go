package derived

import (
	"errors"
	"strings"
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

func mkDV(t *testing.T, d tickflow.TradingDay, v Verdict, r NoVerdictReason) DayVerdict {
	t.Helper()
	dv, err := NewDayVerdict(d, v, r)
	if err != nil {
		t.Fatal(err)
	}
	return dv
}

// 六种差异各一格，外加「一致」两格、「无结论不是差异」三格、「base 比报告窄时窗口外不参与」一格。
//
// ⚠️ 排序有人守（评审方 M8：原来唯一一条只在 base 侧的差异恰好排在最后，删掉排序输出不变）：
// 02-27 只在 base 侧、排在所有观测差异**前面**。不排序时，只在 base 侧的差异是在观测差异**之后**才追加的（顺序随 map），
// ⇒ 02-27 必然不在第一位 ⇒ 删掉排序**必红**，不会时红时绿；03-13 这第二条只在 base 侧的，排序后本来就在最后。
func TestCompareKinds(t *testing.T) {
	rep := Report{From: 20260226, To: 20260410, Days: []DayVerdict{
		mkDV(t, 20260226, NightAbsent, ReasonNone),           // base 窗口（02-27 起）之外 ⇒ 不参与
		mkDV(t, 20260302, NightAbsent, ReasonNone),           // base 有夜盘 ⇒ 差异
		mkDV(t, 20260303, NightZeroVolume, ReasonNone),       // base 有夜盘 ⇒ 差异（与上一种分开）
		mkDV(t, 20260304, NightTraded, ReasonNone),           // base 无夜盘 ⇒ 差异
		mkDV(t, 20260305, NightTraded, ReasonNone),           // base 有夜盘 ⇒ 一致
		mkDV(t, 20260306, NightAbsent, ReasonNone),           // base 无夜盘 ⇒ 一致
		mkDV(t, 20260307, NightTraded, ReasonNone),           // 周六，base 不认 ⇒ 差异
		mkDV(t, 20260308, NoVerdict, ReasonNeverFetched),     // ⛔ 无结论而 base 不认（周日）⇒ 仍不是差异（突变 C1 第一次全绿就是缺这一格）
		mkDV(t, 20260310, NoVerdict, ReasonNoPick),           // 无结论 ⇒ 不是差异
		mkDV(t, 20260311, NoVerdict, ReasonTailUnregistered), // 无结论 ⇒ 不是差异（base 认这一天）
		mkDV(t, 20260312, NightZeroVolume, ReasonNone),       // ⛔ base 无夜盘而观测 b ⇒ 差异（评审方 M7 问出来的那一格）
		mkDV(t, 20260401, NightAbsent, ReasonNone),           // base 窗口（到 03-13）之外 ⇒ 不参与
	}}
	base := []BaseDay{
		{Day: 20260227, Night: true}, // ⛔ 报告里没有这一天、且排在所有观测差异前面 ⇒ 守排序
		{Day: 20260302, Night: true},
		{Day: 20260303, Night: true},
		{Day: 20260304, Night: false},
		{Day: 20260305, Night: true},
		{Day: 20260306, Night: false},
		{Day: 20260310, Night: true},
		{Day: 20260311, Night: true},
		{Day: 20260312, Night: false},
		{Day: 20260313, Night: true}, // 报告里也没有这一天 —— 只在 base 侧的第二条，排在最后（与 02-27 不相邻，排序确定）
	}
	diffs, err := Compare(rep, base)
	if err != nil {
		t.Fatal(err)
	}
	want := []Diff{
		{Day: 20260227, Kind: BaseTradingDayNoObservation, Verdict: VerdictUnset},
		{Day: 20260302, Kind: BaseNightObservedAbsent, Verdict: NightAbsent},
		{Day: 20260303, Kind: BaseNightObservedZeroVolume, Verdict: NightZeroVolume},
		{Day: 20260304, Kind: BaseNoNightObservedTraded, Verdict: NightTraded},
		{Day: 20260307, Kind: ObservedNotBaseTradingDay, Verdict: NightTraded},
		{Day: 20260312, Kind: BaseNoNightObservedZeroVolume, Verdict: NightZeroVolume},
		{Day: 20260313, Kind: BaseTradingDayNoObservation, Verdict: VerdictUnset},
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

// ⛔ 评审方探针 W：base 快照比报告被问的那一段宽 ⇒ 报错，不静默取交集、更不报成「那天没交易」。
func TestCompareRefusesBaseWiderThanReport(t *testing.T) {
	rep := Report{From: 20260302, To: 20260302, Days: []DayVerdict{mkDV(t, 20260302, NightTraded, ReasonNone)}}
	cases := []struct {
		name string
		base []BaseDay
	}{
		{"右边越界（base 取到今天、库只同步到昨天那种形状）", []BaseDay{{Day: 20260302, Night: true}, {Day: 20260303, Night: true}, {Day: 20260304, Night: true}}},
		{"左边越界", []BaseDay{{Day: 20260227, Night: true}, {Day: 20260302, Night: true}}},
	}
	for _, c := range cases {
		diffs, err := Compare(rep, c.base)
		if !errors.Is(err, ErrBaseOutsideReport) {
			t.Errorf("%s：期望 ErrBaseOutsideReport，实得 err=%v diffs=%+v", c.name, err, diffs)
		}
		if diffs != nil {
			t.Errorf("%s：报了错却同时交出差异 %+v —— 调用方忘了看 err 时会拿到假差异", c.name, diffs)
		}
	}
	// 报告没带窗口 ⇒ 也报错
	if _, err := Compare(Report{Days: rep.Days}, []BaseDay{{Day: 20260302, Night: true}}); !errors.Is(err, ErrReportWindowUnset) {
		t.Errorf("报告没带窗口：期望 ErrReportWindowUnset，实得 %v", err)
	}
	// 报告窗口倒置 ⇒ ErrReportWindowUnset（评审方 W4：删掉 `rep.From > rep.To` 那半句原来全绿 ——
	// 倒置时任何 base 日子都会越界，于是错误换成了 ErrBaseOutsideReport、测试照绿；这一格把哨兵钉住）
	if _, err := Compare(Report{From: 20260303, To: 20260302, Days: rep.Days}, []BaseDay{{Day: 20260302, Night: true}}); !errors.Is(err, ErrReportWindowUnset) {
		t.Errorf("报告窗口倒置：期望 ErrReportWindowUnset，实得 %v", err)
	}
	// 正好等宽 ⇒ 不报错
	if _, err := Compare(rep, []BaseDay{{Day: 20260302, Night: true}}); err != nil {
		t.Errorf("base 与报告等宽，不该报错：%v", err)
	}
}

func TestCompareRejectsBadBase(t *testing.T) {
	rep := Report{From: 20260101, To: 20261231}
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

// 报文文字也有人守（评审方次要项：DiffKind.String 覆盖率 0%）：每种非零值有自己的名字、互不相同、不落到兜底格式。
func TestDiffKindStrings(t *testing.T) {
	kinds := []DiffKind{BaseNightObservedAbsent, BaseNightObservedZeroVolume, BaseNoNightObservedTraded,
		BaseNoNightObservedZeroVolume, BaseTradingDayNoObservation, ObservedNotBaseTradingDay}
	seen := map[string]DiffKind{}
	for _, k := range kinds {
		s := k.String()
		if s == DiffUnset.String() || strings.HasPrefix(s, "DiffKind(") {
			t.Errorf("%d 的名字 %q 落到了零值或兜底格式 —— 新加的种类没写进 String", int(k), s)
		}
		if prev, dup := seen[s]; dup {
			t.Errorf("%d 与 %d 共用一个名字 %q —— 报文里分不开两种处置不同的差异", int(prev), int(k), s)
		}
		seen[s] = k
	}
	if got := DiffKind(99).String(); got != "DiffKind(99)" {
		t.Errorf("未知种类的兜底格式 %q", got)
	}
}
