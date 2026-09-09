package tickflow_test

import (
	"testing"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
)

// actualNight 拿【接口】问出那一天实际开了多少分钟夜盘。
//
// ⚠️ 只用 Calendar 的公开方法（DayOf / Template）＋ Day.TemplateMismatch ——
// **不看 embedded 的内部结构**。这一点是下面那条测试成立的前提：
// 它要证的是一个【跨实现】的断言，所以证据也必须只用接口。
func actualNight(t *testing.T, cal tickflow.Calendar, k tickflow.ProductKey, n tickflow.TradingDay) int {
	t.Helper()
	d, err := cal.DayOf(k, n)
	if err != nil {
		t.Fatalf("DayOf(%s)：%v", n, err)
	}
	tmpl, err := cal.Template(k, n)
	if err != nil {
		t.Fatalf("Template(%s)：%v", n, err)
	}
	_, act, _ := d.TemplateMismatch(tmpl)
	return act
}

// TestNightArtifactFollowsCoversFrom SYN-9 的证据：**假象跟着 `Covers().from` 走。**
//
// ⛔ **两份日历，两个起点** —— 这是这条测试成立的关键：
// 一份日历只能证明「08-06 那天 actual=0」，**证不了它是【因为它是覆盖首日】**。
// 换个起点再看一次，假象跟着移，才排除了「就是那一天特殊」。
// （评审方 2026-09-09 用的正是这个变量法；他指出一份日历「只证了在那一天」。）
//
// ⚠️ 而它比「夜盘挂在前一天，所以覆盖首日没有夜盘」那个【论证】硬一格：
//
//	论证    说明【为什么】会这样 —— 而它依赖对 embedded 内部结构的理解
//	变量法  说明【它确实这样】   —— 只用接口，不看实现
//	⇒ 对「这是跨实现的性质」这个断言，变量法是【直接证据】，论证只是【解释】
func TestNightArtifactFollowsCoversFrom(t *testing.T) {
	// 08-06/07/10/11 都是交易日；SHFE.rb 标称夜盘 120 分钟。
	calA := mustCal(t, 20200806, 20200807, 20200810, 20200811)
	calB := mustCal(t, 20200807, 20200810, 20200811)

	for _, c := range []struct {
		name string
		cal  tickflow.Calendar
		want tickflow.TradingDay // 期望「假象」落在哪一天
		rest []tickflow.TradingDay
	}{
		{"起点 08-06", calA, 20200806, []tickflow.TradingDay{20200807, 20200810, 20200811}},
		{"起点 08-07", calB, 20200807, []tickflow.TradingDay{20200810, 20200811}},
	} {
		t.Run(c.name, func(t *testing.T) {
			cf, _, ok := c.cal.Covers(key)
			if !ok {
				t.Fatal("这份日历该覆盖得了 SHFE.rb —— 前提没成立")
			}
			if cf != c.want {
				t.Fatalf("Covers().from = %s，而这条用例是按 %s 写的 —— 前提变了", cf, c.want)
			}
			if got := actualNight(t, c.cal, key, cf); got != 0 {
				t.Errorf("覆盖首日 %s 的实际夜盘 = %d，期望 0 —— 假象不在这里了", cf, got)
			}
			for _, n := range c.rest {
				if got := actualNight(t, c.cal, key, n); got == 0 {
					t.Errorf("%s 的实际夜盘 = 0，而它不是覆盖首日 ——\n"+
						"  ⇒ 假象不止一天的话，「排除 Covers().from」就不够了", n)
				}
			}
		})
	}

	// ⛔ 合起来才是那个断言：**同一天 08-07，在两份日历上给出相反的读数。**
	// 这一条单独写出来，因为上面两个子测试各自都可能通过而这一条仍然不成立
	// （比如实现改成「排除头两天」时）。
	if a, b := actualNight(t, calA, key, 20200807), actualNight(t, calB, key, 20200807); a == 0 || b != 0 {
		t.Errorf("08-07 在 calA（非首日）应有夜盘、在 calB（首日）应为 0，实得 %d / %d\n"+
			"  ⇒ 这一条不成立的话，「假象跟着 Covers().from 走」就没有证据", a, b)
	}
}

// TestScanNightAbsentOnRealCalendarIsNotApplicableForCFFEX 真日历上的「不适用」那一格。
//
// 实测：`CFFEX.IF` 的标称夜盘就是 **0** ⇒ 它从来没有过夜盘
// ⇒ 照直报的话它的**每一天**都是「无夜盘」，一报报整段历史。
// 而 SYN-7 防的是「本来有夜盘、后来永久取消」—— 标称为 0 的品种没有那个失效模式。
func TestScanNightAbsentOnRealCalendarIsNotApplicableForCFFEX(t *testing.T) {
	days := []tickflow.TradingDay{20200806, 20200807, 20200810, 20200811}
	cal := mustCal(t, days...)

	ifKey := tickflow.ProductKey{Exchange: "CFFEX", Product: "IF"}
	tmpl, err := cal.Template(ifKey, days[0])
	if err != nil {
		t.Fatalf("Template：%v", err)
	}
	if tmpl.NightMinutes() != 0 {
		t.Fatalf("CFFEX.IF 的标称夜盘实得 %d，而这条用例是按 0 写的 —— 前提变了，重新挑品种",
			tmpl.NightMinutes())
	}
	// 前提再确认一次：它每一天的实际夜盘都是 0（不然「不适用」这一格就没有意义）。
	for _, n := range days {
		if got := actualNight(t, cal, ifKey, n); got != 0 {
			t.Fatalf("CFFEX.IF %s 实际夜盘 = %d，期望 0 —— 前提没成立", n, got)
		}
	}

	got, ok, err := tickflow.ScanNightAbsent(cal, ifKey, days)
	if err != nil {
		t.Fatalf("不该出错：%v", err)
	}
	if ok {
		t.Fatalf("CFFEX.IF 应当【不适用】，而它报 ok=true：%s\n"+
			"  ⇒ 适用的话，它的整段历史都会被报成「连续无夜盘」——"+
			"一份全是噪声的告警等于没有告警", got)
	}

	// 对照：同一份日历上的 SHFE.rb 标称 120 ⇒ **适用**。
	// ⛔ 少了这一条，上面那个 ok=false 也可能是「函数对谁都不适用」。
	if _, ok, err := tickflow.ScanNightAbsent(cal, key, days); err != nil || !ok {
		t.Errorf("SHFE.rb 标称夜盘 120，应当适用，实得 ok=%v err=%v\n"+
			"  ⇒ 若两个品种都不适用，那不是「按标称分」，是这条判据整个没生效", ok, err)
	}
}
