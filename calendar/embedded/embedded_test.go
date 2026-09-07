package embedded

import (
	"testing"
	"time"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
)

// 2026-09 的一小段真实交易日（周四、周五、下周一——中间隔着周末）。
var days = []tickflow.TradingDay{20260903, 20260904, 20260907, 20260908}

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

// TestFridayNightBelongsToMonday 是「交易日 ≠ 自然日」的直接断言。
//
// 周一（09-07）这个交易日的第一段，落在【上周五 09-04】的 21:00。
// 一个按自然日组装的实现会把它放到周日晚上或干脆没有——
// 而那会让结算错位一天，且不报错。
func TestFridayNightBelongsToMonday(t *testing.T) {
	c := newCal(t)
	k := tickflow.ProductKey{Exchange: tickflow.SHFE, Product: "rb"}

	mon, ok := c.DayOf(k, 20260907)
	if !ok {
		t.Fatal("取不到周一的交易日")
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

	// 反向：周五晚 22:00 这一刻，属于【周一】那个交易日
	at, ok := c.DayAt(k, mustTS("2026-09-04 22:00"))
	if !ok {
		t.Fatal("周五 22:00 应当落在某个交易日内")
	}
	if at.Num != 20260907 {
		t.Errorf("周五 22:00 属于交易日 %d，期望 20260907", int32(at.Num))
	}
}

// TestDayAtRejectsClosedTime 休市时刻必须 ok=false，不能就近吸附到某一天。
func TestDayAtRejectsClosedTime(t *testing.T) {
	c := newCal(t)
	k := tickflow.ProductKey{Exchange: tickflow.SHFE, Product: "rb"}
	for _, s := range []string{
		"2026-09-04 12:00", // 午休
		"2026-09-04 10:20", // 上午休市段
		"2026-09-05 10:00", // 周六
		"2026-09-04 20:00", // 夜盘开盘前
	} {
		if d, ok := c.DayAt(k, mustTS(s)); ok {
			t.Errorf("%s 是休市时刻，却被判进交易日 %d", s, int32(d.Num))
		}
	}
}

// TestTemplateRefusesBeforeBaseFrom 生效起点之前【没有依据】，必须 ok=false。
//
// 拿当前模板去顶替历史是错的：实测 rb1605（2016 上半年）每交易日约 445 分钟，
// 而 rb1801 约 345 分钟——夜盘长度差了近一倍。
func TestTemplateRefusesBeforeBaseFrom(t *testing.T) {
	k := tickflow.ProductKey{Exchange: tickflow.SHFE, Product: "rb"}
	if _, ok := Template(k, 20160104); ok {
		t.Error("2016 年没有依据，应当 ok=false 而不是拿当前模板顶替")
	}
	if _, ok := Template(k, 20260907); !ok {
		t.Error("2026 年应当有模板")
	}
}

// TestTemplateRefusesUnknownProduct 没收录的品种同样说不知道。
func TestTemplateRefusesUnknownProduct(t *testing.T) {
	for _, k := range []tickflow.ProductKey{
		{Exchange: tickflow.SHFE, Product: "zzz"},
		{Exchange: tickflow.CFFEX, Product: "XX"},
	} {
		if _, ok := Template(k, 20260907); ok {
			t.Errorf("%v 没收录，应当 ok=false", k)
		}
	}
}

// TestNightLengthsByProduct 三档夜盘长度，以及广期所无夜盘。
func TestNightLengthsByProduct(t *testing.T) {
	cases := []struct {
		k    tickflow.ProductKey
		mins int
	}{
		{tickflow.ProductKey{Exchange: tickflow.SHFE, Product: "ag"}, 330},
		{tickflow.ProductKey{Exchange: tickflow.SHFE, Product: "au"}, 330},
		{tickflow.ProductKey{Exchange: tickflow.INE, Product: "sc"}, 330},
		{tickflow.ProductKey{Exchange: tickflow.SHFE, Product: "cu"}, 240},
		{tickflow.ProductKey{Exchange: tickflow.SHFE, Product: "rb"}, 120},
		{tickflow.ProductKey{Exchange: tickflow.GFEX, Product: "si"}, 0}, // 实测无夜盘
		{tickflow.ProductKey{Exchange: tickflow.GFEX, Product: "lc"}, 0},
		{tickflow.ProductKey{Exchange: tickflow.GFEX, Product: "ps"}, 0},
		{tickflow.ProductKey{Exchange: tickflow.CFFEX, Product: "IF"}, 0},
		{tickflow.ProductKey{Exchange: tickflow.CFFEX, Product: "T"}, 0},
	}
	for _, c := range cases {
		tm, ok := Template(c.k, 20260907)
		if !ok {
			t.Fatalf("%v 没有模板", c.k)
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
	comm, _ := Template(tickflow.ProductKey{Exchange: tickflow.SHFE, Product: "rb"}, 20260907)

	if idx.DayMinutes() == comm.DayMinutes() && len(idx.Day) == len(comm.Day) {
		t.Error("股指日盘不该与商品相同")
	}
	if bond.DayMinutes() == idx.DayMinutes() {
		t.Errorf("国债(%d 分)与股指(%d 分)日盘不该相同",
			bond.DayMinutes(), idx.DayMinutes())
	}
	// 国债 09:15–11:30 + 13:00–15:15 = 135 + 135 = 270
	if got := bond.DayMinutes(); got != 270 {
		t.Errorf("国债日盘 %d 分，期望 270", got)
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
		d, ok := c.DayOf(k, 20260907)
		if !ok {
			t.Fatalf("%v 取不到交易日", k)
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
	k := tickflow.ProductKey{Exchange: tickflow.SHFE, Product: "rb"}
	var seen []tickflow.TradingDay
	if err := c.Walk(k, 20260904, 20260908, func(d tickflow.Day) bool {
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
	// 中断
	n := 0
	c.Walk(k, 20260903, 20260908, func(tickflow.Day) bool { n++; return false })
	if n != 1 {
		t.Errorf("返回 false 应当立刻停止，实际走了 %d 步", n)
	}
	if err := c.Walk(k, 20260908, 20260903, nil); err == nil {
		t.Error("from 晚于 to 应当报错")
	}
}

// Calendar 必须满足 tickflow.Calendar 接口。
var _ tickflow.Calendar = (*Calendar)(nil)

func mustTS(s string) int64 {
	t, err := time.ParseInLocation("2006-01-02 15:04", s, tickflow.CST)
	if err != nil {
		panic(err)
	}
	return t.UnixMilli()
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
	k := tickflow.ProductKey{Exchange: tickflow.SHFE, Product: "rb"}
	d, ok := c.DayOf(k, 20261009)
	if !ok {
		t.Fatal("取不到节后第一个交易日")
	}
	if got := hhmm(d.Sessions[0].Start); got != "09-30 21:00" {
		t.Fatalf("内置实现【应当】给出这段并不存在的夜盘（已知缺陷），得到 %s；"+
			"若这是 calendar/derived 修好的结果，请一并更新 DayOf 文档与 contract.md",
			got)
	}
}
