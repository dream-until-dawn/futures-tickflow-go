package tickflow

import (
	"errors"
	"fmt"
	"testing"
)

// —— 这一条守的是【「本次没走查」与「走查过而没通过」分得开】——
//
// ⛔ 由来（2026-09-11 实测）：`GapStoreVerifyUnrun` 一类盖着两种来历，而**处置分岔**：
//
//	本次没走查过这一段        它**不表示这一段有问题**（健康库上就是这一支）
//	走查过了，而它没通过      **别再重跑**，去读真因
//
// 而真因当时被转成一句字符串扔进 `TruncatedTails` —— **分得开的信息已经在手里，我们把它丢了。**
//
// ⚠️ 而这一条同时守着一件**反向**的事，它比上面那件更要紧：
// `classifyTradingDay` 的最后一支是**兜底**（认不出的错误一律中止，不折进任何一类缺口）。
// 🔴 **我们这次是在一个封闭的维度上开一个口子，而开口子的人最容易顺手把口子开大。**
// ⇒ 所以下面每一格「该认出来」的旁边，都配了一格「**该照旧中止**」。

// guard: 「本次没走查」与「走查过而没通过」分得开，而兜底不许因此变松。
func TestVerifyFailedIsItsOwnClassAndTheCatchAllStaysTight(t *testing.T) {
	cal := week() // 覆盖 20200106..20200112
	k := ProductKey{Exchange: "SHFE", Product: "rb"}
	sp := Span{From: 20200106, To: 20200107, Bars: 2, Days: 2}
	daysOf := func(Span) (map[TradingDay]bool, error) { return map[TradingDay]bool{}, nil }

	plan := func(t *testing.T, err error) ([]Gap, error) {
		t.Helper()
		return PlanGaps(cal, k, 20200106, 20200107,
			[]SpanStatus{{Span: sp, Err: err}}, daysOf)
	}

	// ⛔ 前提自检：**先证明这些输入真的走到了 coverage 那一支。**
	// 本仓那条：一次测量在给出读数之前，必须先证明它发生在它声称的那个位置 ——
	// 我第一版探针的日历盖不到那个区间，两支都返回「日历答不了」，
	// **而它印出来的 err=nil 读起来像「那一支容忍了它」。**
	if gaps, err := plan(t, ErrSpanUnverified); err != nil ||
		len(gaps) == 0 || gaps[0].Kind != GapStoreVerifyUnrun {
		t.Fatalf("前提没成立：标定端没走到 coverage 那一支 ⇒ 整轮读数作废\n"+
			"  gaps=%v err=%v", gaps, err)
	}

	cause := errors.New("segfile: 记录的交易日落在本段之外")

	// ⛔ 这一格喂的是【裸哨兵】，而不是包了真因的那个 —— 2026-09-11 评审方打突变打出来的：
	// 上一版喂 `fmt.Errorf("%w: %w", 哨兵, 真因)`，而「三」喂的是它【再包一层】
	// ⇒ **三的输入 ⊃ 一的输入，而两格断言相同** ⇒ 凡是让一红的缺陷必然也让三红，
	// **一在【红】这一侧不提供任何超出三的判别力**（实测：M1 与 M2 红的是同一对子用例）。
	// 🔴 收：**两格若输入是包含关系而断言相同，前一格就是多余的** ——
	// 而它的代价不是冗余，是**两个不同的缺陷印出同一张脸**。
	// ⇒ 改喂裸哨兵之后：把那一支改成 `==` 时「一」绿而「三」红，两者才分得开。
	t.Run("一 喂裸哨兵_给出新类别而不是中止", func(t *testing.T) {
		gaps, err := plan(t, ErrSpanVerifyFailed)
		if err != nil {
			t.Fatalf("它中止了，而这一类现在该被认出来：%v", err)
		}
		if len(gaps) == 0 || gaps[0].Kind != GapStoreVerifyFailed {
			t.Fatalf("期望 %v，实得 %v", GapStoreVerifyFailed, gaps)
		}
	})

	t.Run("一之二 两个errors_Is都要取得到", func(t *testing.T) {
		// 🔴 只包哨兵 ⇒ 调用方回不到现场；只包真因 ⇒ 调用方分不出「这一类」。两头都要。
		wrapped := fmt.Errorf("%w: %w", ErrSpanVerifyFailed, cause)
		if !errors.Is(wrapped, ErrSpanVerifyFailed) {
			t.Error("取不到哨兵 ⇒ 调用方分不出「这一类」与「这一个错」")
		}
		if !errors.Is(wrapped, cause) {
			t.Error("取不到真因 ⇒ 调用方只剩一个类别名，再也回不到现场")
		}
	})

	t.Run("二 第四种错误_仍然中止", func(t *testing.T) {
		fourth := errors.New("某个我们没见过的错误")
		gaps, err := plan(t, fourth)
		if err == nil {
			t.Fatalf("兜底变松了：一个认不出的错误被折进了缺口 %v\n"+
				"  ⇒ 那等于把白名单改成了黑名单，而这道兜底当初防的正是这个。", gaps)
		}
		if !errors.Is(err, fourth) {
			t.Errorf("它中止了，而真因没被包回来：%v", err)
		}
		if len(gaps) != 0 {
			t.Errorf("中止时还给了 %d 条缺口", len(gaps))
		}
	})

	t.Run("三 哨兵被再包一层_仍被认出", func(t *testing.T) {
		// 🔴 这一格分得开【errors.Is 分支】与【== 比较】——
		// 后者在多包一层之后就认不出来了，而调用方多包一层是常态。
		outer := fmt.Errorf("外层: %w", fmt.Errorf("%w: %w", ErrSpanVerifyFailed, cause))
		gaps, err := plan(t, outer)
		if err != nil {
			t.Fatalf("多包一层就掉进兜底了 ⇒ 那一支多半写成了 == 而不是 errors.Is：%v", err)
		}
		if len(gaps) == 0 || gaps[0].Kind != GapStoreVerifyFailed {
			t.Fatalf("期望 %v，实得 %v", GapStoreVerifyFailed, gaps)
		}
	})

	t.Run("四 两类不许混_旧哨兵仍给旧类别", func(t *testing.T) {
		gaps, err := plan(t, ErrSpanUnverified)
		if err != nil || len(gaps) == 0 || gaps[0].Kind != GapStoreVerifyUnrun {
			t.Fatalf("旧哨兵不再给旧类别了 ⇒ 两类混了：gaps=%v err=%v", gaps, err)
		}
	})
}

// —— 射程，写明它不比什么 ——
//
// ⚠️ 它断言的是**分类那一层**（`PlanGaps` 收到什么 ⇒ 给出什么）。
// **不管**的是：`Syncer` 那一侧有没有把真因填进去 —— 那由 `sync_dispose_test.go`
// 里那条端到端的守着（走查全失败 ⇒ 报告里要有这一类）。
// ⇒ 写清楚是因为：不写的话，下一个人会把这条的绿读成「整条线都接对了」。
//
// ⚠️ 而 `GapStoreVerifyUnrun` 这一类**本身**的射程写在 `gap.go` 上：
// 它说的是【本次】没走查过，**不表示这一段有问题** —— 而今天在健康库上它也会出现。
