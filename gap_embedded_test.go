package tickflow_test

import (
	"testing"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/embedded"
)

// 这一份跑【真日历】，理由写在 gap_test.go 的 fakeCal 上：
//
// **一个自造的替身，最容易和被测代码错得一模一样。**
// fakeCal 的 Walk 是我按自己对契约的理解写的 —— 若我理解错了，
// PlanGaps 与 fakeCal 会一起错，而单元测试全绿。
//
// ⇒ 所以这里换一条独立的路：拿 calendar/embedded 真跑一遍，
// 只断言**那几条不依赖具体日期的性质**（形状、非交易日成段、越界成第四类），
// 不断言「哪一天是交易日」—— 那是日历的事，不是本层的事。
//
// ⚠️ 本文件是 `package tickflow_test`（外部测试包）：
// `calendar/embedded` import 了根包，根包内的测试文件 import 它就是**循环**。

var key = tickflow.ProductKey{Exchange: "SHFE", Product: "rb"}

// never 是「这一段里一天都没有根」。
//
// ⚠️ 换读法之后它按【段】答，而它返回的是**空 map ＋ nil error** ——
// 那正是「拉过、确认没有」；⛔ 而「答不了」在新契约里落在 **error** 上，
// **不落在空 map 上**（空 map 与「每天都没根」长得一模一样）。
// ⇒ 本文件用得着的只有前者，写下这一句是为了让它别被当成后者的样例。
func never(tickflow.Span) (map[tickflow.TradingDay]bool, error) {
	return map[tickflow.TradingDay]bool{}, nil
}

// mustCal 造一份只含给定交易日的真日历。
func mustCal(t *testing.T, days ...tickflow.TradingDay) tickflow.Calendar {
	t.Helper()
	c, err := embedded.New(days)
	if err != nil {
		t.Fatalf("造日历失败：%v", err)
	}
	return c
}

// shape 断言那三条【对任何输入都成立】的性质。与 gap_test.go 里那份同源，
// 而这里重写一遍不是重复：**它要跑在真日历上**，而那份跑在替身上。
func shape(t *testing.T, from, to tickflow.TradingDay, gaps []tickflow.Gap) {
	t.Helper()
	for i, g := range gaps {
		if g.From > g.To {
			t.Errorf("第 %d 段首尾反了：%s", i, g)
		}
		if g.From < from || g.To > to {
			t.Errorf("第 %d 段 %s 跑出了请求区间 [%s,%s]", i, g, from, to)
		}
		if g.Kind == 0 {
			t.Errorf("第 %d 段的 Kind 是零值", i)
		}
		if i > 0 && g.From <= gaps[i-1].To {
			t.Errorf("第 %d 段与前一段重叠：%s 之后是 %s", i, gaps[i-1], g)
		}
	}
}

// TestPlanGapsOnRealCalendar_WeekendBecomesItsOwnRun 真日历上，
// 周末自成一段「不是交易日」，而它把两侧的「没拉过」断开。
func TestPlanGapsOnRealCalendar_WeekendBecomesItsOwnRun(t *testing.T) {
	// 2020-08-05(三) … 08-07(五) ＋ 08-10(一) …… 08-08/09 是周末，不在表里。
	cal := mustCal(t, 20200805, 20200806, 20200807, 20200810)

	gaps, err := tickflow.PlanGaps(cal, key, 20200805, 20200810, nil, never)
	if err != nil {
		t.Fatalf("不该出错：%v", err)
	}
	shape(t, 20200805, 20200810, gaps)

	want := []tickflow.Gap{
		{From: 20200805, To: 20200807, Kind: tickflow.GapNeverFetched},
		{From: 20200808, To: 20200809, Kind: tickflow.GapNotTrading},
		{From: 20200810, To: 20200810, Kind: tickflow.GapNeverFetched},
	}
	if len(gaps) != len(want) {
		t.Fatalf("期望 %d 段，实得 %d 段：%v", len(want), len(gaps), gaps)
	}
	for i := range want {
		if gaps[i] != want[i] {
			t.Errorf("第 %d 段：期望 %s，实得 %s", i, want[i], gaps[i])
		}
	}
	// ⛔ 合成 1 段就说明相邻性按【交易日】算了 —— 那一段会盖住中间的周末。
	if len(gaps) == 1 {
		t.Error("三段合成了一段：相邻性按交易日算会让「不是交易日」被盖住")
	}
}

// TestPlanGapsOnRealCalendar_OutsideCoverage 请求两端越出 Covers ⇒ 首尾各一段第四类，
// 而**中间那段仍然按交易日分类** —— 这一条挡的是「越界就整段作废」。
func TestPlanGapsOnRealCalendar_OutsideCoverage(t *testing.T) {
	cal := mustCal(t, 20200805, 20200806, 20200807)
	cf, ct, ok := cal.Covers(key)
	if !ok {
		t.Fatal("这份日历该覆盖得了 SHFE.rb —— 前提没成立，后面的断言什么也没验")
	}
	if cf != 20200805 || ct != 20200807 {
		t.Fatalf("覆盖窗口与预期不同：[%s,%s] —— 前提变了，重新挑日期", cf, ct)
	}

	gaps, err := tickflow.PlanGaps(cal, key, 20200803, 20200809, nil, never)
	if err != nil {
		t.Fatalf("不该出错：%v", err)
	}
	shape(t, 20200803, 20200809, gaps)

	want := []tickflow.Gap{
		{From: 20200803, To: 20200804, Kind: tickflow.GapCalendarUnknown},
		{From: 20200805, To: 20200807, Kind: tickflow.GapNeverFetched},
		{From: 20200808, To: 20200809, Kind: tickflow.GapCalendarUnknown},
	}
	if len(gaps) != len(want) {
		t.Fatalf("期望 %d 段，实得 %d 段：%v\n"+
			"  ⇒ 若只有 1 段第四类，就是越界把整段作废了（Walk 的护栏被原样吞了）",
			len(want), len(gaps), gaps)
	}
	for i := range want {
		if gaps[i] != want[i] {
			t.Errorf("第 %d 段：期望 %s，实得 %s", i, want[i], gaps[i])
		}
	}
	// ⚠️ 08-08/09 是周末，而它们落在覆盖之外 ⇒ 报的是【日历答不了】，不是【不是交易日】。
	// 这一格要紧：日历在那一段【没有答案】，而「不是交易日」是一个答案。
	if gaps[2].Kind != tickflow.GapCalendarUnknown {
		t.Errorf("覆盖之外的周末被报成了 %s —— 那是把「答不了」说成了「没有交易」", gaps[2].Kind)
	}
}
