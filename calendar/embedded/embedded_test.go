package embedded

import (
	"errors"
	"strings"
	"testing"
	"time"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
)

// 2026-09 的一小段真实交易日（周四、周五、下周一——中间隔着周末）。
var days = []tickflow.TradingDay{20260903, 20260904, 20260907, 20260908}

var rb = tickflow.ProductKey{Exchange: tickflow.SHFE, Product: "rb"}

func newCal(t *testing.T) *Calendar {
	t.Helper()
	c, err := New(days)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func hhmm(ms int64) string {
	return time.UnixMilli(ms).In(tickflow.CST).Format("01-02 15:04")
}

func mustTS(s string) int64 {
	t, err := time.ParseInLocation("2006-01-02 15:04", s, tickflow.CST)
	if err != nil {
		panic(err)
	}
	return t.UnixMilli()
}

// Calendar 必须满足 tickflow.Calendar 接口。
var _ tickflow.Calendar = (*Calendar)(nil)

// TestNewRefusesToGuessTradingDays 不给交易日就报错，**不从工作日近似**。
//
// 用工作日近似会在每个长假前后错一次——而那正是保证金上调的时候。
// 报错比给一个看起来合理的近似强。
func TestNewRefusesToGuessTradingDays(t *testing.T) {
	if _, err := New(nil); err == nil {
		t.Fatal("空交易日列表应当报错")
	}
	if _, err := New([]tickflow.TradingDay{0}); err == nil {
		t.Fatal("非法交易日应当报错")
	}
}

// TestUncoveredIsDistinguishableFromNoTrading 是这一组里最要紧的一条。
//
// 「日历答不了」与「那天不是交易日」**必须分得开**。
//
// v0.1.0 里两者都是 ok=false，后果很具体：内置模板自 2020-05-06 生效，
// 而新浪 RB0 日线自 2009-03-27 起——**中间十一年日历全答「不知道」**，
// 上层读成「不是交易日」就会静默跳过日线最值钱的那一段，且不会自愈。
//
// 这条测试的存在，是为了让下一次重构没法把它们合回去而不出声。
func TestUncoveredIsDistinguishableFromNoTrading(t *testing.T) {
	c := newCal(t)
	for _, cs := range []struct {
		what string
		k    tickflow.ProductKey
		num  tickflow.TradingDay
		want error
	}{
		{"周六——日历知道，那天不交易", rb, 20260905, tickflow.ErrNotTradingDay},
		{"2016 年——早于生效起点，答不了", rb, 20160104, tickflow.ErrUncovered},
		{"没收录的品种——答不了",
			tickflow.ProductKey{Exchange: tickflow.SHFE, Product: "zzz"}, 20260907,
			tickflow.ErrUncovered},
	} {
		_, err := c.DayOf(cs.k, cs.num)
		if err == nil {
			t.Errorf("%s：应当报错", cs.what)
			continue
		}
		if !errors.Is(err, cs.want) {
			t.Errorf("%s：得到 %v，期望 %v", cs.what, err, cs.want)
		}
	}
	// 反向：这两种原因【绝不能】互相包含，否则上面三条一起退化成「反正都报错」，
	// 而调用方分不出该重拉还是该跳过。
	if errors.Is(tickflow.ErrNotTradingDay, tickflow.ErrUncovered) ||
		errors.Is(tickflow.ErrUncovered, tickflow.ErrNotTradingDay) {
		t.Fatal("「不是交易日」与「答不了」不能是同一个错误")
	}
	if _, err := c.DayOf(rb, 20260907); err != nil {
		t.Errorf("正常交易日不该报错: %v", err)
	}
}

// TestWalkRefusesOutOfCoverage Walk 是这一组里【唯一真正的护栏】。
//
// 哨兵错误只负责把话说清楚——`if err != nil { continue }` 一行就能把三种一起吞掉，
// 成本和 `if !ok { continue }` 一模一样。真正拦住「静默少同步十一年」的，
// 是把检查放在【循环所在的地方】：上层最自然的写法就是把整个请求区间交给 Walk。
func TestWalkRefusesOutOfCoverage(t *testing.T) {
	c := newCal(t)
	err := c.Walk(rb, 20090327, 20260908, func(tickflow.Day) bool { return true })
	if err == nil {
		t.Fatal("区间超出覆盖时必须报错，不能静默少遍历")
	}
	if !errors.Is(err, tickflow.ErrUncovered) {
		t.Errorf("应当是 ErrUncovered，得到 %v", err)
	}
	// 错误信息要说出【覆盖到哪】，否则调用方只知道错了、不知道边界在哪
	for _, want := range []string{"2009-03-27", "2026-09-03", "2026-09-08"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("错误信息应含 %q，得到 %v", want, err)
		}
	}
	n := 0
	if err := c.Walk(rb, 20260903, 20260908,
		func(tickflow.Day) bool { n++; return true }); err != nil {
		t.Fatalf("覆盖内的区间不该报错: %v", err)
	}
	if n != 4 {
		t.Errorf("应当走 4 天，实际 %d", n)
	}
	// 未收录品种：整个答不了。
	//
	// ⚠️ 这里【必须】比对措辞，不能只比 errors.Is(err, ErrUncovered)——
	// 品种没收录时 Covers 返回 (0, 0, false)，于是下面那个区间检查
	// （from < 0 || to > 0）也会成立，**同样吐出一个 ErrUncovered**。
	// 只断言错误类型的话，把品种检查整个删掉测试照样全绿：
	// 两条路给出同一个可观察量，测试就分不出哪条在承重。
	err = c.Walk(tickflow.ProductKey{Exchange: tickflow.SHFE, Product: "zzz"},
		20260903, 20260908, func(tickflow.Day) bool { return true })
	if !errors.Is(err, tickflow.ErrUncovered) {
		t.Errorf("未收录品种应当 ErrUncovered，得到 %v", err)
	} else if !strings.Contains(err.Error(), "未收录品种") {
		t.Errorf("应当说清是【品种没收录】而不是区间超界，得到 %v", err)
	}
}

