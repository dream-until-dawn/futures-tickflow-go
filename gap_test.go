package tickflow

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// fakeCal 是一份【最小日历】：一个覆盖窗口 + 一张「哪天交易」的表。
//
// ⚠️ 它是我自己写的，所以它**证明不了 PlanGaps 对真日历也对**——
// **一个自造的替身，最容易和被测代码错得一模一样。**
// 那一条由 gap_embedded_test.go 里跑 calendar/embedded 的用例接着。
type fakeCal struct {
	from, to TradingDay
	ok       bool
	trading  map[TradingDay]bool
	walks    int // 记 Walk 被调了几次，给「求交在最前」那条断言用
}

func (c *fakeCal) Covers(ProductKey) (TradingDay, TradingDay, bool) { return c.from, c.to, c.ok }

func (c *fakeCal) Walk(_ ProductKey, from, to TradingDay, fn func(Day) bool) error {
	c.walks++
	// 照真日历的护栏：区间任一端落在覆盖之外就报错，绝不静默少遍历。
	if !c.ok || from < c.from || to > c.to {
		return fmt.Errorf("fakeCal: [%s,%s] 越界：%w", from, to, ErrUncovered)
	}
	for d := from; d <= to; d = natNext(d) {
		if c.trading[d] && !fn(Day{Num: d, Sessions: []Session{{Start: 1, End: 2}}}) {
			return nil
		}
	}
	return nil
}

func (c *fakeCal) DayAt(ProductKey, int64) (Day, error) { return Day{}, ErrUncovered }

func (c *fakeCal) DayOf(_ ProductKey, n TradingDay) (Day, error) {
	if c.trading[n] {
		return Day{Num: n}, nil
	}
	return Day{}, ErrNotTradingDay
}

func (c *fakeCal) Template(ProductKey, TradingDay) (SessionTemplate, error) {
	return SessionTemplate{}, ErrUncovered
}

var _ Calendar = (*fakeCal)(nil)

// week 是 2020-01-06(一) … 2020-01-12(日)：周一到周五交易，周六周日不交易。
func week() *fakeCal {
	c := &fakeCal{from: 20200106, to: 20200112, ok: true, trading: map[TradingDay]bool{}}
	for _, d := range []TradingDay{20200106, 20200107, 20200108, 20200109, 20200110} {
		c.trading[d] = true
	}
	return c
}

func always(v bool) func(TradingDay) (bool, error) {
	return func(TradingDay) (bool, error) { return v, nil }
}

var testKey = ProductKey{Exchange: "SHFE", Product: "rb"}

// TestPlanGapsEachInputTriggersExactlyOneKind 六类各一条输入，而断言是
// **「各只触发一条、且六条互不相同、且并集就是六类」**。
//
// ⛔ 只断言「六条输入都过了」不够，那和「六条输入触发了同一类」**绿得一模一样**
// （评审方 2026-09-09 预告的那一问）。所以这里三条断言缺一不可：
//
//	一  每条输入 ⇒ 恰好 1 个 Gap，且 Kind 等于期望   ← 挡「一条输入触发了两类」
//	二  六条的 Kind 互不相同                          ← 挡「两条输入触发了同一类」
//	三  并集 == 六类全集                              ← 挡「少了一类而表里凑数」
func TestPlanGapsEachInputTriggersExactlyOneKind(t *testing.T) {
	type tc struct {
		name    string
		from    TradingDay
		cov     []SpanStatus
		hasBars func(TradingDay) (bool, error)
		want    GapKind
	}
	cases := []tc{
		{"没拉过：交易日、coverage 为空", 20200106, nil, always(false), GapNeverFetched},
		{"拉过确认没有：coverage 覆盖、已走查、那天没根", 20200106,
			[]SpanStatus{{Span: Span{From: 20200106, To: 20200110}}}, always(false), GapConfirmedEmpty},
		{"不是交易日：周六", 20200111, nil, always(false), GapNotTrading},
		{"日历答不了：落在 Covers 之外", 20200103, nil, always(false), GapCalendarUnknown},
		{"存储答不了·未走查", 20200106,
			[]SpanStatus{{Span: Span{From: 20200106, To: 20200110}, Err: ErrSpanUnverified}},
			always(false), GapStoreUnverified},
		{"存储答不了·旧格式", 20200106,
			[]SpanStatus{{Span: Span{From: 20200106, To: 20200110}, Err: ErrLegacyMeta}},
			always(false), GapStoreLegacy},
	}

	got := map[GapKind]string{}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gaps, err := PlanGaps(week(), testKey, c.from, c.from, c.cov, c.hasBars)
			if err != nil {
				t.Fatalf("不该出错：%v", err)
			}
			if len(gaps) != 1 {
				t.Fatalf("期望【恰好 1 段】，实得 %d 段：%v\n"+
					"  ⇒ 一条输入触发了不止一类，或者一类都没触发", len(gaps), gaps)
			}
			if gaps[0].Kind != c.want {
				t.Fatalf("期望 %s，实得 %s", c.want, gaps[0].Kind)
			}
			if gaps[0].From != c.from || gaps[0].To != c.from {
				t.Errorf("单日请求，段却是 %s..%s", gaps[0].From, gaps[0].To)
			}
		})
		if prev, dup := got[c.want]; dup {
			t.Errorf("两条输入触发了【同一类】%s：%q 与 %q\n"+
				"  ⇒ 这正是「六条都过了」与「六条各触发一类」的差别", c.want, prev, c.name)
		}
		got[c.want] = c.name
	}

	all := []GapKind{GapNeverFetched, GapConfirmedEmpty, GapNotTrading,
		GapCalendarUnknown, GapStoreUnverified, GapStoreLegacy}
	for _, k := range all {
		if _, ok := got[k]; !ok {
			t.Errorf("%s 一条输入都没触发到 —— 六类里少了一类", k)
		}
	}
	if len(got) != len(all) {
		t.Errorf("触发到 %d 类，而六类全集是 %d 类", len(got), len(all))
	}
}

