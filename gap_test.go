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

// byDay 把一个【逐日谓词】接成 `PlanGaps` 要的【按段】读法。
//
// ⛔ 它是**测试专用的形状转换器**，理由要写清楚，别让它冒充生产路径：
// 本文件这些用例断言的是**分类逻辑**（哪一天落进哪一类），
// 而不是**读取形状**（读几次、每次读多少）。⇒ 用逐日谓词表达用例更直接。
//
// 🔴 而「读取形状」那一半**另有人守，不靠这里**：
//
//	TestPlanGapsReadsNothingWhenNoCoverage       cov 为空 ⇒ 读 0 次
//	TestPlanGapsReadsEachSpanAtMostOnce          每段最多读一次
//	syncer_segfile_seam_test.go                  真库穿过这条缝
//	store/segfile 的等价性测试                    新旧两个读法答案一致
//
// ⚠️ 写下这张表，是因为一个转换器最容易造成的错觉是
// **「这些用例已经把新读法测过了」** —— 它们没有，它们测的是它上游那一段。
func byDay(has func(TradingDay) (bool, error)) func(Span) (map[TradingDay]bool, error) {
	return func(sp Span) (map[TradingDay]bool, error) {
		out := make(map[TradingDay]bool)
		for d := sp.From; d <= sp.To; d = natNext(d) {
			v, err := has(d)
			if err != nil {
				return nil, err
			}
			if v {
				out[d] = true
			}
		}
		return out, nil
	}
}

func always(v bool) func(Span) (map[TradingDay]bool, error) {
	return byDay(func(TradingDay) (bool, error) { return v, nil })
}

var testKey = ProductKey{Exchange: "SHFE", Product: "rb"}

// TestPlanGapsEachInputTriggersExactlyOneKind 六类各一条输入，而断言是
// **「各只触发一条、且六条互不相同、且并集就是六类」**。
//
// ⛔ 三条断言缺一不可，**而它们守的不是同一个东西** ——
// 这一格的理由上一版写错了，改在这里（评审方 2026-09-09 指出，我读代码复核一致）：
//
//	一  每条输入 ⇒ **恰好 1 段** 且 Kind == 期望     ← 守【实现】
//	二  六条的【期望】两两不同                        ← 守【期望表】
//	三  六条的【期望】并集 == 六类全集                ← 守【期望表】
//
// ⚠️ 上一版给二写的理由是「挡『两条输入触发了同一类』」——**推不成立**：
// 那种失败【第一条就挡住了】（B 那条的 Kind 断言会当场红）。
// 而且二、三读的是 c.want，**根本没看实现返回了什么**。
//
// ⇒ 二、三真正挡的是**期望表自己退化**：
// 有人看见 B 红了，把 B 的期望改成 A 的那一类「让它过」⇒ 两条 per-input 都绿，
// **而六类不再覆盖六类**。同族是本仓 TestCapsAndBarsAgreeOnPeriods 那句
// 「候选集退化了：支持 %d / 拒绝 %d —— 两侧都得有」。
//
// ⇒ 判据（值得单记）：**一条断言的价值，要按【它挡住的那个失败长什么样】来写** ——
// **理由写错了，下一个人删它的时候会以为删的是冗余。**
func TestPlanGapsEachInputTriggersExactlyOneKind(t *testing.T) {
	type tc struct {
		name    string
		from    TradingDay
		cov     []SpanStatus
		hasBars func(Span) (map[TradingDay]bool, error)
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
			t.Errorf("两条用例把【期望】写成了同一类 %s：%q 与 %q\n"+
				"  ⇒ 这不是实现出错（那由上面的 Kind 断言挡），是【期望表退化了】：\n"+
				"     有人把一条红了的用例的期望改成另一类「让它过」，\n"+
				"     于是每条 per-input 都绿，而六类不再覆盖六类", c.want, prev, c.name)
		}
		got[c.want] = c.name
	}

	all := []GapKind{GapNeverFetched, GapConfirmedEmpty, GapNotTrading,
		GapCalendarUnknown, GapStoreUnverified, GapStoreLegacy}
	for _, k := range all {
		if _, ok := got[k]; !ok {
			t.Errorf("%s 没有任何一条用例【期望】它 —— 六类里少了一类的覆盖", k)
		}
	}
	if len(got) != len(all) {
		t.Errorf("期望里出现了 %d 类，而六类全集是 %d 类", len(got), len(all))
	}
}

