package tickflow_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/embedded"
	"github.com/dream-until-dawn/futures-tickflow-go/source/pacing"
	"github.com/dream-until-dawn/futures-tickflow-go/store/segfile"
)

// —— 这一条把【两次写之间崩溃】那条路从头走一遍，并断言**侦测出声** ——
//
// ⛔ 由来（评审方 2026-09-12 点名要的）：丙片让这条路上的 `Sync` **不再返回错误**，
// 而那是 2026-09-12 那条裁决的正常结果（`err` ＝「这一次动作本身没做成」，
// 库的状态走 `Complete()` ＋ `Gaps`）。⇒ 所以侦测那一侧必须被单独钉住。
//
// —— 这一格造的到底是什么 ——
//
//	① `Sync [d1..d2]` 干净落盘 ⇒ coverage = [d1,d2]
//	② 注入让 `CommitSpan` 失败一次（**模拟两次写之间崩溃**：`AppendBars` 与
//	   `CommitSpan` 是两次写，本仓没有原子性）⇒ 盘上多出 d3，而 coverage 里没有它 ＝ 孤儿
//	③ 再同步一次（走丙片那条路：d1,d2 被跳过，只拉 d3）
//
// ⇒ ③ 之后盘上有 **4** 条记录，而 coverage 声称 `[d1..d3] Bars=3`。
// 🔴 **元数据从「沉默」变成了「说了一句假话」** —— 改前那颗库不声称拥有 d3，改后它声称。
//
// —— 为什么这个不一致不会自愈（机械理由，评审方给的，我复核属实）——
//
// `NormalizeCoverage` 合并相邻段时是 `last.Bars += s.Bars` —— **累加，从不重数**
// ⇒ 那个 3 会一直是 3。而侦测活下来靠的是 `Verify` **自己数文件**，不信 `.meta` 里那个数。
//
// —— ⚠️ 硬要求：「没走查过」与「数对不上」必须分得开 ——
//
// 我们在 `GapStoreVerifyUnrun` / `GapStoreVerifyFailed` 上已经为这件事付过一次学费。
// ⇒ 这一格**同时**断两层：
//
//	缺口的**类别**    必须是「走查没通过」，不是「没走查过」
//	报告里的**真因**  必须点名那两个数（4 与 3），而不是一句泛泛的「这一段有问题」
//
// —— ⛔ 而写这一格时撞见一个【错标签】，登记为 (u)，本片不改 ——
//
// 走查失败那条留声今天写进的是 `rep.TruncatedTails`，而 `Incidents()` 给它加的前缀是
// **「开库时截过残尾：」**。实测这一格印出来的 `Incidents()` 原文：
//
//	开库时截过残尾：走查 2020-08-05..2020-08-07 失败：
//	segfile: bars 与走查数出来的对不上: 走查数出 4 条，而 .meta 记的是 3 条
//
// 🔴 **一条走查失败，被宣布成「开库时截过残尾」** —— 那两件事的处置毫不相干。
// 📎 本仓那条（两种状态共用一个载体）最难看的一格：**这次载体上还贴着另一件事的名字。**
// ⚠️ 而它**不是丙片引入的**（`verifyAll` 一直写在那儿），所以不在本片改 ——
// 改它要给 `SyncReport` 加一栏（`VerifyFailures`）并动 `Incidents()`，
// 那是一次公开结构体的变更，且有三处测试在读这一栏，**该单独一片走评审**。
//
// ⚙ 而这一格**照旧读 `TruncatedTails`**：断言要跟着今天的事实走，
// 不跟着我们希望的样子走 —— (u) 落地那天，这一行跟着改。