// TestCovers 覆盖区间取「注入的交易日」与「模板生效起点」的交集。
func TestCovers(t *testing.T) {
	c := newCal(t)
	from, to, ok := c.Covers(rb)
	if !ok || from != 20260903 || to != 20260908 {
		t.Errorf("Covers = (%s, %s, %v)，期望 (2026-09-03, 2026-09-08, true)", from, to, ok)
	}
	if _, _, ok := c.Covers(tickflow.ProductKey{Exchange: tickflow.SHFE,
		Product: "zzz"}); ok {
		t.Error("未收录品种应当 ok=false")
	}
	// 注入的交易日早于 baseFrom 时，覆盖起点要抬到 baseFrom 之后的第一天——
	// 否则 Covers 会承诺一段它其实答不了的区间，而 Walk 就是照它放行的。
	old, err := New([]tickflow.TradingDay{20160104, 20160105, 20260907})
	if err != nil {
		t.Fatal(err)
	}
	if from, _, _ := old.Covers(rb); from != 20260907 {
		t.Errorf("baseFrom 之前的交易日不该算进覆盖，得到起点 %s", from)
	}
	if err := old.Walk(rb, 20160104, 20260907,
		func(tickflow.Day) bool { return true }); !errors.Is(err, tickflow.ErrUncovered) {
		t.Errorf("请求含 baseFrom 之前的区间应当被拒，得到 %v", err)
	}
}