// checkShape 断言的是【对任何输入都成立】的那几条，不是某几条样例。
//
// ⛔ 它收 hasBars，是为了守住第三条性质：**有数据的天不许落进任何一段**。
// 那一条此前**没有任何守卫**（评审方 2026-09-09 用突变量出来的，我复现一致）：
//
//	突变  把合并条件里的 `out[n-1].To == natPrev(d)` 去掉（同类就合，不管隔没隔天）
//	⇒ **全仓 0 条红**
//	而行为实测已经变了：「确认没有·有数据·确认没有」从 2 段变成
//	**1 段 2020-01-06..08 拉过确认没有** —— 它**吞掉了 01-07，而那天有数据**
//
// ⇒ 那是本仓分类法里最坏的一格：**一个「拉过、确认没有」的区间跨过了一个有数据的日子。**
// ⇒ 成因是方向：对照组 C 测的是「该合的没合」，**「不该合的合了」这一半没有测** ——
// 与当年 ErrSinaDisagreesWithCalendar「只查一向」同形。
func checkShape(t *testing.T, from, to TradingDay, gaps []Gap, hasBars func(TradingDay) (bool, error)) {
	t.Helper()
	for _, g := range gaps {
		for d := g.From; d <= g.To; d = natNext(d) {
			has, err := hasBars(d)
			if err == nil && has {
				t.Errorf("段 %s 里包含了 %s，而那天【有数据】——\n"+
					"  ⇒ 有数据的天必须断开前后的段，不许被吞进任何一段", g, d)
			}
		}
	}
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

	gaps, err := PlanGaps(cal, testKey, 20200103, 20200113, cov, byDay(has))
	if err != nil {
		t.Fatalf("不该出错：%v", err)
	}
	checkShape(t, 20200103, 20200113, gaps, has)

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

// TestPlanGapsDoesNotSwallowADayWithData 有数据的天必须【断开】前后的段。
//
// ⛔ **这一条要的输入是「同类·有数据·同类」，而不是随便一个跨类的区间** ——
// 我第一版补了断言（checkShape 收 hasBars）却没补输入，于是评审方那个突变
// （去掉合并条件里的自然日相邻）**仍然全绿**：
//
//	TestPlanGapsPartitionsTheRange 里 01-06 有数据，可它两侧是【不同类】
//	（日历答不了 / 拉过确认没有）⇒ 本来就不会合 ⇒ **那条断言一次都没被走到**
//
// ⇒ 这正是评审方给的那条方法，而我先只学了一半：
// **突变后输出没变，先别下结论 —— 问「我这个输入走到那一行了吗」；
// 造输入要从【那一行的成立条件】倒推，不要从典型场景正推。**
func TestPlanGapsDoesNotSwallowADayWithData(t *testing.T) {
	cal := week()
	cov := []SpanStatus{{Span: Span{From: 20200106, To: 20200110}}}
	has := func(d TradingDay) (bool, error) { return d == 20200107, nil }

	gaps, err := PlanGaps(cal, testKey, 20200106, 20200108, cov, byDay(has))
	if err != nil {
		t.Fatalf("不该出错：%v", err)
	}
	checkShape(t, 20200106, 20200108, gaps, has)

	want := []Gap{
		{20200106, 20200106, GapConfirmedEmpty},
		{20200108, 20200108, GapConfirmedEmpty},
	}
	if len(gaps) != 2 || gaps[0] != want[0] || gaps[1] != want[1] {
		t.Fatalf("期望 %v，实得 %v\n"+
			"  ⇒ 合成 1 段 = 一个【拉过、确认没有】的区间跨过了一个【有数据】的日子，\n"+
			"     那是本仓分类法里最坏的一格", want, gaps)
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
	checkShape(t, 20200106, 20200112, gaps, func(TradingDay) (bool, error) { return false, nil })
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
		hasBars func(Span) (map[TradingDay]bool, error)
	}{
		{"daysWithBars 报错（.dat 读坏了）",
			[]SpanStatus{{Span: Span{From: 20200106, To: 20200110}}},
			func(Span) (map[TradingDay]bool, error) { return nil, boom }},
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
		// ⛔ 2020-02-30 不存在。Valid() 只做粗筛会放行它，而本层按自然日铺开：
		// natNext(20200230) = 2020-03-02 ⇒ **真实的 2020-03-01 一次都没被分类**，
		// 而输出里还带着一个「2020-02-30」流进报告。**静默跳过一天。**
		{"端点是个不存在的日子（2 月 30）", func() ([]Gap, error) {
			return PlanGaps(week(), testKey, 20200230, 20200302, nil, always(true))
		}},
		{"末端是个不存在的日子", func() ([]Gap, error) {
			return PlanGaps(week(), testKey, 20200106, 20200631, nil, always(true))
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
