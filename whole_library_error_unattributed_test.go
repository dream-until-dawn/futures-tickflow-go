package tickflow_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/embedded"
	"github.com/dream-until-dawn/futures-tickflow-go/source/pacing"
	"github.com/dream-until-dawn/futures-tickflow-go/store/segfile"
)

// —— ⛔ 这一条钉住的也是【一个今天就存在的缺陷】：真因在手里，而报文里没有它 ——
//
// 盘上有一条**全库**坏记录（零值 `TradingDay`）时：
//
//	segfile 那一侧   Coverage() 的【每一段】跑 Verify 都报得出真因（本条前提自检会证明这一点）
//	Sync 的报文      两段都只有 `GapStoreUnverified`，而 `TruncatedTails` 是**空的**
//
// 🔴 ⇒ **分得开的信息已经在手里，我们把它丢了** —— 而丢的方式是：
// 这次同步在写盘那一步就 halt 了 ⇒ `touched` 为空 ⇒ `verifyTouched` 一圈都不转
// ⇒ 没有任何一段报出真因 ⇒ 每一段落到 `!verified[sp]` ⇒ 全报「本次没走查」。
//
// ⚠️ 而 `GapStoreUnverified` 这一类的处置写着「**不表示这一段有问题**」——
// **库确实坏了。** ⇒ 那句话在这里**为真而有害**：它把读的人送去「走一遍就行」，
// 而真相是这个库需要人来决定怎么办。
//
// 📎 它与 `false_unverified_test.go` 是**两件事，两份报文**：
//
//	那一条  一次【成功】的多块同步报出伪缺口   ⇒ 病在【键】
//	本条    一次【失败】的同步把真因整个丢掉   ⇒ 病在【谁被走查】
//
// —— ⚠️ 红了有两个方向 ——
//
//	红法一  构造被改坏（零值记录没留住 / 没并成两段）⇒ 前提自检先说话
//	红法二  **有人把它修好了**（两段变成 `GapStoreVerifyFailed`，或 `TruncatedTails` 里有了真因）
//	        ⇒ 那就是 (iv) 落地了（走查绑到【库】上、全库错误归给每一段）
//	        ⇒ 该做的是：删掉这条测试、改 `gap.go` 那段处置的第一、二种情形，
//	          **不是把断言改回去**
//
// ⛔ 射程：本条构造出的是那段处置里的**情形二**（一段都没走查）。
// **情形一**（本次同步中【别的段】报出了全库错误，而没碰过的段拿不到它）本条**没有构造**——
// 它要求「一段被碰过而另一段没有」，而今天每一块都会被 `CommitSpan`，我造不出来。
// ⇒ 那一格留在「核不了的」单子上，别把本条的绿读成两种情形都验过了。