// checkShape 断言的是【对任何输入都成立】的那几条，不是某几条样例。
func checkShape(t *testing.T, from, to TradingDay, gaps []Gap) {
	t.Helper()
	for i, g := range gaps {
		if g.From > g.To {
			t.Errorf("第 %d 段首尾反了：%s", i, g)
		}
		if g.From < from || g.To > to {
			t.Errorf("第 %d 段 %s 跑出了请求区间 [%s,%s]", i, g, from, to)
		}
		if g.Kind == 0 {
			t.Errorf("第 %d 段的 Kind 是零值 —— 零值不合法", i)
		}
		if i == 0 {
			continue
		}
		p := gaps[i-1]
		if g.From <= p.To {
			t.Errorf("第 %d 段与前一段重叠：%s 之后是 %s", i, p, g)
		}
		if p.To == natPrev(g.From) && p.Kind == g.Kind {
			t.Errorf("第 %d 段与前一段【首尾相接且同类】而没有合并：%s 与 %s", i, p, g)
		}
	}
}

// TestPlanGapsPartitionsTheRange 一次跨越五类的请求：形状 ＋ 逐段读数。
func TestPlanGapsPartitionsTheRange(t *testing.T) {
	// 请求 2020-01-03(五) … 2020-01-13(一)，而日历只覆盖 06..12。
	//   01-03,04,05  覆盖之外           ⇒ 日历答不了
	//   01-06        coverage 有、有根   ⇒ 不是缺口（并且它断开前后）
	//   01-07,08     coverage 有、没根   ⇒ 拉过确认没有
	//   01-09,10     coverage 没有       ⇒ 没拉过
	//   01-11,12     周末                ⇒ 不是交易日
	//   01-13        覆盖之外           ⇒ 日历答不了
	cal := week()
	cov := []SpanStatus{{Span: Span{From: 20200106, To: 20200108}}}
	has := func(d TradingDay) (bool, error) { return d == 20200106, nil }

	gaps, err := PlanGaps(cal, testKey, 20200103, 20200113, cov, has)
	if err != nil {
		t.Fatalf("不该出错：%v", err)
	}
	checkShape(t, 20200103, 20200113, gaps)

	want := []Gap{
		{20200103, 20200105, GapCalendarUnknown},
		{20200107, 20200108, GapConfirmedEmpty},
		{20200109, 20200110, GapNeverFetched},
		{20200111, 20200112, GapNotTrading},
		{20200113, 20200113, GapCalendarUnknown},
	}
	if len(gaps) != len(want) {
		t.Fatalf("段数不符：期望 %d，实得 %d\n  实得：%v", len(want), len(gaps), gaps)
	}
	for i := range want {
		if gaps[i] != want[i] {
			t.Errorf("第 %d 段：期望 %s，实得 %s", i, want[i], gaps[i])
		}
	}
	// ⛔ 有数据的 01-06 **不在结果里**，而它把前后两段【断开】了。
	for _, g := range gaps {
		if g.From <= 20200106 && 20200106 <= g.To {
			t.Errorf("01-06 有数据，却被算进了 %s", g)
		}
	}
}

// TestPlanGapsMergesOnlyWithinAKind 相邻同类要合，不同类绝不合。
func TestPlanGapsMergesOnlyWithinAKind(t *testing.T) {
	cal := week()
	// 06..10 全是「没拉过」的交易日，11..12 是周末 ⇒ 期望 2 段，不是 1 段也不是 7 段。
	gaps, err := PlanGaps(cal, testKey, 20200106, 20200112, nil, always(false))
	if err != nil {
		t.Fatalf("不该出错：%v", err)
	}
	checkShape(t, 20200106, 20200112, gaps)
	want := []Gap{
		{20200106, 20200110, GapNeverFetched},
		{20200111, 20200112, GapNotTrading},
	}
	if len(gaps) != 2 || gaps[0] != want[0] || gaps[1] != want[1] {
		t.Fatalf("期望 %v，实得 %v\n"+
			"  ⇒ 合成 1 段 = 跨类别合并了（那会让「不是交易日」被盖住）；\n"+
			"  ⇒ 7 段 = 同类相邻没合（那样 Gap 是区间类型就没有意义了）", want, gaps)
	}
}

