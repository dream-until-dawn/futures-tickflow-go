package tickflow

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dream-until-dawn/futures-tickflow-go/source/pacing"
)

// 本文件是【丙三之三】：C3b · D2b · SYN-6。
// SYN-10 在 store/segfile 那一侧（走查是它的活），测试也在那儿。

// ───────── C3b：截断要进报告 ─────────

// TestC3bTruncatedTailReachesTheReport 截了不留声 ⇒ 与「本来没事」同形。
//
// ⛔ 断言两件事，而第二件才是 C3b 的要害：**它让报告变得不 Complete。**
// 只把字符串塞进去而 `Complete()` 照旧为真，等于「在日志上吵、在报告上哑」。
func TestC3bTruncatedTailReachesTheReport(t *testing.T) {
	st := &fakeStore{openState: OpenState{TruncatedTail: 33}}
	h := newHarnessWithStore(t, pacing.NoPacing(), 1, 0, st)

	rep, err := h.syn.Sync(context.Background(), req(0), 0)
	if err != nil {
		t.Fatalf("残尾不该让同步失败（它已经处理好了）：%v", err)
	}
	if len(rep.TruncatedTails) != 1 {
		t.Fatalf("TruncatedTails 有 %d 条，期望 1 条", len(rep.TruncatedTails))
	}
	if rep.Complete() {
		t.Error("库开的时候截过残尾，而报告说 Complete() —— 下游读的正是这一位")
	}

	// 对照：没有残尾时那一格必须是空的 ——
	// ⛔ 少了这一条，一个「无论如何都塞一条」的实现也会绿。
	h2 := newHarnessWithStore(t, pacing.NoPacing(), 1, 0, &fakeStore{})
	rep2, err := h2.syn.Sync(context.Background(), req(0), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep2.TruncatedTails) != 0 {
		t.Errorf("没有残尾时 TruncatedTails 却有 %d 条", len(rep2.TruncatedTails))
	}
	if !rep2.Complete() {
		t.Errorf("一次干净的同步却不 Complete：%s", rep2)
	}
}

// ───────── D2b：旧 .meta 的两种处置都要进报告 ─────────

// TestD2bBothDispositionsReachTheReport 两支都测，而它们的**方向相反**。
//
//	可重放   ⇒ coverage 作废、重拉、报告留声、**不报错**
//	不可重放 ⇒ coverage【不】作废、报 ErrLegacyMeta、报告留声
//
// ⛔ 只测一支的话，「无论如何都作废」和「无论如何都停」各自都会绿一半 ——
// 而 D2a 那条判据的全部内容就是**这两支必须不同**。
func TestD2bBothDispositionsReachTheReport(t *testing.T) {
	legacyCov := []Span{{From: 20200106, To: 20200107, Bars: 2, Days: 2}}

	t.Run("可重放 ⇒ 作废重拉", func(t *testing.T) {
		// httpSource 的 Since 是 20200101 ≤ coverage 最早的 20200106 ⇒ 可重放。
		st := &fakeStore{openState: OpenState{LegacyMeta: true}, coverage: legacyCov}
		h := newHarnessWithStore(t, pacing.NoPacing(), 1, 0, st)

		rep, err := h.syn.Sync(context.Background(), req(0), 0)
		if err != nil {
			t.Fatalf("可重放那一支不该要人介入：%v", err)
		}
		if st.discarded != 1 {
			t.Errorf("DiscardCoverage 被调了 %d 次，期望 1 次 ——\n"+
				"  ⇒ 报告说「已作废」而 coverage 原样留着，正是 D2 要挡的那个错", st.discarded)
		}
		if len(rep.LegacyMetaDiscarded) != 1 {
			t.Errorf("LegacyMetaDiscarded 有 %d 条，期望 1 条", len(rep.LegacyMetaDiscarded))
		}
		if len(rep.LegacyMetaUnverified) != 0 {
			t.Errorf("走了作废那一支，却也留了 unverified 的声")
		}
		if rep.Complete() {
			t.Error("做过一次自愈动作，而报告说 Complete()")
		}
	})

	t.Run("不可重放 ⇒ 不作废，报 ErrLegacyMeta", func(t *testing.T) {
		// coverage 最早是 20191201，早于源的 Since=20200101 ⇒ 重放不回来。
		st := &fakeStore{openState: OpenState{LegacyMeta: true},
			coverage: []Span{{From: 20191201, To: 20191231, Bars: 5, Days: 5}}}
		h := newHarnessWithStore(t, pacing.NoPacing(), 1, 0, st)

		rep, err := h.syn.Sync(context.Background(), req(0), 0)
		if !errors.Is(err, ErrLegacyMeta) {
			t.Fatalf("不可重放那一支应当报 ErrLegacyMeta，实得 %v", err)
		}
		if st.discarded != 0 {
			t.Errorf("不可重放却作废了 coverage %d 次 ——\n"+
				"  ⇒ 重拉取不回那段，作废在这里换不来任何东西，而丢掉区间不可逆", st.discarded)
		}
		if len(rep.LegacyMetaUnverified) != 1 {
			t.Errorf("LegacyMetaUnverified 有 %d 条，期望 1 条", len(rep.LegacyMetaUnverified))
		}
		if rep.Complete() {
			t.Error("停在一个要人拍板的地方，而报告说 Complete()")
		}
	})

	t.Run("没有 coverage ⇒ 到不了 D2a", func(t *testing.T) {
		// ⚠️ 没有 coverage 就没有「语义未知的 coverage」要处置。
		// 这一格钉住的是**那个分支不会被走到**，免得下一个人去补它。
		st := &fakeStore{openState: OpenState{LegacyMeta: true}}
		h := newHarnessWithStore(t, pacing.NoPacing(), 1, 0, st)
		rep, err := h.syn.Sync(context.Background(), req(0), 0)
		if err != nil {
			t.Fatalf("空 coverage 上不该走 D2a：%v", err)
		}
		if st.discarded != 0 || len(rep.LegacyMetaDiscarded)+len(rep.LegacyMetaUnverified) != 0 {
			t.Error("空 coverage 上走了 D2a")
		}
	})
}

