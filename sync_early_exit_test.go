package tickflow

import (
	"context"
	"testing"

	"github.com/dream-until-dawn/futures-tickflow-go/source/pacing"
)

// 本文件钉住 `Sync` 的**三条非错误早退**，而它们的处置【不一样】。
//
// 🔴 **三条不是两条** —— 我第一版只列了两条，第三条（`!found`）的哑是**故意留的**，
// 照单具名化会把它一起弄绿（评审方 2026-09-10 指出，我数过：确实三条）。
//
// ⇒ 判据：**给一族路径统一具名之前，先数清这一族有几条，
// 并逐条问「它现在的哑，是不是有人故意留的」。**
// ⚠️ 而「问」的第一步是看**有没有人写过** —— 三条里当时是
// 「有落款 / 有半句 / **一句都没有**」。**哑有三种状态，不是两种。**

// TestNoTradingDaysIsAResultNotAnIncident 区间里一个交易日都没有 ⇒ 结果，不是异常。
//
// ⛔ 而它**先落成事实、再谈总状态**，顺序不能反：
// `Gaps` 里那一段写着「不是交易日」，`Complete()` 才配为真。
// **只改 Complete() 而不加载体，那一步是【放宽】。**
func TestNoTradingDaysIsAResultNotAnIncident(t *testing.T) {
	h := newHarness(t, pacing.NoPacing(), 1, 0)
	r := req(0)
	r.From, r.To = 20200111, 20200112 // 周六、周日，都在覆盖内

	rep, err := h.syn.Sync(context.Background(), r, 0)
	if err != nil {
		t.Fatalf("没什么可同步不是错误：%v", err)
	}
	if rep.Halt != HaltNoTradingDays {
		t.Errorf("Halt = %v，期望 HaltNoTradingDays", rep.Halt)
	}
	// ⛔ 先看事实：那一段必须**在报告里说出来**。
	if len(rep.Gaps) == 0 {
		t.Fatal("一段缺口都没报 —— 而这两天不交易，那是一个【日历给得出的确定答案】；\n" +
			"  ⇒ 少了这一格，Complete()=true 就是【放宽】不是【自洽】")
	}
	for _, g := range rep.Gaps {
		if g.Kind != GapNotTrading {
			t.Errorf("缺口 %s 的类别是 %v，期望「不是交易日」", g, g.Kind)
		}
	}
	if !rep.Complete() {
		t.Errorf("事实已经落进 Gaps 了，而报告仍不 Complete：%s", rep)
	}
	if rep.Bars != 0 {
		t.Errorf("不该同步到任何根，实得 %d", rep.Bars)
	}
}

// TestOutsideCoverageIsAResultNotAnIncident 请求整段落在日历覆盖之外 ⇒ 同上。
//
// ⛔ **两个方向都测**（之前 / 之后）—— 只测一侧的话，一个「只处理了之前那一支」
// 的实现会绿一半，而那正是 `hi < from` 这个判据同时接住两侧的地方。
func TestOutsideCoverageIsAResultNotAnIncident(t *testing.T) {
	for _, c := range []struct {
		name     string
		from, to TradingDay
	}{
		{"整段在覆盖之前", 20190101, 20190102},
		{"整段在覆盖之后", 20210101, 20210102},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, pacing.NoPacing(), 1, 0)
			r := req(0)
			r.From, r.To = c.from, c.to

			rep, err := h.syn.Sync(context.Background(), r, 0)
			if err != nil {
				t.Fatalf("日历答不了这一段，不是错误：%v", err)
			}
			if rep.Halt != HaltOutsideCoverage {
				t.Errorf("Halt = %v，期望 HaltOutsideCoverage", rep.Halt)
			}
			if len(rep.Gaps) == 0 {
				t.Fatal("一段缺口都没报 —— 而 GapCalendarUnknown 的定义里逐字写着\n" +
					"  「或日期在 Covers 之外」：【槽位一直都在，是这条早退跳过了 PlanGaps】")
			}
			for _, g := range rep.Gaps {
				if g.Kind != GapCalendarUnknown {
					t.Errorf("缺口 %s 的类别是 %v，期望「日历答不了」", g, g.Kind)
				}
			}
			if !rep.Complete() {
				t.Errorf("事实已经落进 Gaps 了，而报告仍不 Complete：%s", rep)
			}
		})
	}
}

// TestNotYetClosedKeepsItsDeliberateSilence 第三条早退**保持它的哑**，而这是钉住的。
//
// 🔴 **它存在的理由是防一次「统一这一族」** ——
// 另外两条已经具名化并变绿了，下一个人很容易把这一条也扫进去。
// 而它与那两条**不同类**：
//
//	另外两条  「没东西可同步」是**日历给的确定答案** ⇒ 落得成事实 ⇒ 可以不留声
//	这一条    「现在还答不了，等收盘」是一个**时刻**问题 ——
//	          下一分钟同样的请求可能就有答案了；而这里连 `To` 都定不下来，
//	          **没有区间可以交给 PlanGaps** ⇒ 它连「落成事实」这一步都做不到
//
// ⇒ 所以它只能留声。**这条测试就是那个决定的载体** ——
// 有人把它一起具名化的那天，这里会红。
func TestNotYetClosedKeepsItsDeliberateSilence(t *testing.T) {
	h := newHarness(t, pacing.NoPacing(), 1, 0)
	r := req(0)
	r.To = 0 // 让 Sync 自己定末端

	// now = 0 ⇒ 覆盖里没有任何一天已经收盘。
	rep, err := h.syn.Sync(context.Background(), r, 0)
	if err != nil {
		t.Fatalf("「一天都还没收盘」不是错误：%v", err)
	}
	// 前提：确实走到了那条路（`To` 没定下来 ⇒ Requested 的末端是 0）。
	if rep.Requested[1] != 0 {
		t.Fatalf("前提没成立：Requested 末端是 %s，而这一格要的是那条『定不了末端』的路",
			rep.Requested[1])
	}
	if rep.Halt != HaltUnknown {
		t.Errorf("Halt = %v，期望 HaltUnknown ——\n"+
			"  ⇒ 这一条的哑是【故意】的：它答不了，而不是「答案是没有」。\n"+
			"  ⇒ 若你是在「把三条早退统一具名化」，请先读这条测试顶上的理由", rep.Halt)
	}
	if rep.Complete() {
		t.Error("一份「现在还答不了」的报告说 Complete() —— 它会被读成「跑完了」")
	}
}
