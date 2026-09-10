package segfile

import (
	"errors"
	"testing"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/embedded"
)

// tradingDays 是这两条测试用到的全部交易日。
//
// ⛔ **为什么是 2020-08 而不是随手挑的 2020-01**：`calendar/embedded` 的
// `baseFrom = 2020-05-06` —— 早于它的交易日**不在覆盖里**，
// 而 `CommitSpan` 会当场拒绝，那句拒绝读起来像「这一段坏了」。
// ⇒ 我第一版就是这么红的。**写下来，免得下一个人照着 1 月那批日期再写一遍。**
//
// ⚠️ 用**真日历**（calendar/embedded）而不是一个替身：`CommitSpan` 会拿它
// 校端点，而**一个自造的替身最容易和被测代码错得一模一样**（本仓那条）。
// 📎 无 import 环：segfile 与 embedded 都只依赖根包，谁也不依赖谁。
var tradingDays = []tickflow.TradingDay{
	20200805, 20200806, 20200807, 20200812, 20200813, 20200814,
}

// ⚠️ 空 ProductKey 在 embedded 日历里【没有覆盖】—— CommitSpan 会当场拒绝，
// 而那句拒绝读起来像「这一段坏了」。用一个真键。
var eqKey = tickflow.ProductKey{Exchange: "SHFE", Product: "rb"}

func testCal(t *testing.T) tickflow.Calendar {
	t.Helper()
	c, err := embedded.New(tradingDays)
	if err != nil {
		t.Fatalf("造日历失败：%v", err)
	}
	return c
}

// —— 换读法那一颗的第一道门：**新旧两个读法答案一致** ——
//
// ⛔ 评审方 2026-09-10 登记：「实现那一颗，若不带这条等价性测试，我不放行。」
//
// ⚠️ 而它能成立的前提写在这儿：**`HasBars` 没有被删掉。**
// 它从 `tickflow.Store` 接口降级成了 `*segfile.Store` 的具体方法，
// **留着就是为了当这条测试的参照实现**。
// 🔴 两边都走新代码的话，这条测试是空的 —— 它会断言「新读法等于它自己」。
// ⇒ 所以下一个想「清理掉这个没人调的方法」的人，请先读这一段。
//
// —— 它照不到的那一格，先写下来 ——
//
// 它验的是「**两个读法在同一份库上一致**」，**不是**「新读法是对的」。
// 两个都错、且错得一样时它照绿。⇒ 挡那一格的是 `TestMultiSpanHasBars` 那类
// **对着构造断言**的测试，不是这一条。**两条各答一半，合起来才是「可以换上去」。**