// TestReplayableIsAboutTheRangeNotTheSource 丙三冲突三那句话的落点。
//
// 🔴 **「可不可重放」不是【源】的性质，是【源 × 这一段】的性质。**
// 同一个源、同一份 Caps，对两段不同的 coverage 给出**相反**的答案 ——
// 这一条断言的正是那个「相反」，不是两个孤立的值。
//
// ⚠️ 而设计里原本写「只有编排经 Caps 知道源可不可重放」，**抄了四遍，
// 而 Caps 里根本没有那一格**。这条测试是那次对表的落地。
func TestReplayableIsAboutTheRangeNotTheSource(t *testing.T) {
	h := newHarness(t, pacing.NoPacing(), 1, 0)
	r := req(0) // Period = Daily；httpSource 的 Since[Daily] = 20200101

	for _, c := range []struct {
		name string
		cov  []Span
		want bool
	}{
		{"coverage 起点晚于 Since ⇒ 拿得回来", []Span{{From: 20200106, To: 20200110}}, true},
		{"coverage 起点正好等于 Since ⇒ 拿得回来", []Span{{From: 20200101, To: 20200110}}, true},
		{"coverage 起点早于 Since ⇒ 拿不回来", []Span{{From: 20191201, To: 20200110}}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := h.syn.replayable(r, c.cov); got != c.want {
				t.Errorf("replayable = %v，期望 %v", got, c.want)
			}
		})
	}

	// Since 缺失 ⇒ false。判据：**给一个取值定级，取它最哑的那个后果。**
	// 取 true 最哑的后果是把一段取不回来的历史作废掉且不可逆；取 false 是多问一次人。
	r2 := r
	r2.Period = MustIntraday(1) // httpSource 的 Since 里没有这个周期
	if h.syn.replayable(r2, []Span{{From: 20200106, To: 20200110}}) {
		t.Error("Since 里没有这个周期，却判成可重放 —— 那会把一段取不回来的历史作废掉")
	}
}

// ───────── SYN-6：结束时走查本次碰过的段 ─────────