// TestPlanGapsIntersectsBeforeClassifying 求交必须在最前 ——
// 越界那几天若先被 coverage 判成「没拉过」，调用方会去重拉一段【日历根本答不了】的区间。
func TestPlanGapsIntersectsBeforeClassifying(t *testing.T) {
	cal := week()
	// coverage 故意声称覆盖了日历答不了的那几天。
	cov := []SpanStatus{{Span: Span{From: 20200101, To: 20200131}}}
	gaps, err := PlanGaps(cal, testKey, 20200103, 20200105, cov, always(true))
	if err != nil {
		t.Fatalf("不该出错：%v", err)
	}
	if len(gaps) != 1 || gaps[0].Kind != GapCalendarUnknown {
		t.Fatalf("期望整段【日历答不了】，实得 %v\n"+
			"  ⇒ 若得到「没拉过」或空结果，就是分类先于求交跑了", gaps)
	}
	if cal.walks != 0 {
		t.Errorf("交集为空时不该调 Walk，实调 %d 次 —— 真日历会当场报越界", cal.walks)
	}
}

// TestPlanGapsWholeProductUncovered 品种整个没收录 ⇒ 整段第四类，且不碰 Walk。
func TestPlanGapsWholeProductUncovered(t *testing.T) {
	cal := &fakeCal{ok: false}
	gaps, err := PlanGaps(cal, testKey, 20200106, 20200110, nil, always(true))
	if err != nil {
		t.Fatalf("不该出错：%v", err)
	}
	want := Gap{20200106, 20200110, GapCalendarUnknown}
	if len(gaps) != 1 || gaps[0] != want {
		t.Fatalf("期望 %v，实得 %v", want, gaps)
	}
	if cal.walks != 0 {
		t.Errorf("品种没收录还去 Walk 了 %d 次", cal.walks)
	}
}

// TestPlanGapsAbortsOnBroken 「坏了」不是「答不了」：中止，不折进任何一类。
func TestPlanGapsAbortsOnBroken(t *testing.T) {
	boom := errors.New("segfile: 读第 7 条记录失败")
	for _, c := range []struct {
		name    string
		cov     []SpanStatus
		hasBars func(TradingDay) (bool, error)
	}{
		{"hasBars 报错（.dat 读坏了）",
			[]SpanStatus{{Span: Span{From: 20200106, To: 20200110}}},
			func(TradingDay) (bool, error) { return false, boom }},
		{"coverage 报了一个认不出的错误",
			[]SpanStatus{{Span: Span{From: 20200106, To: 20200110}, Err: boom}},
			always(false)},
	} {
		t.Run(c.name, func(t *testing.T) {
			gaps, err := PlanGaps(week(), testKey, 20200106, 20200110, c.cov, c.hasBars)
			if err == nil {
				t.Fatalf("期望中止，而它返回了 %v —— 「坏了」被折成了缺口", gaps)
			}
			if !errors.Is(err, boom) {
				t.Errorf("错误没有把原因包进去：%v", err)
			}
			if gaps != nil {
				t.Errorf("中止时不该返回半截结果，实得 %v", gaps)
			}
			if !strings.Contains(err.Error(), "坏了") {
				t.Errorf("错误消息没说清这是【坏了】而不是【答不了】：%v", err)
			}
		})
	}
}

// TestPlanGapsRejectsBadInput 四个前提：日历、hasBars、区间合法、区间不反。
func TestPlanGapsRejectsBadInput(t *testing.T) {
	for _, c := range []struct {
		name string
		call func() ([]Gap, error)
	}{
		{"没给日历", func() ([]Gap, error) {
			return PlanGaps(nil, testKey, 20200106, 20200110, nil, always(true))
		}},
		{"没给 hasBars", func() ([]Gap, error) {
			return PlanGaps(week(), testKey, 20200106, 20200110, nil, nil)
		}},
		{"区间反了", func() ([]Gap, error) {
			return PlanGaps(week(), testKey, 20200110, 20200106, nil, always(true))
		}},
		{"区间不合法", func() ([]Gap, error) {
			return PlanGaps(week(), testKey, 0, 20200110, nil, always(true))
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := c.call(); err == nil {
				t.Fatal("期望报错")
			}
		})
	}
}

// TestNaturalDayArithmetic 月末、年末、闰日 —— 直接对 YYYYMMDD 加减 1 会在这里出事。
func TestNaturalDayArithmetic(t *testing.T) {
	for _, c := range []struct{ d, next TradingDay }{
		{20200131, 20200201},
		{20200228, 20200229}, // 2020 是闰年
		{20200229, 20200301},
		{20191231, 20200101},
		{20210228, 20210301}, // 2021 不是闰年
	} {
		if got := natNext(c.d); got != c.next {
			t.Errorf("natNext(%s) = %s，期望 %s", c.d, got, c.next)
		}
		if got := natPrev(c.next); got != c.d {
			t.Errorf("natPrev(%s) = %s，期望 %s", c.next, got, c.d)
		}
	}
}