// buildStore 造一份**形状刻意不平凡**的库，并把它走查好。
//
// ⚠️ 三条构造要求各自防一种空转，逐条写明：
//
//	一、段内既要有【有根的天】也要有【没根的天】
//	   —— 否则「两边都对」在一份全有或全无的库上是必然的
//	二、同一天被 AppendBars 【追加两次】
//	   —— 新读法一次扫描建 set，旧读法找到第一条就返回：
//	     **重复记录正是两者最可能分岔的地方**
//	三、跨两段
//	   —— 新读法按段答，而段的边界是它独有的概念；单段测不到「段外的记录不进结果」
func buildStore(t *testing.T) (*Store, []tickflow.Span) {
	t.Helper()
	s, truncated, err := Open(t.TempDir(), tickflow.Daily)
	if err != nil {
		t.Fatalf("开库失败：%v", err)
	}
	if truncated != 0 {
		t.Fatalf("新库不该有残尾，却报了 %d —— 前提不成立，读数作废", truncated)
	}
	t.Cleanup(func() {
		if cerr := s.Close(); cerr != nil {
			t.Errorf("关库失败：%v", cerr)
		}
	})

	bar := func(d tickflow.TradingDay, ts int64) tickflow.Bar {
		return tickflow.Bar{
			Ts: ts, TsEnd: ts + 1, TradingDay: d,
			Open: 1, High: 1, Low: 1, Close: 1, Volume: 1,
		}
	}
	// 段一 [0805, 0807]：0805 有根（**两条**，其中一条是重复追加）、0806 无根、0807 有根
	// 段二 [0812, 0814]：0812 无根、0813 有根、0814 无根
	// ⛔ **顺序是硬约束**：`Verify` 要求记录按交易日【非降序】
	// （报文：「记录的交易日不是非降序」）。我第一版把重复的那条放在后面一天之后
	// ⇒ 走查当场拒绝。⇒ 重复追加要**在序**。
	// 📎 这条约束顺带是一个设计读数：**已走查段内 `.dat` 是有序的。**
	batches := [][]tickflow.Bar{
		{bar(20200805, 1)},
		{bar(20200805, 2)}, // ← 0805 第二次追加，仍在序
		{bar(20200813, 4)},
	}
	for _, b := range batches {
		if err := s.AppendBars(b); err != nil {
			t.Fatalf("落盘失败：%v", err)
		}
	}
	cal := testCal(t)
	// ⛔ **两段之间要隔一个【没被覆盖的交易日】（这里是 0807），否则它们相邻**
	// —— `CommitSpan` 走 `NormalizeCoverage`，**按交易日相邻就合并**。
	// 我第一版取 [0805,0807] 与 [0812,0814]：0807 与 0812 在这份日历里相邻
	// ⇒ 合成一段 `[0805, 0814]` ⇒ 我手上那两个 `Span` 值在库里**已经不存在**
	// ⇒ `s.verified[那两个值]` 全是 false ⇒ 参照实现报「还没走查过」。
	// 🔴 教训：**我给出去的 Span 是【输入】，不是【读回来的状态】** —— 库会规整它。
	for _, sp := range []tickflow.Span{
		{From: 20200805, To: 20200806, Bars: 2, Days: 1},
		{From: 20200812, To: 20200814, Bars: 1, Days: 1},
	} {
		if err := s.CommitSpan(cal, eqKey, sp, OutcomeComplete); err != nil {
			t.Fatalf("提交 %s..%s 失败：%v", sp.From, sp.To, err)
		}
	}
	// ⇒ 于是**从库里读回来**，而不是复用我刚才那两个值。
	spans := s.Coverage()
	if len(spans) != 2 {
		t.Fatalf("期望库里留下【两段】，实得 %d 段：%v\n"+
			"  ⇒ 两段之间那个没被覆盖的交易日没起作用，它们被合并了；\n"+
			"     而单段测不到「段外的记录不进结果」，这一组读数作废", len(spans), spans)
	}
	// ⛔ 先全提交、再走查：`Verify` 走整个 `.dat`，要求每条记录都落在【某一段】里。
	// 提一段就走一段时，第二段的记录还没有归属 ⇒ 报「记录落在本段之外」。
	for _, sp := range spans {
		if err := s.Verify(sp); err != nil {
			t.Fatalf("走查 %s..%s 失败：%v —— 前提不成立，等价性读数作废", sp.From, sp.To, err)
		}
	}
	return s, spans
}

