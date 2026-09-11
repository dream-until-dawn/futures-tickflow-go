package tickflow

import "testing"

// —— 这一条守的是【写在 `spansTouchedBy` 注释里的那句行为断言】——
//
//	「一次同步碰到两个不相邻的段时，**两段都会被走查**」
//
// ⛔ 由来（评审方 2026-09-11 打突变打出来的）：把那个函数改成「只取相交的第一段」，
// **全仓一条测试都不红**。⇒ 那句话当时是一条**没有任何东西钉着的断言**。
//
// 📎 判据（收进本仓）：**一句写在函数注释里的行为断言，应当由【那个函数自己的输入】来钉，
// 而不是等端到端去碰。** ——「一次同步能不能到达『一块横跨两段』这个状态」双方都没量过，
// 而**在函数这一层它是可达的**，所以那句话在这一层就该被钉住。
// ⇒ 这也是 `spansTouchedBy` 被做成纯函数（不碰 `s.store`）的理由。
//
// ⛔ 而同一个构造顺带证伪了一句我们差点写进注释的话：
//
//	「每个被选中的合并段至少有一块落在它里面 ⇒ 相交段数 ≤ 分块数 ⇒ 只会更便宜」
//	反例就在下面「一块横跨两段」那一格：j=2 > k=1。**一块可以横跨多段。**
//
// 🔴 收：**一个计数不等式最像「显然」的时候，正是它没被喂过反例的时候。**

// guard: spansTouchedBy 对「一块横跨两个不相邻的段」要回两段 —— 那句注释靠它钉着。
func TestSpansTouchedByCoversEveryIntersectingSpan(t *testing.T) {
	a := Span{From: 20200805, To: 20200805, Bars: 1, Days: 1}
	b := Span{From: 20200807, To: 20200807, Bars: 1, Days: 1}
	cov := []Span{a, b}

	// ⛔ 前提自检：这两段确实**不相邻**（中间隔着 08-06）——
	// 相邻的话 `CommitSpan` 早把它们并了，这个构造就不是它声称的那个。
	if b.From <= a.To+1 {
		t.Fatalf("前提没成立：%v 与 %v 相邻或重叠 ⇒ 构造作废", a, b)
	}

	got := func(t *testing.T, touched []Span) []Span {
		t.Helper()
		return spansTouchedBy(cov, touched)
	}
	same := func(t *testing.T, got, want []Span) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("回了 %d 段（%v），期望 %d 段（%v）", len(got), got, len(want), want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("第 %d 段是 %v，期望 %v", i, got[i], want[i])
			}
		}
	}

	t.Run("标定 只盖住第一段_回一段", func(t *testing.T) {
		// ⇒ 少了这一格，一个「无脑回全部 cov」的实现也能让下面那一格绿。
		same(t, got(t, []Span{{From: 20200805, To: 20200805, Bars: 1, Days: 1}}), []Span{a})
	})

	t.Run("标定 只盖住第二段_回一段", func(t *testing.T) {
		same(t, got(t, []Span{{From: 20200807, To: 20200807, Bars: 1, Days: 1}}), []Span{b})
	})

	t.Run("标定 谁都不沾_回零段", func(t *testing.T) {
		// ⇒ 少了这一格，一个恒回 cov[0] 的实现也能让上面两格里的一格绿。
		if out := got(t, []Span{{From: 20200901, To: 20200902, Bars: 1, Days: 1}}); len(out) != 0 {
			t.Fatalf("与谁都不相交，而它回了 %v", out)
		}
	})

	t.Run("被测 一块横跨两段_两段都要回", func(t *testing.T) {
		// 🔴 这一格就是那句注释：一块 `[08-05, 08-07]` 同时压着 a 和 b。
		// ⚠️ 它也是「相交段数 ≤ 分块数」那句话的反例：这里 j=2 而 k=1。
		out := got(t, []Span{{From: 20200805, To: 20200807, Bars: 2, Days: 2}})
		if len(out) != 2 {
			t.Fatalf("一块横跨两个不相邻的段，而它只回了 %d 段（%v）。\n"+
				"  ⇒ 那句「两段都会被走查」就不成立了：没被回来的那一段不会被走查，\n"+
				"     而 planGaps 会把它报成「本次没走查」—— 正是这一片修掉的那个形状。",
				len(out), out)
		}
		same(t, out, []Span{a, b})
	})

	t.Run("被测 同一段被两块碰到_只回一次", func(t *testing.T) {
		// ⇒ 少了这一格，一个「每命中一次就 append 一次」的实现会让同一段被走查两遍。
		out := got(t, []Span{
			{From: 20200805, To: 20200805, Bars: 1, Days: 1},
			{From: 20200805, To: 20200806, Bars: 1, Days: 1},
		})
		same(t, out, []Span{a})
	})
}
