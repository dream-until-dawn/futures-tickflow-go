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

// —— 这一条守的是【一条全库坏记录，要归给每一段】——
//
// ⛔ 由来：上一版这里钉的是**缺陷本身** —— 盘上有一条全库坏记录时，
// 两段都只拿到 `GapStoreUnverified`，而 `TruncatedTails` 是**空的**：
// 分得开的信息已经在手里（segfile 那侧每一段的 `Verify` 都报得出真因），我们把它丢了。
// 那颗钉子在 (iv) 落地时如约响了，于是按它报文里写死的红法二处置：
// **改这条测试，不是把断言改回去。**
//
// 根因是**走查绑在「本次碰过什么」上**：那一跑在写盘那一步就 halt 了 ⇒ `touched` 为空
// ⇒ 一圈都不转 ⇒ 每一段落到 `!verified[sp]`。
// ⇒ (iv) 把走查绑到**库本身**（`VerifyCoverage` 一遍扫描核算每一段），于是：
//
//	全库错误（零值 / 顺序 / 谁的段都不属于）在第一条坏记录上中止整遍，**而它归给每一段**
//	⇒ 两段都报 `GapStoreVerifyFailed`，且真因对**每一段**各印一条
//
// 🔴 而这不是「保持了今天的好行为」—— **今天根本没有那个行为**：
// 今天只有 `touched` 的段拿得到真因，没碰过的段拿到的是
// 「本次没走查过，**不表示这一段有问题**」，**而库确实有问题**。
//
// —— ⚠️ 前提自检那一道是本条的要害，别删 ——
//
// 它先证明 **segfile 那侧每一段的 `Verify` 都报得出真因**。
// 🔴 少了它，「真因归给了每一段」就没有立足点 ——
// **「信息不在手里」与「信息在手里而没归对」在报文里长得一样。**
//
// —— ⚠️ 红了有两个方向 ——
//
//	红法一  构造被改坏（坏记录没留住 / 没并成两段 / 那一跑没 halt）⇒ 前提自检先说话
//	红法二  **有人把归属改回去了**（某一段又拿不到真因）⇒ 那是回归，
//	        去看 `VerifyCoverage` 里那句「`whole != nil` ⇒ 归给每一段」
//
// ⛔ 射程：本条构造的是**一段都没走查过**那一种（`touched` 空）。
// 它现在之所以仍然能拿到真因，正是因为走查**不再看 `touched`** —— 那就是本条钉的那件事。

// guard: 一条全库坏记录要归给【每一段】—— 两段都报「走查没通过」，且真因各印一条。
func TestWholeLibraryErrorIsAttributedToEverySpan(t *testing.T) {
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

	// —— 断言一：两段都拿到「走查没通过」，而不是「本次没走查」——
	for _, d := range []tickflow.TradingDay{a1, b1} {
		k, ok := kindOf[d]
		if !ok {
			t.Fatalf("%s 不在任何缺口里 —— 构造变了，读数作废：%s", d, strings.Join(got, " "))
		}
		if k != tickflow.GapStoreVerifyFailed {
			t.Errorf("%s 报成了 %v，期望「走查没通过」。\n"+
				"  ⇒ 若是「本次没走查」：全库错误没有归给这一段，那是回归 ——\n"+
				"     去看 VerifyCoverage 里那句「whole != nil 时归给每一段」。\n"+
				"  实得：%s", d, k, strings.Join(got, " "))
		}
	}

	// —— 断言二（要害）：真因对【每一段】各印一条 ——
	//
	// 🔴 只断言「有东西」不够：一条痕迹也满足它，而那正是上一版的病
	// （只有 touched 的段拿得到真因）。所以按段数点名。
	if len(rep.TruncatedTails) != len(cov) {
		t.Errorf("TruncatedTails 有 %d 条，而 coverage 有 %d 段——期望每一段各一条。\n"+
			"  ⇒ 少了：有段没拿到真因（回归）；多了：同一段被走查了不止一次。\n"+
			"  实得：%v", len(rep.TruncatedTails), len(cov), rep.TruncatedTails)
	}
	joined := strings.Join(rep.TruncatedTails, " | ")
	for _, sp := range cov {
		want := sp.From.String() + ".." + sp.To.String()
		if !strings.Contains(joined, want) {
			t.Errorf("痕迹里没有 %s 那一段：\n  %s\n"+
				"  ⇒ 这一段没拿到真因，而库是坏的。", want, joined)
		}
	}
	if !strings.Contains(joined, "零值") {
		t.Errorf("痕迹里没有那条全库错误的真因（「零值」）：\n  %s", joined)
	}
}