// guard: 两次写之间崩溃 ⇒ Sync 不报错，而侦测必须出声并点名那两个数。
func TestCrashBetweenWritesIsDetectedEvenThoughSyncSucceeds(t *testing.T) {
	const (
		d1 = tickflow.TradingDay(20200805)
		d2 = tickflow.TradingDay(20200806)
		d3 = tickflow.TradingDay(20200807)
	)
	cal, err := embedded.New([]tickflow.TradingDay{d1, d2, d3})
	if err != nil {
		t.Fatalf("造日历失败：%v", err)
	}
	store, truncated, err := segfile.Open(t.TempDir(), tickflow.Daily)
	if err != nil {
		t.Fatalf("开库失败：%v", err)
	}
	if truncated != 0 {
		t.Fatalf("新库不该有残尾，却报了 %d —— 前提不成立，读数作废", truncated)
	}
	t.Cleanup(func() {
		if cerr := store.Close(); cerr != nil {
			t.Errorf("关库失败：%v", cerr)
		}
	})
	src := seamSource{give: map[tickflow.TradingDay]bool{d1: true, d2: true, d3: true}}
	failing := &commitFailsOnce{Store: store}
	syn, err := tickflow.NewSyncer(tickflow.SyncerConfig{
		Calendar:  cal,
		Store:     failing,
		NewSource: func(*http.Client) tickflow.Source { return src },
		Pacer:     pacing.NoPacing(),
		Timeout:   5 * time.Second,
	})
	if err != nil {
		t.Fatalf("造 Syncer 失败：%v", err)
	}
	base := tickflow.SyncRequest{
		Symbol: tickflow.Symbol{Exchange: "SHFE", Product: "rb", YearMon: 2101},
		Period: tickflow.Daily,
	}
	now := time.Date(2020, 9, 1, 0, 0, 0, 0, time.UTC).UnixMilli()

	// ① 打底。
	r1 := base
	r1.From, r1.To = d1, d2
	if rep, err := syn.Sync(context.Background(), r1, now); err != nil || rep.Bars != 2 {
		t.Fatalf("前提没成立：打底那次应当干净落盘 2 根，实得 err=%v Bars=%d", err, rep.Bars)
	}

	// ② 两次写之间崩溃。
	commitsBefore := failing.commits
	failing.armed = true
	r2 := base
	r2.From, r2.To = d3, d3
	if _, err := syn.Sync(context.Background(), r2, now); err == nil {
		t.Fatalf("前提没成立：注入之后这一次应当失败（CommitSpan 没成），实得 err=nil")
	}
	failing.armed = false
	if n := failing.commits - commitsBefore; n != 1 {
		t.Fatalf("前提没成立：这一步里 CommitSpan 应当被调 1 次（注入才算施加），实得 %d 次", n)
	}
	if n := len(store.Coverage()); n != 1 {
		t.Fatalf("前提没成立：coverage 应当仍是 1 段（孤儿没登记进去），实得 %d 段：%v",
			n, store.Coverage())
	}

	// ③ 再同步一次 —— 走丙片那条路。
	r3 := base
	r3.From, r3.To = d1, d3
	rep, err3 := syn.Sync(context.Background(), r3, now)
	// ⚠️ 把 Incidents() 一起印出来：**Complete() ＝ len(Incidents()) == 0**，
	// 而 Gaps **不在** Incidents() 里 ⇒ 这一格的「Complete() 为假」靠的是那条留声，不是缺口。
	t.Logf("③ err=%v · Bars=%d · Halt=%v · Complete=%v\n   跳过=%v\n   缺口=%v\n   留声=%v\n   痕迹=%v",
		err3, rep.Bars, rep.Halt, rep.Complete(),
		rep.SkippedCovered, rep.Gaps, rep.TruncatedTails, rep.Incidents())

	// 🔴 承重一：`Sync` **不报错** —— 这是 2026-09-12 裁决的正常结果，不是缺陷。
	// ⛔ 而写下它是必须的：少了这一句，下一个人会以为「它该报错」而去把裁决改回去。
	if err3 != nil {
		t.Errorf("这一次同步本身没做错任何事，而它报了错：%v\n"+
			"  ⇒ 按 2026-09-12 的裁决，err ＝「这一次动作本身没做成」，\n"+
			"     库的历史状态走 Complete() ＋ Gaps。", err3)
	}
	// 🔴 承重二：**而它绝不许说这个库是好的。**
	if rep.Complete() {
		t.Fatalf("报告说 Complete() —— 而盘上有一批不属于任何已提交段的记录。\n" +
			"  ⇒ 这正是「err 不管库」这条裁决唯一的兜底：Complete() 必须为假。")
	}
	// 🔴 承重三：缺口的**类别**要说「走查没通过」，不是「没走查过」。
	found := false
	for _, g := range rep.Gaps {
		if strings.Contains(g.Kind.String(), "走查没通过") {
			found = true
		}
	}
	if !found {
		t.Errorf("缺口里没有一条说「走查没通过」：%v\n"+
			"  ⇒ 「没走查过」（走查根本没跑）与「走查没通过」（跑了而数对不上）"+
			"处置相反，不许共用一句话。", rep.Gaps)
	}
	// 🔴 承重四：报告里的**真因**要点名那两个数。
	//
	// ⚠️ 判据写成「两个数都出现」，而不是找「对不上」三个字 ——
	// 那句话可以被改写，而**那两个数是这件事本身**。
	joined := strings.Join(rep.TruncatedTails, "\n")
	for _, want := range []string{"4 条", "3 条"} {
		if !strings.Contains(joined, want) {
			t.Errorf("报告里没点名 %q —— 真因没走到调用方手里：\n%s\n"+
				"  ⇒ 只说「这一段有问题」的话，调用方分不出是「少写了」还是「多写了」。",
				want, joined)
		}
	}
	// ⛔ 前提自检：那几行留声真的存在 —— 空切片会让上面那个循环报一个误导的错。
	if len(rep.TruncatedTails) == 0 {
		t.Fatalf("一条留声都没有 ⇒ 上面那两句断言在问一个空字符串，读数作废")
	}
}