// guard: 全库坏记录 ＋ 一段都没走查 ⇒ 两段都只报「本次没走查」，真因一个字都没印。
func TestWholeLibraryErrorIsNotAttributedToUntouchedSpans(t *testing.T) {
	const (
		a1 = tickflow.TradingDay(20200805)
		a2 = tickflow.TradingDay(20200806)
		gp = tickflow.TradingDay(20200807) // 空档，两段靠它分开
		b1 = tickflow.TradingDay(20200810)
		b2 = tickflow.TradingDay(20200811)
	)
	days := []tickflow.TradingDay{a1, a2, gp, b1, b2}
	cal, err := embedded.New(days)
	if err != nil {
		t.Fatalf("造日历失败：%v", err)
	}
	key := tickflow.ProductKey{Exchange: "SHFE", Product: "rb"}
	dir := t.TempDir()

	s, _, err := segfile.Open(dir, tickflow.Daily)
	if err != nil {
		t.Fatalf("开库失败：%v", err)
	}
	mk := func(d tickflow.TradingDay) tickflow.Bar {
		ts := dayStartMs(d)
		return tickflow.Bar{Ts: ts, TsEnd: ts + 1000, TradingDay: d,
			Open: 1, High: 1, Low: 1, Close: 1, Volume: 1}
	}
	commit := func(from, to tickflow.TradingDay) {
		t.Helper()
		if err := s.AppendBars([]tickflow.Bar{mk(from), mk(to)}); err != nil {
			t.Fatalf("落盘失败：%v", err)
		}
		if err := s.CommitSpan(cal, key,
			tickflow.Span{From: from, To: to, Bars: 2, Days: 2},
			tickflow.OutcomeComplete); err != nil {
			t.Fatalf("登记失败：%v", err)
		}
	}
	commit(a1, a2)
	commit(b1, b2)
	if n := len(s.Coverage()); n != 2 {
		t.Fatalf("前提没成立：期望 2 段，实得 %d 段 %v ⇒ 读数作废", n, s.Coverage())
	}
	if err := s.Close(); err != nil {
		t.Fatalf("关库失败：%v", err)
	}

	// —— 造一条【全库】坏记录：写入口拒收零值，只能绕过它直接写 .dat ——
	var dat string
	if werr := filepath.Walk(dir, func(p string, fi os.FileInfo, e error) error {
		if e == nil && !fi.IsDir() && strings.HasSuffix(p, ".dat") {
			dat = p
		}
		return e
	}); werr != nil {
		t.Fatalf("走一遍库目录失败：%v", werr)
	}
	if dat == "" {
		t.Fatal("没找到 .dat —— 构造作废")
	}
	rec := segfile.EncodeBar(tickflow.Bar{Ts: dayStartMs(b2) + 5, TsEnd: dayStartMs(b2) + 6,
		TradingDay: 0, Open: 1, High: 1, Low: 1, Close: 1, Volume: 1})
	f, err := os.OpenFile(dat, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("打开 .dat 失败：%v", err)
	}
	if _, err := f.Write(rec[:]); err != nil {
		f.Close()
		t.Fatalf("写零值记录失败：%v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("关 .dat 失败：%v", err)
	}

	s2, truncated, err := segfile.Open(dir, tickflow.Daily)
	if err != nil {
		t.Fatalf("重开失败：%v", err)
	}
	t.Cleanup(func() {
		if cerr := s2.Close(); cerr != nil {
			t.Errorf("关库失败：%v", cerr)
		}
	})
	// ⛔ 前提自检一：那条零值记录真的留在盘上（`OpenDat` 会砍半截记录）。
	if truncated != 0 {
		t.Fatalf("前提没成立：重开砍掉了 %d 字节 ⇒ 那条零值记录没留住，读数作废", truncated)
	}
	cov := s2.Coverage()
	if len(cov) != 2 {
		t.Fatalf("前提没成立：重开之后是 %d 段（要 2 段）⇒ 读数作废：%v", len(cov), cov)
	}

	// ⛔ 前提自检二（本条的**要害**）：segfile 那一侧对【每一段】都报得出真因。
	// 🔴 少了它，下面那句「真因被丢了」就没有立足点 ——
	// 「信息不在手里」和「信息在手里而没印」是两件事，而报文里长得一样。
	for i, sp := range cov {
		verr := s2.Verify(sp)
		if verr == nil {
			t.Fatalf("前提没成立：第 %d 段的走查竟然通过了 ⇒ 这个库没坏，读数作废：%v", i, sp)
		}
		if !strings.Contains(verr.Error(), "零值") {
			t.Fatalf("前提没成立：第 %d 段报的不是那条全库错误：%v", i, verr)
		}
	}

	give := map[tickflow.TradingDay]bool{}
	for _, d := range days {
		give[d] = true
	}
	syn, err := tickflow.NewSyncer(tickflow.SyncerConfig{
		Calendar:  cal,
		Store:     s2,
		NewSource: func(*http.Client) tickflow.Source { return seamSource{give: give} },
		Pacer:     pacing.NoPacing(),
		Timeout:   5 * time.Second,
	})
	if err != nil {
		t.Fatalf("造 Syncer 失败：%v", err)
	}
	rep, _ := syn.Sync(context.Background(), tickflow.SyncRequest{
		Symbol: tickflow.Symbol{Exchange: "SHFE", Product: "rb", YearMon: 2101},
		Period: tickflow.Daily, From: a1, To: b2,
	}, dayStartMs(20200901))

	// ⛔ 前提自检三：这一跑真的在写盘那一步停了（＝情形二：一段都没走查）。
	// 若它跑完了，本条量的就是另一种情形，而断言会以一个错的理由绿或红。
	if rep.Halt == tickflow.HaltDone {
		t.Fatalf("前提没成立：这一跑跑完了，而本条要的是「一段都没走查」那一种 ⇒ 读数作废\n"+
			"  Halt=%v Bars=%d gaps=%v", rep.Halt, rep.Bars, rep.Gaps)
	}

	var got []string
	for _, g := range rep.Gaps {
		got = append(got, g.From.String()+".."+g.To.String()+"="+g.Kind.String())
	}
	kindOf := map[tickflow.TradingDay]tickflow.GapKind{}
	for _, g := range rep.Gaps {
		for d := g.From; d <= g.To; d++ {
			kindOf[d] = g.Kind
		}
	}

	// —— 断言一：两段都只拿到「本次没走查」，而库确实坏了 ——
	for _, d := range []tickflow.TradingDay{a1, b1} {
		k, ok := kindOf[d]
		if !ok {
			t.Fatalf("%s 不在任何缺口里 —— 构造变了，读数作废：%s", d, strings.Join(got, " "))
		}
		if k != tickflow.GapStoreUnverified {
			t.Errorf("%s 报成了 %v，而今天它是「本次没走查」。\n"+
				"  这是红法二：有人把它修好了（多半是 (iv)）。\n"+
				"  ⇒ 删掉这条测试，并改 gap.go 里 GapStoreUnverified 那段处置的第一、二种情形，\n"+
				"     不是把断言改回去。实得：%s", d, k, strings.Join(got, " "))
		}
	}

	// —— 断言二（要害）：连真因都没人印 ——
	if len(rep.TruncatedTails) != 0 {
		t.Errorf("TruncatedTails 里有东西了：%v\n"+
			"  今天它是空的 —— 一段都没走查，所以没有任何一段报出真因。\n"+
			"  ⇒ 这是红法二：真因开始被印出来了。改这条测试与那段处置，别把断言改回去。",
			rep.TruncatedTails)
	}
}