// TestFridayNightBelongsToMonday 是「交易日 ≠ 自然日」的直接断言。
//
// 周一（09-07）这个交易日的第一段，落在【上周五 09-04】的 21:00。
// 一个按自然日组装的实现会把它放到周日晚上或干脆没有——
// 而那会让结算错位一天，且不报错。
func TestFridayNightBelongsToMonday(t *testing.T) {
	c := newCal(t)
	mon, err := c.DayOf(rb, 20260907)
	if err != nil {
		t.Fatal(err)
	}
	if len(mon.Sessions) != 4 {
		t.Fatalf("期望 1 段夜盘 + 3 段日盘，得到 %d 段", len(mon.Sessions))
	}
	if got := hhmm(mon.Sessions[0].Start); got != "09-04 21:00" {
		t.Errorf("周一交易日的首段应从【上周五】21:00 起，得到 %s", got)
	}
	if got := hhmm(mon.Sessions[len(mon.Sessions)-1].End); got != "09-07 15:00" {
		t.Errorf("周一交易日的末段应收在 09-07 15:00，得到 %s", got)
	}
	at, err := c.DayAt(rb, mustTS("2026-09-04 22:00"))
	if err != nil {
		t.Fatalf("周五 22:00 应当落在某个交易日内: %v", err)
	}
	if at.Num != 20260907 {
		t.Errorf("周五 22:00 属于交易日 %s，期望 2026-09-07", at.Num)
	}
}

// TestDayAtRejectsClosedTime 休市时刻必须 ErrClosed，不能就近吸附到某一天。
func TestDayAtRejectsClosedTime(t *testing.T) {
	c := newCal(t)
	for _, s := range []string{
		"2026-09-04 12:00", // 午休
		"2026-09-04 10:20", // 上午休市段
		"2026-09-05 10:00", // 周六
		"2026-09-04 20:00", // 夜盘开盘前
	} {
		d, err := c.DayAt(rb, mustTS(s))
		if err == nil {
			t.Errorf("%s 是休市时刻，却被判进交易日 %s", s, d.Num)
			continue
		}
		if !errors.Is(err, tickflow.ErrClosed) {
			t.Errorf("%s 应当 ErrClosed，得到 %v", s, err)
		}
	}
	// 未收录品种是 ErrUncovered，**不是** ErrClosed——
	// 「答不了」不该被降级成「这一刻没在交易」，那是同一个塌缩换了个说法。
	if _, err := c.DayAt(tickflow.ProductKey{Exchange: tickflow.SHFE, Product: "zzz"},
		mustTS("2026-09-07 10:00")); !errors.Is(err, tickflow.ErrUncovered) {
		t.Errorf("未收录品种应当 ErrUncovered，得到 %v", err)
	}
}

// TestTemplateRefusesBeforeBaseFrom 生效起点之前【没有依据】，必须 ErrUncovered。
//
// 拿当前模板去顶替历史是错的：实测 rb1605（2016 上半年）每交易日约 445 分钟，
// 而 rb1801 约 345 分钟——夜盘长度差了近一倍。
func TestTemplateRefusesBeforeBaseFrom(t *testing.T) {
	if _, err := Template(rb, 20160104); !errors.Is(err, tickflow.ErrUncovered) {
		t.Errorf("2016 年没有依据，应当 ErrUncovered，得到 %v", err)
	}
	if _, err := Template(rb, 20260907); err != nil {
		t.Errorf("2026 年应当有模板: %v", err)
	}
}

// TestTemplateRefusesUnknownProduct 没收录的品种同样说不知道。
func TestTemplateRefusesUnknownProduct(t *testing.T) {
	for _, k := range []tickflow.ProductKey{
		{Exchange: tickflow.SHFE, Product: "zzz"},
		{Exchange: tickflow.CFFEX, Product: "XX"},
	} {
		if _, err := Template(k, 20260907); !errors.Is(err, tickflow.ErrUncovered) {
			t.Errorf("%v 没收录，应当 ErrUncovered，得到 %v", k, err)
		}
	}
}

