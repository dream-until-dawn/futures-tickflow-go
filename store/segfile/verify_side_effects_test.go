package segfile

import (
	"strings"
	"testing"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
)

// —— 这一条守的是【写进处置里的那两句话为真】——
//
// `gap.go` 的 GapStoreVerifyUnrun 把「跑一次 store.Verify(span)」写成了给用户的判别符，
// 并在旁边写了两句限定。那两句话是**断言**，所以要有东西钉住它们：
//
//	一、它【不是只读的】：走查通过之后，HasBars / DaysWithBars 对这一段
//	    从【拒绝回答】变成【回答】
//	二、span 必须是 Coverage() 返回的那个值 —— 自己拼一个 Bars 不同的，
//	    DaysWithBars 会报「这一段还没走查过」，**与真的没验过一模一样**
//
// ✅ **第二格已于 (j)（2026-09-12）翻面** —— 它上一版钉的是**缺陷**：
// 自己拼一个 `From`/`To` 相同而 `Bars` 不同的 Span，`DaysWithBars` 报
// 「这一段还没走查过」**——与真的没验过一模一样**，于是**一个调用方的错被报成了库的状态**。
// 那条报文里写死的红法二是：「有人把这句报文改准了 ⇒ 那是**修好了**，改这条测试，别把它调绿」。
// 🔴 **(j) 就是那次修好**，所以这一格现在钉的是**修法**，红法二也跟着换了（见下面那一格）。
//
// (j) 做的事：`DaysWithBars` **先在 `s.meta.Coverage` 里按 `[From, To]` 找到登记的那一段**，
// 再拿**那个值**当 `verified` 的键 —— 那一步 `HasBars` 一直就有，(j) 把它搬了过来。
// ⇒ 于是身份变成了 `[From, To]`，而 `Bars`/`Days` 是**内容**：
//
//	Bars 不同而 From/To 相同  ⇒ **答得出来**（问的是「这一段里哪些天有根」，与那两个计数无关）
//	From/To 不在 coverage 里   ⇒ 报「不在任何一段 coverage 里」—— **一句指向调用方的话**
//
// 📎 而这一格本质上是**把一个被删掉的输入请回来**：2026-09-10 我为了让等价性测试通过，
// 把「自己拼的 Span」换成了「从库里读回来的值」—— 而那恰好消灭了唯一能暴露这个缺陷的输入。
// 🔴 **那个输入从 2026-09-10 被删掉，到 (j) 修好，中间隔了两天** ——
// 而这两天里它一直在，只是没有任何输入喂得到它。

// —— ⚠️ 它是【搭车】，不是【承重】：它钉的是既有现状，不是这次改动 ——
//
// 评审方 2026-09-11 指出，我实测了：**把这份文件原样放到 `main`（乙片【之前】）上跑 ⇒ 两格都 PASS。**
// ⇒ 它在乙片之前就绿、之后也绿 ⇒ **它不提供任何关于乙片的信息。**
//
// 🔴 本仓那条：**一条测试的绿，只在它会因这次改动而变时，才是这次改动的证据。**
// ⇒ 写在这儿是为了让下一个人别把「乙片那批守卫全绿」读得比实际强 ——
//   承重的是 `gap_verify_failed_test.go` 那五格；这一份是搭车的两格。
//
// ⇒ 那它为什么还值得有？因为它钉的是**写进用户处置里的三句断言**
// （`gap.go` 的 `GapStoreVerifyUnrun`）——**那三句话会被照着做**，
// 而「它们今天为真」这件事此前没有任何东西守着。