// TestSYN6VerifiesEverySpanTouchedThisRun 不走查 ⇒ 冷序列的损坏**发现时间没有上界**。
func TestSYN6VerifiesEverySpanTouchedThisRun(t *testing.T) {
	st := &fakeStore{}
	h := newHarnessWithStore(t, pacing.NoPacing(), 1, 0, st) // 一天一块 ⇒ 5 段
	rep, err := h.syn.Sync(context.Background(), req(0), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.spans) == 0 {
		t.Fatal("一段都没提交 —— 基线没成立，下面那条相等什么也不证明")
	}
	if len(st.verified) != len(st.spans) {
		t.Errorf("提交了 %d 段，只走查了 %d 段 ——\n"+
			"  ⇒ 没走查的那些，损坏要等到有人来取那段数据的那天才知道",
			len(st.spans), len(st.verified))
	}
	if !rep.Complete() {
		t.Errorf("走查都过了却不 Complete：%s", rep)
	}
}

// TestSYN6VerifyFailureLeavesANote 走查失败要留声，而不是被吞掉。
func TestSYN6VerifyFailureLeavesANote(t *testing.T) {
	st := &fakeStore{verifyErr: errors.New("segfile: 假装走查发现对不上")}
	h := newHarnessWithStore(t, pacing.NoPacing(), 1, 0, st)
	rep, err := h.syn.Sync(context.Background(), req(0), 0)
	if err != nil {
		t.Fatalf("走查失败不改变「已落盘的是真的」，不该整体报错：%v", err)
	}
	if len(rep.TruncatedTails) != len(st.spans) {
		t.Errorf("%d 段走查全失败，却只留了 %d 条声", len(st.spans), len(rep.TruncatedTails))
	}
	if rep.Complete() {
		t.Error("每一段走查都失败了，而报告说 Complete()")
	}
}

// ───────── 缺口：报告里那个「0 段缺口」必须是【算出来的】 ─────────

// TestGapsAreComputedNotLeftEmpty 一份从不填 Gaps 的实现，`String()` 会永远印
// 「0 段缺口」——**那不是缺席，是一个自信的错答案。**
func TestGapsAreComputedNotLeftEmpty(t *testing.T) {
	// 库里什么都没有 ⇒ 请求区间里的每个交易日都该落进某一类缺口。
	st := &fakeStore{}
	h := newHarnessWithStore(t, pacing.NoPacing(), 1, 0, st)
	rep, err := h.syn.Sync(context.Background(), req(0), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Gaps) == 0 {
		t.Fatal("一段缺口都没报 —— 而这一次同步之后 HasBars 全是 false，" +
			"那几天都该落进「拉过、确认没有」那一类")
	}
	// 每一段都必须落在 [Requested] 里 —— 一个乱填的实现挡在这儿。
	for _, g := range rep.Gaps {
		if g.From < rep.Requested[0] || g.To > rep.Requested[1] {
			t.Errorf("缺口 %s 落在请求区间 %s..%s 之外",
				g, rep.Requested[0], rep.Requested[1])
		}
		if g.Kind == 0 {
			t.Errorf("缺口 %s 的类别是零值 —— 六类之外还有第七类？", g)
		}
	}

	// 🔴 **类别要分得开，而这一格钉住的是 B3 的接线**：
	// 本次走查过的段 ⇒ 「拉过、确认没有」（GapConfirmedEmpty）；
	// 没走查过的段   ⇒ 「答不了」（GapStoreUnverified）。
	// ⇒ 若 planGaps 那张「哪些段走查过」的表匹配不上（比如键的形状不对），
	//   每一段都会变成 Unverified —— **而那读起来仍然像一份正常的报告**。
	for _, g := range rep.Gaps {
		if g.Kind == GapStoreUnverified {
			t.Errorf("缺口 %s 报成【答不了·未走查】，而本次这些段都刚走查过 ——\n"+
				"  ⇒ 「哪些段走查过」那张表没有匹配上", g)
		}
	}

	// 对照：让走查失败，那些段就该变成【答不了】——
	// ⛔ 少了这一格，上面那条也可能是「它对谁都不报 Unverified」。
	st2 := &fakeStore{verifyErr: errors.New("假装走查失败")}
	h2 := newHarnessWithStore(t, pacing.NoPacing(), 1, 0, st2)
	rep2, err := h2.syn.Sync(context.Background(), req(0), 0)
	if err != nil {
		t.Fatal(err)
	}
	// ⛔ **这一格 2026-09-11 改过期望值，而改的方向是【修好了】，不是【弄坏了】** ——
	// 写清楚，因为本仓那条：一条钉住行为的测试，红有两个方向，报文要说得出是哪一个。
	//
	//	改之前  走查失败 ⇒ GapStoreUnverified   ← 它与「本次没走查」**共用一个类别**
	//	改之后  走查失败 ⇒ GapStoreVerifyFailed ← 而真因包在 Err 里，errors.Is 取得到
	//
	// 🔴 旧的那个期望值**钉住的正是那次混装**：它要求「走查失败」也报成「未走查」，
	// 而那两件事的处置分岔（前者重跑无意义，后者不表示这一段有问题）。
	// ⇒ 所以这一次红是产品变对了，处置是改这里的期望，不是把类别改回去。
	found := false
	for _, g := range rep2.Gaps {
		if g.Kind == GapStoreVerifyFailed {
			found = true
		}
	}
	if !found {
		t.Errorf("走查全失败，却一段【答不了·走查没通过】都没有 —— "+
			"那说明 Gaps 的类别根本没跟着走查结果走。\n"+
			"  实得：%v", rep2.Gaps)
	}
}