// TestNightLengthsByProduct 三档夜盘长度，以及广期所无夜盘。
func TestNightLengthsByProduct(t *testing.T) {
	for _, c := range []struct {
		k    tickflow.ProductKey
		mins int
	}{
		{tickflow.ProductKey{Exchange: tickflow.SHFE, Product: "ag"}, 330},
		{tickflow.ProductKey{Exchange: tickflow.SHFE, Product: "au"}, 330},
		{tickflow.ProductKey{Exchange: tickflow.INE, Product: "sc"}, 330},
		{tickflow.ProductKey{Exchange: tickflow.SHFE, Product: "cu"}, 240},
		{rb, 120},
		{tickflow.ProductKey{Exchange: tickflow.GFEX, Product: "si"}, 0}, // 实测无夜盘
		{tickflow.ProductKey{Exchange: tickflow.GFEX, Product: "lc"}, 0},
		{tickflow.ProductKey{Exchange: tickflow.GFEX, Product: "ps"}, 0},
		{tickflow.ProductKey{Exchange: tickflow.CFFEX, Product: "IF"}, 0},
		{tickflow.ProductKey{Exchange: tickflow.CFFEX, Product: "T"}, 0},
	} {
		tm, err := Template(c.k, 20260907)
		if err != nil {
			t.Fatalf("%v 没有模板: %v", c.k, err)
		}
		if got := tm.NightMinutes(); got != c.mins {
			t.Errorf("%v 夜盘 %d 分，期望 %d", c.k, got, c.mins)
		}
	}
}

// TestCffexDayDiffersFromCommodity 中金所股指与国债时段互不相同，且都异于商品。
func TestCffexDayDiffersFromCommodity(t *testing.T) {
	idx, _ := Template(tickflow.ProductKey{Exchange: tickflow.CFFEX, Product: "IF"}, 20260907)
	bond, _ := Template(tickflow.ProductKey{Exchange: tickflow.CFFEX, Product: "T"}, 20260907)
	comm, _ := Template(rb, 20260907)

	if idx.DayMinutes() == comm.DayMinutes() && len(idx.Day) == len(comm.Day) {
		t.Error("股指日盘不该与商品相同")
	}
	if bond.DayMinutes() == idx.DayMinutes() {
		t.Errorf("国债(%d 分)与股指(%d 分)日盘不该相同",
			bond.DayMinutes(), idx.DayMinutes())
	}
	// 国债 09:30–11:30 + 13:00–15:15 = 120 + 135 = 255
	//
	// 这个数曾经写的是 270（起点 09:15）——**测试把 bug 一起编码了进去**，
	// 于是它绿着守了一个错的值。实测见 dayBond 的注释。
	// ⇒ 一个从实现读出来的期望值，守的是实现而不是事实。
	if got := bond.DayMinutes(); got != 255 {
		t.Errorf("国债日盘 %d 分，期望 255（09:30–11:30 + 13:00–15:15）", got)
	}
	// 股指 09:30–11:30 + 13:00–15:00 = 120 + 120 = 240
	if got := idx.DayMinutes(); got != 240 {
		t.Errorf("股指日盘 %d 分，期望 240", got)
	}
}

// TestEndToEndGridMatchesMeasured 端到端：内置日历 → Period.Bars → 与实测标签一致。
func TestEndToEndGridMatchesMeasured(t *testing.T) {
	c := newCal(t)
	p := tickflow.MustIntraday(60)

	for _, tc := range []struct {
		prod  string
		exch  string
		want  []string
		total int // 全天根数，含夜盘——只看日盘标签会漏掉「凭空多出一段夜盘」
	}{
		{"ag", tickflow.SHFE, []string{"09:30", "10:45", "13:45", "14:45", "15:00"}, 10},
		{"cu", tickflow.SHFE, []string{"10:00", "11:15", "14:15", "15:00"}, 8},
		{"rb", tickflow.SHFE, []string{"10:00", "11:15", "14:15", "15:00"}, 6},
		{"si", tickflow.GFEX, []string{"10:00", "11:15", "14:15", "15:00"}, 4},
	} {
		k := tickflow.ProductKey{Exchange: tc.exch, Product: tc.prod}
		d, err := c.DayOf(k, 20260907)
		if err != nil {
			t.Fatalf("%v 取不到交易日: %v", k, err)
		}
		tm, _ := Template(k, 20260907)
		bars := p.Bars(tm, d)
		if len(bars) != tc.total {
			t.Errorf("%s 全天 %d 根，期望 %d 根", tc.prod, len(bars), tc.total)
		}
		var got []string
		for _, b := range bars {
			lbl := time.UnixMilli(b.Close).In(tickflow.CST).Format("15:04")
			if lbl >= "09:00" && lbl <= "15:00" {
				got = append(got, lbl)
			}
		}
		if len(got) != len(tc.want) {
			t.Errorf("%s 日盘 %v，期望 %v", tc.prod, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("%s 日盘 %v，期望 %v", tc.prod, got, tc.want)
				break
			}
		}
	}
}