// guard: 换读法的等价性 —— DaysWithBars 与旧的 HasBars 在同一份库上必须逐日一致。
// TestDaysWithBarsEqualsHasBars 是那道门本身。
func TestDaysWithBarsEqualsHasBars(t *testing.T) {
	s, spans := buildStore(t)

	var sawTrue, sawFalse int
	for _, sp := range spans {
		days, err := s.DaysWithBars(sp)
		if err != nil {
			t.Fatalf("DaysWithBars(%s..%s) 报错：%v", sp.From, sp.To, err)
		}
		// ⛔ **段外的键一个都不许有。**
		//
		// 🔴 这一条是我打突变时才补上的：突变「不看段边界，所有记录都进 map」
		// ⇒ **原本 0 红**。成因是下面那个循环只走 `[sp.From, sp.To]` 之内的天，
		// **从不问 map 里还有没有别的键** —— 断言的粒度比被改的东西小。
		// ⇒ 本仓那条「锚点比被改的东西小 ⇒ 突变只施加了一半」的姊妹形态：
		// **这次是【断言】比被改的东西小，而它的绿与「等价」同形。**
		for d := range days {
			if d < sp.From || d > sp.To {
				t.Errorf("DaysWithBars(%s..%s) 的结果里有段外的 %s\n"+
					"  ⇒ 段边界是新读法独有的概念（旧读法按天问，没有这个概念）：\n"+
					"     它一旦失效，另一段的记录会被算进这一段的答案里。", sp.From, sp.To, d)
			}
		}
		for d := sp.From; d <= sp.To; d++ {
			want, werr := s.HasBars(d) // ← 参照实现：**旧代码原样**
			if werr != nil {
				t.Fatalf("HasBars(%s) 报错：%v —— 参照实现塌了，这一组读数作废", d, werr)
			}
			got := days[d]
			if got != want {
				t.Errorf("%s：新读法 %v，旧读法 %v\n"+
					"  ⇒ 两个读法在同一份库上分岔了。先看重复追加那一天：\n"+
					"     旧读法【找到第一条就返回】，新读法一次扫描建 set。", d, got, want)
			}
			if want {
				sawTrue++
			} else {
				sawFalse++
			}
		}
	}

	// ⛔ 前提自检：两种答案都必须出现过。
	// 🔴 一份全有（或全无）的库会让「两边一致」成为必然 —— 那时这条测试是空转的，
	// 而空转与「等价」同形。
	if sawTrue == 0 || sawFalse == 0 {
		t.Fatalf("这份库上「有根」%d 天、「没根」%d 天 —— 两种答案没有都出现，"+
			"这条测试是空转的，读数作废", sawTrue, sawFalse)
	}
	t.Logf("逐日比过 %d 天（有根 %d · 没根 %d），两个读法一致", sawTrue+sawFalse, sawTrue, sawFalse)
}

// guard: 三值语义的第三值 —— 未走查段上，新旧两个读法都必须【答不了】而不是【说没有】。
// TestUnverifiedSpanRefusesBothReads 守的是 B3 在换读法之后仍然成立。
//
// ⛔ 它与上面那条**不是同一件事**：上面比的是【两个布尔值】，
// 而这一条比的是**错误面** —— 一个「答不了」被换成「确认没有」，
// 在布尔那一侧看起来完全正常。
func TestUnverifiedSpanRefusesBothReads(t *testing.T) {
	s, _, err := Open(t.TempDir(), tickflow.Daily)
	if err != nil {
		t.Fatalf("开库失败：%v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	cal := testCal(t)
	sp := tickflow.Span{From: 20200805, To: 20200807, Bars: 1, Days: 1}
	if err := s.AppendBars([]tickflow.Bar{{
		Ts: 1, TsEnd: 2, TradingDay: 20200805, Open: 1, High: 1, Low: 1, Close: 1, Volume: 1,
	}}); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitSpan(cal, eqKey, sp, OutcomeComplete); err != nil {
		t.Fatal(err)
	}
	// **故意不 Verify。**

	if _, err := s.DaysWithBars(sp); !errors.Is(err, tickflow.ErrSpanUnverified) {
		t.Errorf("未走查段上 DaysWithBars 给的是 %v，而该给 ErrSpanUnverified\n"+
			"  ⇒ 一个空 map ＋ nil 会被调用方读成「这一段每天都没有根」——\n"+
			"     那正是「没验过悄悄变成一个肯定的答案」那个洞（B3）。", err)
	}
	if _, err := s.HasBars(20200805); !errors.Is(err, tickflow.ErrSpanUnverified) {
		t.Errorf("未走查段上 HasBars 给的是 %v，而该给 ErrSpanUnverified —— "+
			"参照实现这一侧也塌了，上面那条断言的对照就没了", err)
	}
}
