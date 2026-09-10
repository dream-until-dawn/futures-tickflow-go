package tickflow_test

import (
	"testing"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/embedded"
	"github.com/dream-until-dawn/futures-tickflow-go/source/cffexsource"
	"github.com/dream-until-dawn/futures-tickflow-go/source/sinasource"
)

// guard: ㉒ 那条到期条件 —— 到期那天它自己红。
// TestHasBarsExpiryConditionNotYetDue 把 `HasBars` 那条到期条件从
// **只有人能跑**变成**会红的测试**。
//
// 被钉住的那句话（v0.4.0 注解 二②）：
//
//	`HasBars` 是 O(天数 × 根数)
//	到期条件：**出现第一个 `Caps().Periods` 含日内周期的源之前**
//
// ⛔ 它是 v0.4.0 注解里三条到期条件中**唯一没有机器求值法**的那一条
// （原来写的是「**读** source/ 下各源的 Periods 声明」）——
// 而它的失败方向与①同型：新加一个源、`Caps().Periods` 含日内周期
// ⇒ **不会有任何东西响**，要等有人想起来去读。
// （评审方 2026-09-10 在 v0.4 末检里指出；而让这个不齐一眼看得见的，
//
//	是注解把三条**并排**写了出来 —— **并排写本身就是一次检查**。）
//
// —— 这条测试判的是【甲】不是【乙】，而两者的差别要写清 ——
//
//	甲：有源**声明**了含日内周期的 `Caps().Periods`     ← 本测试判这个
//	乙：有源**实际能给**日内数据                        ← 才是那个复杂度真正被引爆的条件
//
// 选甲的理由三条：**可机器判**（②今天的毛病正是只有人能跑）·
// **失败方向是早报**（早报会把人叫来）· **甲是乙的必要条件**。
//
// ⛔ 而第三条**有一个前提，写在这儿**（评审方要求，我同意）：
// **它依赖「本仓只请求 `Caps()` 声明过的周期」。**
// 那个前提哪天不成立（有人绕过 `Caps` 直接取日内数据），
// 甲就不再是乙的必要条件 ⇒ **这条到期条件会【安静地】不响**。
// ⇒ 那一天要改的不是这条测试的阈值，是它判的那个命题。
//
// ⚠️ 而「早报会被当成噪音」那条本仓规矩（**一个长期误报的告警最终会关掉它自己**）
// **在这里不适用**：甲只在「有人新加一个日内源」时才响，
// 那是**罕见且总是值得看一眼**的事件。挑早报的前提是误报率低，这里满足。
func TestHasBarsExpiryConditionNotYetDue(t *testing.T) {
	cal := testCalendarForCaps(t)

	// ⚠️ 这张表是**手写**的 —— 本仓今天没有「所有源」的注册表。
	// ⇒ 于是它有一个已知的失败方向：**新加一个源而忘了加进这张表**，
	//   这条测试**不会响**。这一句写在这儿，因为它就是这条测试的射程边界。
	//   （而它比原来那句「读 source/ 下各源的声明」强一格：那一条连表都没有。）
	sources := []struct {
		name string
		caps func(tickflow.ProductKey) tickflow.Capabilities
	}{
		{"sinasource", newSinaForCaps(t, cal).Caps},
		{"cffexsource", newCffexForCaps(t, cal).Caps},
	}

	products := embedded.Products()
	// 前提自检：尺子不能是空转的 —— 没有品种时下面的循环一格都不跑，而它照样绿。
	if len(products) == 0 {
		t.Fatal("一个品种都没有 —— 这条测试会空转，读数作废")
	}
	if len(sources) == 0 {
		t.Fatal("一个源都没有 —— 同上")
	}

	checked := 0
	for _, s := range sources {
		sawAny := false
		for _, k := range products {
			for _, p := range s.caps(k).Periods {
				sawAny = true
				checked++
				if _, isIntraday := p.(tickflow.IntradayPeriod); isIntraday {
					t.Fatalf("源 %s 对 %v 声明了日内周期 %v —— "+
						"【㉒ 那条到期条件到期了】。\n"+
						"处置不是把这条测试改掉，是：\n"+
						"  一、去看 HasBars 的 O(天数 × 根数)：日内落库之后它按最坏档约 1.9 小时/次\n"+
						"  二、决定是换算法还是加缓存，并把决定写进 docs/design.md\n"+
						"  三、再回来改这条测试与 v0.4.0 注解里那条到期条件的后继",
						s.name, k, p)
				}
			}
		}
		// 前提自检：一个源若一个周期都不声明，上面的循环什么也没检 —— 那不是「没到期」。
		if !sawAny {
			t.Fatalf("源 %s 对所有品种都没有声明任何周期 —— "+
				"这条测试在它身上是空转的，读数作废", s.name)
		}
	}
	t.Logf("检查了 %d 个（源 × 品种 × 周期）组合，没有日内周期", checked)
}

func testCalendarForCaps(t *testing.T) tickflow.Calendar {
	t.Helper()
	// 两天足够 —— 这条测试只问 Caps()，不走日历上的任何一天。
	// ⚠️ 手列，不从别处抽 —— 同本仓 cffexsource 那条：拿数据造日历再用它验那份数据是循环论证。
	days := []tickflow.TradingDay{20260909, 20260910}
	cal, err := embedded.New(days)
	if err != nil {
		t.Fatalf("构造日历失败：%v", err)
	}
	return cal
}

func newSinaForCaps(t *testing.T, cal tickflow.Calendar) *sinasource.Client {
	t.Helper()
	c, err := sinasource.New(cal)
	if err != nil {
		t.Fatalf("构造 sinasource 失败：%v", err)
	}
	return c
}

func newCffexForCaps(t *testing.T, cal tickflow.Calendar) *cffexsource.Client {
	t.Helper()
	c, err := cffexsource.New(cal)
	if err != nil {
		t.Fatalf("构造 cffexsource 失败：%v", err)
	}
	return c
}
