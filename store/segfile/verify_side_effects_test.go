package segfile

import (
	"strings"
	"testing"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
)

// —— 这一条守的是【写进处置里的那两句话为真】——
//
// `gap.go` 的 GapStoreUnverified 把「跑一次 store.Verify(span)」写成了给用户的判别符，
// 并在旁边写了两句限定。那两句话是**断言**，所以要有东西钉住它们：
//
//	一、它【不是只读的】：走查通过之后，HasBars / DaysWithBars 对这一段
//	    从【拒绝回答】变成【回答】
//	二、span 必须是 Coverage() 返回的那个值 —— 自己拼一个 Bars 不同的，
//	    DaysWithBars 会报「这一段还没走查过」，**与真的没验过一模一样**
//
// ⛔ 而第二格是【钉住现状，而现状是有缺陷的】，红有两个方向：
//
//	红法一  构造被改坏 ⇒ 前提自检先说话
//	红法二  **有人把这句报文改准了**（例如改成「你给的 [From,To] 不在任何一段 coverage 里」）
//	        ⇒ 那是**修好了**，该改的是这条测试与 gap.go 那段处置，**不是把它调绿**
//
// 📎 而这一格本质上是**把一个被删掉的输入请回来**：2026-09-10 我为了让等价性测试通过，
// 把「自己拼的 Span」换成了「从库里读回来的值」—— 而那恰好消灭了唯一能暴露这个缺陷的输入。

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
// （`gap.go` 的 `GapStoreUnverified`）——**那三句话会被照着做**，
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

	t.Run("二 自拼的 span 会得到一句会骗人的话", func(t *testing.T) {
		fake := tickflow.Span{From: got.From, To: got.To, Bars: got.Bars + 97, Days: got.Days}
		_, err := s.DaysWithBars(fake)
		if err == nil {
			t.Fatal("自拼的 span 竟然答得出来 ⇒ 键不再是整个结构体了，处置里那句限定要改")
		}
		if !strings.Contains(err.Error(), "还没走查过") {
			t.Logf("⇒ 报文变了：%v", err)
			t.Errorf("这一格钉的是【现状】，而现状是有缺陷的：\n" +
				"  旧（有缺陷）：「这一段还没走查过」—— 与真的没验过一模一样\n" +
				"  新（修好了）：例如「你给的 [From,To] 不在任何一段 coverage 里」\n" +
				"  ⇒ 若是后者，那是【修好了】：改这条测试与 gap.go 那段处置，别把类别改回去。")
		}
	})
}