// TestWalkAscending Walk 按升序、可中断。
func TestWalkAscending(t *testing.T) {
	c := newCal(t)
	var seen []tickflow.TradingDay
	if err := c.Walk(rb, 20260904, 20260908, func(d tickflow.Day) bool {
		seen = append(seen, d.Num)
		return true
	}); err != nil {
		t.Fatal(err)
	}
	want := []tickflow.TradingDay{20260904, 20260907, 20260908}
	if len(seen) != len(want) {
		t.Fatalf("Walk 得到 %v，期望 %v", seen, want)
	}
	for i := range seen {
		if seen[i] != want[i] {
			t.Fatalf("Walk 得到 %v，期望 %v", seen, want)
		}
	}
	n := 0
	c.Walk(rb, 20260903, 20260908, func(tickflow.Day) bool { n++; return false })
	if n != 1 {
		t.Errorf("返回 false 应当立刻停止，实际走了 %d 步", n)
	}
	if err := c.Walk(rb, 20260908, 20260903, nil); err == nil {
		t.Error("from 晚于 to 应当报错")
	}
}

// TestKnownDefect_EmbeddedCannotSeeSuspendedNight 钉住一条【已知缺陷】，不是期望行为。
//
// 前缀 TestKnownDefect_ 是约定：**欠条要能被一条命令列出来**，
// 否则「本仓当前钉住了几个已知缺陷」只能靠翻文件，
// 那它会和白名单走同一条路——迟早变成垃圾桶。
//
//	go test -list 'TestKnownDefect_.*' ./...
//
// 内置实现假定「实际 = 标称」：只要品种有夜盘，它就给一段夜盘。
// 但长假前最后一个交易日的夜盘是【不开】的——2026-09-30 晚上没有夜盘，
// 而这里的 20261009 交易日照样会拿到一段 09-30 21:00 的夜盘。
//
// 这条测试断言的是错误行为，目的是让它【别悄悄变】：
// v0.3 的 calendar/derived 从分钟数据反推真值修掉它时，这条会变红，
// 从而逼着改的人同时更新 DayOf 的文档与 contract.md 的限定。
//
// 相位不受影响（相位按标称算，是品种常量），受影响的是切分与判完结。
func TestKnownDefect_EmbeddedCannotSeeSuspendedNight(t *testing.T) {
	// 节前最后一个交易日 09-30，节后第一个 10-09。真实情况：09-30 晚无夜盘。
	c, err := New([]tickflow.TradingDay{20260929, 20260930, 20261009})
	if err != nil {
		t.Fatal(err)
	}
	d, err := c.DayOf(rb, 20261009)
	if err != nil {
		t.Fatalf("取不到节后第一个交易日: %v", err)
	}
	if got := hhmm(d.Sessions[0].Start); got != "09-30 21:00" {
		t.Fatalf("内置实现【应当】给出这段并不存在的夜盘（已知缺陷），得到 %s；"+
			"若这是 calendar/derived 修好的结果，"+
			"请一并更新 DayOf 文档与 contract.md", got)
	}
}
