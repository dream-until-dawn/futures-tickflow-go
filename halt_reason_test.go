package tickflow

import (
	"context"
	"errors"
	"testing"

	"github.com/dream-until-dawn/futures-tickflow-go/source/pacing"
)

// —— 这一条守的是【停因这个名字，和它旁边那些通道说的是同一件事】——
//
// ⛔ 由来（2026-09-11 实测，评审方登记为 (b)）：`HaltBudget` 一个名字盖着三条出口。
// 三条出口在调用方能读到的每一个通道上的取值，改之前是这样的：
//
//	通道                                 超预算    落盘失败    扩 coverage 失败
//	errors.Is(err, ErrBudgetExhausted)   true     **false**   **false**
//	Halt.String()                        预算耗尽  预算耗尽    预算耗尽     ← 后两个是假话
//	Incidents() 那句留声                  ——       同上        同上        ← 后两个是假话
//	盘上根数 / coverage 段数              0 / 0     0 / 0      **1 / 0**
//
// 🔴 **两个通道互相矛盾**：`errors.Is` 说「不是预算」，而 `String()` 说「预算耗尽」——
// 而读的人按后者行事：**加大预算、稍后重试**。
// 在 ≤v0.4.0 上，那个动作正是把库越弄越坏的那一个（见 v0.4.1 的注解）。
//
// ⚠️ 而分成三个名字的判据**不是「它们看起来不同」，是【处置分不分岔】**：
//
//	超预算            上游在闹脾气 ⇒ 等一等再来，是对的
//	落盘失败          本机的问题（盘满/权限/占用）⇒ 加大预算一点用都没有
//	扩 coverage 失败  **盘上已有这一批而 coverage 里没有** ⇒ 直接重跑会再追加一遍
//
// 📎 ⇒ 本仓那条：**判两个东西该不该共用一个名字，看【处置】分不分岔，不看成因像不像。**

// guard: 停因与它旁边的通道不许互相矛盾 —— 一条出口只能有一个说法。
func TestHaltReasonAgreesWithEveryChannel(t *testing.T) {
	cases := []struct {
		name string
		// build 造一个会走到那条出口的 harness。
		build func() *harness
		want  HaltReason
		// budget 是「这条出口该不该 wrap ErrBudgetExhausted」。
		budget bool
		// orphan 是「这条出口会不会留下盘上有、coverage 里没有的记录」。
		orphan bool
	}{
		{"连续失败超预算", func() *harness {
			return newHarness(t, pacing.NoPacing(), 1, 99)
		}, HaltBudget, true, false},

		{"AppendBars 落盘失败", func() *harness {
			return newHarnessWithStore(t, pacing.NoPacing(), 1, 0,
				&fakeStore{appendErr: errors.New("盘满")})
		}, HaltStoreWrite, false, false},

		{"CommitSpan 扩 coverage 失败", func() *harness {
			return newHarnessWithStore(t, pacing.NoPacing(), 1, 0,
				&fakeStore{commitErr: errors.New("meta 写不进去")})
		}, HaltCoverageWrite, false, true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := c.build()
			rep, err := h.syn.Sync(context.Background(), req(0), 0)

			// 前提：这三条出口都该返回错误。前提不成立就作废，别去解读下面的读数。
			if err == nil {
				t.Fatalf("前提没成立：这条出口应当返回错误，实得 nil（Halt=%v）", rep.Halt)
			}
			if rep.Halt != c.want {
				t.Fatalf("前提没成立：走到的不是那条出口 —— Halt=%v(%d)，期望 %v(%d)\n"+
					"  err = %v", rep.Halt, rep.Halt, c.want, c.want, err)
			}

			// ⭐ 这一条是本守卫的核心：**两个通道不许互相矛盾。**
			// 它比「钉住每条出口的取值」活得久：哪天多出第四条出口，只要它
			// 返回 HaltBudget 而没 wrap 哨兵（或反过来），这里当场红。
			if got := errors.Is(err, ErrBudgetExhausted); got != (rep.Halt == HaltBudget) {
				t.Errorf("两个通道互相矛盾：errors.Is(err, ErrBudgetExhausted)=%v，"+
					"而 Halt=%v\n"+
					"  ⇒ 读的人会按 Halt 那一侧行事（加大预算、稍后重试），\n"+
					"     而在 ≤v0.4.0 上那个动作会把库越弄越坏。\n"+
					"  err = %v", got, rep.Halt, err)
			}
			if got := errors.Is(err, ErrBudgetExhausted); got != c.budget {
				t.Errorf("errors.Is(err, ErrBudgetExhausted)=%v，期望 %v\n  err = %v",
					got, c.budget, err)
			}

			// 三条都必须留声 ——「出错了而报告说干净」是本仓最早堵的那一格。
			if rep.Complete() {
				t.Errorf("这条出口返回了错误，而 rep.Complete() 说干净")
			}
			if _, halted := rep.Halt.note(); !halted {
				t.Errorf("这条出口不留声 —— 它不会出现在 Incidents() 里")
			}

			// ⭐ 孤儿记录那一格：**盘上有、coverage 里没有**。
			// 它是「扩 coverage 失败」必须单独成名的全部理由 ——
			// 直接重跑会把同一批记录再追加一遍。
			gotOrphan := h.store.appended > 0 && len(h.store.spans) == 0
			if gotOrphan != c.orphan {
				t.Errorf("孤儿记录：实得 %v，期望 %v（盘上 %d 根 / coverage %d 段）\n"+
					"  ⇒ 若这一格翻了，说明落盘与登记的先后变了，\n"+
					"     而 %v 那句留声（「别直接重跑」）就不再准确。",
					gotOrphan, c.orphan, h.store.appended, len(h.store.spans), c.want)
			}
		})
	}
}

// —— 射程，写明它不比什么 ——
//
// ⚠️ 它**不**断言留声的【文字】。本仓给 `Incidents()` 写过一条到期条件：
// 「它的返回值第一次被拿去做子串匹配那天，正解是**给它一个类型**，不是去匹配字符串」——
// ⇒ 所以这里只断言**类型化的那个通道**（`HaltReason`），文字留给人读。
// 🔴 代价写出来：**有人把那句「别直接重跑」改成「重跑就行」，这条守卫不会响。**
//
// ⚠️ 它也**不**枚举「所有返回 HaltBudget 的地方」——判据是「这三条出口的行为」，
// 不是「源码里 HaltBudget 出现几次」。⇒ 第四条出口若压根没被测到，它照不到。