// —— 这一条守的是【`Store` 违约 ＝ 坏了，不是答不了】——
//
// `VerifyCoverage` 的契约一要求它是**全的**：`Coverage()` 里每一段都要有一个结论。
// ⛔ 漏掉一段时，编排这一层**认不出**发生了什么 ⇒ 按本仓那条最硬的规矩：
// **认不出的一律中止，不折进任何一类缺口。**
//
// 🔴 把它折成 `GapStoreUnverified` 看起来更温和，而那是有害的：
// 调用方会拿到一个**看起来可以照着处置的答案**，而那个处置不存在
// （「再走一遍」对一个不给结论的实现没有用）。
// 📎 与 `classifyTradingDay` 那条兜底同一个处置、同一个理由。
//
// ⚠️ 而「这一遍跑不起来」（`VerifyCoverage` 的第二个返回值）**不是违约**：
// 那时每一段落到「本次没走查过」—— **而那句话是真的**，所以它照旧报成缺口。
// ⇒ 下面第二格钉的就是这个**分岔**：少了它，一个把两者都当成中止的实现也能让第一格绿。

// guard: VerifyCoverage 漏掉一段 ⇒ 中止；而「跑不起来」不是违约，照旧报成缺口。
func TestStoreBreachAbortsWhileUnrunnableDoesNot(t *testing.T) {
	sp := Span{From: 20200805, To: 20200806, Bars: 2, Days: 2}

	t.Run("违约 漏掉一段_中止", func(t *testing.T) {
		st := &fakeStore{coverage: []Span{sp}, verifySkipAll: true}
		_, _, breach := (&Syncer{store: st}).verifyAll(&SyncReport{})
		if breach == nil {
			t.Fatal("漏掉一段而它没有中止 ⇒ 那一段会被折成一类缺口，" +
				"而调用方会拿到一个照着做不通的处置")
		}
		if !strings.Contains(breach.Error(), "没有为") {
			t.Errorf("它中止了，而报文没点名是哪一段没给结论：%v", breach)
		}
	})

	t.Run("跑不起来 不是违约_照旧报成缺口", func(t *testing.T) {
		st := &fakeStore{coverage: []Span{sp}, verifyAllErr: errors.New("读盘失败")}
		rep := &SyncReport{}
		ok, failed, breach := (&Syncer{store: st}).verifyAll(rep)
		if breach != nil {
			t.Fatalf("「跑不起来」被当成了违约 ⇒ 中止：%v\n"+
				"  ⇒ 那时每一段落到「本次没走查过」，而那句话是真的，不该中止。", breach)
		}
		if len(ok) != 0 || len(failed) != 0 {
			t.Errorf("跑不起来时它仍然填了结论：ok=%v failed=%v", ok, failed)
		}
		if len(rep.TruncatedTails) == 0 {
			t.Error("跑不起来而 TruncatedTails 里没有痕迹 ⇒ 调用方分不出它和「Store 违约」")
		}
	})
}