// guard: 写进处置的那两句（Verify 不是只读的 · span 必须来自 Coverage）必须为真。
func TestVerifyIsNotReadOnlyAndSpanMustComeFromCoverage(t *testing.T) {
	dir := t.TempDir()
	s, _, err := Open(dir, tickflow.Daily)
	if err != nil {
		t.Fatalf("开库失败：%v", err)
	}
	t.Cleanup(func() { s.Close() })

	d1, d2 := tradingDays[0], tradingDays[1]
	if err := s.AppendBars([]tickflow.Bar{bar(d1, 1), bar(d2, 2)}); err != nil {
		t.Fatalf("落盘失败：%v", err)
	}
	sp := tickflow.Span{From: d1, To: d2, Bars: 2, Days: 2}
	if err := s.CommitSpan(testCal(t), eqKey, sp, tickflow.OutcomeComplete); err != nil {
		t.Fatalf("登记失败：%v", err)
	}

	// ⛔ 前提自检：走查【之前】必须先拒绝回答 —— 否则这条测试量不到那个变化，
	// 而它会绿得像「Verify 没有副作用」。
	if _, e := s.DaysWithBars(sp); e == nil {
		t.Fatal("前提没成立：走查之前它就能回答 ⇒ 读数作废")
	}

	got := s.Coverage()[0]
	if err := s.Verify(got); err != nil {
		t.Fatalf("前提没成立：这一段本该走查得过，实得 %v", err)
	}

	t.Run("一 Verify 不是只读的", func(t *testing.T) {
		m, err := s.DaysWithBars(got)
		if err != nil {
			t.Fatalf("走查通过之后它仍然拒绝回答 ⇒ 处置里那句「从拒绝回答变成回答」是假的：%v", err)
		}
		if len(m) == 0 {
			t.Errorf("它回答了，而答案是空的 —— 那两句话的证据不成立")
		}
	})

	t.Run("二之一 Bars 不同而 From_To 相同_答得出来", func(t *testing.T) {
		// 身份是 [From, To]，`Bars`/`Days` 是内容 ——
		// 问的是「这一段里哪些天有根」，那与登记的两个计数无关。
		fake := tickflow.Span{From: got.From, To: got.To, Bars: got.Bars + 97, Days: got.Days}
		m, err := s.DaysWithBars(fake)
		if err != nil {
			t.Fatalf("自拼的 span（只有 Bars 不同）答不出来：%v\n"+
				"  ⇒ 这是回归：(j) 之前它报「这一段还没走查过」，与真的没验过一模一样，\n"+
				"     而那是【一个调用方的错被报成了库的状态】。\n"+
				"  ⇒ 去看 DaysWithBars 里那段「先在 coverage 里按 [From,To] 找到登记的那一段」。", err)
		}
		if len(m) == 0 {
			t.Errorf("它答了，而答案是空的 —— 这一段明明有根")
		}
	})

	t.Run("二之二 From_To 不在 coverage 里_报的是一句指向调用方的话", func(t *testing.T) {
		// ⛔ 这一格钉的是那句话**指向谁**：
		//	旧：「这一段还没走查过」 —— 指向【库的状态】，而真因在调用方
		//	新：「不在任何一段 coverage 里」 —— 指向【调用方给的那个值】
		// 📎 本仓那条：**报错指向数据，而真因在调用方。**
		fake := tickflow.Span{From: 20990101, To: 20990102, Bars: 1, Days: 1}
		_, err := s.DaysWithBars(fake)
		if err == nil {
			t.Fatal("一个根本不在 coverage 里的段竟然答得出来 ⇒ 那一步查找没生效")
		}
		if !strings.Contains(err.Error(), "不在任何一段 coverage 里") {
			t.Errorf("报文没指向调用方：%v\n"+
				"  ⇒ 若它说的是「还没走查过」：那是 (j) 之前那句会骗人的话回来了 ——\n"+
				"     它与【真的没验过】一模一样，而真因是调用方给了一个库里没有的段。", err)
		}
		if strings.Contains(err.Error(), "还没走查过") {
			t.Errorf("报文里仍然有「还没走查过」：%v ⇒ 两种状态又共用一句话了", err)
		}
	})

	t.Run("二之三 一个严格的子区间_也不算那一段", func(t *testing.T) {
		// ⛔ 这一格钉的是那个查找判据是【相等】，不是【包含】。
		//
		// 🔴 它的由来是一格**全绿**的突变：把判据改成包含之后，上面两格**都不红**
		// （自拼那个被包含 ⇒ 照答；越界那个仍不被包含 ⇒ 照拒）
		// ⇒ **那两格分不开这两种判据**，而「包含」是最容易被顺手写成的那一种。
		//
		// 为什么必须是相等：这个方法的契约是「回答**一整段**」，
		// 而调用方点名的是 coverage 里的**某一段**。一个子区间不是一段 ——
		// 对它回答，等于替调用方认定「你要问的是包着它的那一段」，**而那不是他说的话**。
		sub := tickflow.Span{From: got.To, To: got.To, Bars: 1, Days: 1}
		// ⛔ 前提自检：造出来的真的是【严格】子区间 —— 与整段同起点的话这一格量的是别的东西。
		if sub.From == got.From {
			t.Fatalf("前提没成立：%v 与整段 %v 同起点，不是严格子区间 ⇒ 读数作废", sub, got)
		}
		_, err := s.DaysWithBars(sub)
		if err == nil {
			t.Fatalf("一个严格子区间 %v 竟然答得出来 ⇒ 那个查找判据成了【包含】。\n"+
				"  ⇒ 契约是「回答一整段」，而子区间不是一段。", sub)
		}
		if !strings.Contains(err.Error(), "不在任何一段 coverage 里") {
			t.Errorf("它拒了，而没拒在那一条上：%v", err)
		}
	})
}
