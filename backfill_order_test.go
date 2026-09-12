package tickflow_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/embedded"
	"github.com/dream-until-dawn/futures-tickflow-go/source/pacing"
	"github.com/dream-until-dawn/futures-tickflow-go/store/segfile"
)

// —— 这条守的是【一次合法调用序】不会毁掉一个已经走查过的段 ——
//
// ⛔ 它是那个「有序性窗口」的**生产路径形态**，而它今天是一个【现存缺陷】，
// 不是一个将来的风险 —— 这一句是量出来的（2026-09-11），对照如下：
//
//	                    main（写入口没有顺序检查）        加上顺序检查之后
//	② 报错点            CommitSpan「coverage 未按 From 升序」  **AppendBars「交易日倒退了」**
//	那两条记录落盘了吗   **是**                              **否**
//	③ Verify(第一段)     **红**「第 2 条是 0805，而上一条是 0811」  **绿**
//
// 🔴 ⇒ 在 main 上，一次**合法的**倒填调用会留下：
//
//	一、盘上两条**不属于任何已提交段**的记录（没有任何东西会去清它们）
//	二、一个**本来走查得过**的段从此走查不过 ⇒ 下一次同步它会被报成 `GapStoreVerifyUnrun`
//	三、而报告写着 `Bars=0` —— **说没写，而盘上写了**
//
// ⚠️ 第三条是本仓最怕的那个形状。而它**不是** CommitSpan 的错：
// CommitSpan 挡住了 coverage（那是它的职责，它做对了），
// **而它挡不住已经发生的那次写** —— 写在它之前。
//
// —— 为什么窗口需要【两次】Sync ——
//
// 一次 Sync 内的顺序是 `fetch`（AppendBars → CommitSpan）→ `verifyTouched` → `planGaps`
// ⇒ 走查在追加**之后** ⇒ 单次 Sync 走不到那个窗口。
// 而 `req.From` 是公开 API ⇒ 「先同步晚的、再同步早的」是一条**合法调用序**。

// guard: 把同一次 Sync 跑两遍不得毁掉这个库 —— 而这是【最常见】的那个入口。
//
// ⛔ **它的由来是一次对照把我的归因整个推翻了**（2026-09-11）：
// 我原以为触发条件是「先同步晚的、再同步早的」（倒填）。而做对照时只动一个变量
// ——**同一个健康库、同一个区间、跑两遍**——读数如下：
//
//	                     main（今天）                      本片
//	② 第二次 Sync        CommitSpan「coverage 有重叠段」     AppendBars「交易日倒退了」
//	③ Verify(那一段)      **红**「第 2 条是 0810，上一条是 0811」  **绿**
//
// 🔴 成因一句话：**第二次把同样的记录又追加了一遍**（0810 接在 0811 之后 ⇒ 倒退），
// `CommitSpan` 拒了重叠 —— **而记录已经在盘上**。
// ⇒ 于是**触发条件比倒填宽得多**：任何一次「取回的记录不严格晚于盘上已有的」都算，
// **而「把同一条命令再跑一遍」正是其中最平常的那一种。**
//
// ⚠️ 而本片**没有**让重跑变成幂等 —— 它让重跑**大声失败**而不是**静默毁库**。
// 「重跑该不该是空操作」是另一个设计决定（要不要在 `fetch` 之前就跳过已覆盖的区间），
// 我没有替它做主。⇒ **这条测试断言的是「库仍然完好」，而那句话在两种设计下都该成立。**
func TestResyncTwiceDoesNotPoisonTheStore(t *testing.T) {
	days := []tickflow.TradingDay{20200805, 20200806, 20200807, 20200810, 20200811, 20200812}
	cal, err := embedded.New(days)
	if err != nil {
		t.Fatalf("造日历失败：%v", err)
	}
	store, _, err := segfile.Open(t.TempDir(), tickflow.Daily)
	if err != nil {
		t.Fatalf("开库失败：%v", err)
	}
	t.Cleanup(func() {
		if cerr := store.Close(); cerr != nil {
			t.Errorf("关库失败：%v", cerr)
		}
	})
	src := seamSource{give: map[tickflow.TradingDay]bool{20200810: true, 20200811: true}}
	syn, err := tickflow.NewSyncer(tickflow.SyncerConfig{
		Calendar:  cal,
		Store:     store,
		NewSource: func(*http.Client) tickflow.Source { return src },
		Pacer:     pacing.NoPacing(),
		Timeout:   5 * time.Second,
	})
	if err != nil {
		t.Fatalf("造 Syncer 失败：%v", err)
	}
	req := tickflow.SyncRequest{
		Symbol: tickflow.Symbol{Exchange: "SHFE", Product: "rb", YearMon: 2101},
		Period: tickflow.Daily, From: 20200810, To: 20200811,
	}
	now := dayStartMs(20200901)

	rep1, err := syn.Sync(context.Background(), req, now)
	if err != nil {
		t.Fatalf("第一次同步不该出错：%v", err)
	}
	if rep1.Bars == 0 {
		t.Fatalf("第一次同步落盘 0 条 —— 构造不成立（报告：%+v）", rep1)
	}
	cov := store.Coverage()
	if len(cov) != 1 {
		t.Fatalf("期望一段 coverage，实得 %v —— 构造不成立", cov)
	}
	good := cov[0]
	// 前提自检：它此刻走查得过 —— 这是下面那句的**对照端点**。
	if verr := store.Verify(good); verr != nil {
		t.Fatalf("第一次同步之后就走查不过：%v —— 下面那句什么也证不了", verr)
	}

	// ⇒ **把同一条命令再跑一遍。**
	rep2, err2 := syn.Sync(context.Background(), req, now)
	t.Logf("第二次：err=%v · Bars=%d · Halt=%v", err2, rep2.Bars, rep2.Halt)

	// 🔴 承重的那一句：**不管第二次成不成功，这个库必须仍然完好。**
	// ⚠️ 这里**故意不断言第二次的错误值** —— 若哪天重跑被改成空操作（err=nil），
	// 这条测试应当仍然绿。**断言后果，不断言当前的实现方式。**
	if verr := store.Verify(good); verr != nil {
		t.Errorf("把同一次同步跑两遍之后，这一段走查不过了：%v\n"+
			"  ⇒ 第二次把同样的记录又追加了一遍（交易日倒退），而 CommitSpan 拒了重叠 ——\n"+
			"     记录却已经在盘上。⇒ 一次【最平常的重跑】毁掉了这个库。", verr)
	}
}

// guard: 一次合法的倒填调用不得毁掉已走查的段 —— 有序性窗口的生产路径形态。
func TestBackfillAfterVerifyDoesNotPoisonTheStore(t *testing.T) {
	days := []tickflow.TradingDay{20200805, 20200806, 20200807, 20200810, 20200811, 20200812}
	cal, err := embedded.New(days)
	if err != nil {
		t.Fatalf("造日历失败：%v", err)
	}
	store, truncated, err := segfile.Open(t.TempDir(), tickflow.Daily)
	if err != nil {
		t.Fatalf("开库失败：%v", err)
	}
	if truncated != 0 {
		t.Fatalf("新库不该有残尾，却报了 %d —— 前提不成立", truncated)
	}
	t.Cleanup(func() {
		if cerr := store.Close(); cerr != nil {
			t.Errorf("关库失败：%v", cerr)
		}
	})

	src := seamSource{give: map[tickflow.TradingDay]bool{
		20200805: true, 20200806: true, 20200810: true, 20200811: true,
	}}
	syn, err := tickflow.NewSyncer(tickflow.SyncerConfig{
		Calendar:  cal,
		Store:     store,
		NewSource: func(*http.Client) tickflow.Source { return src },
		Pacer:     pacing.NoPacing(),
		Timeout:   5 * time.Second,
	})
	if err != nil {
		t.Fatalf("造 Syncer 失败：%v", err)
	}
	mk := func(from, to tickflow.TradingDay) tickflow.SyncRequest {
		return tickflow.SyncRequest{
			Symbol: tickflow.Symbol{Exchange: "SHFE", Product: "rb", YearMon: 2101},
			Period: tickflow.Daily, From: from, To: to,
		}
	}
	now := dayStartMs(20200901)

	// ① 先同步【晚】的那一段，它会被 verifyTouched 走查过。
	rep1, err := syn.Sync(context.Background(), mk(20200810, 20200811), now)
	if err != nil {
		t.Fatalf("第一次同步不该出错：%v", err)
	}
	// 前提自检：这一段真的落了根 —— 否则后面两句都在问一个空库。
	if rep1.Bars == 0 {
		t.Fatalf("第一次同步落盘 0 条 —— 构造不成立，读数作废（报告：%+v）", rep1)
	}
	cov := store.Coverage()
	if len(cov) != 1 {
		t.Fatalf("期望一段 coverage，实得 %v —— 构造不成立", cov)
	}
	good := cov[0]
	// 前提自检：它此刻走查得过 —— 这是下面那句「仍然走查得过」的**对照端点**。
	if verr := store.Verify(good); verr != nil {
		t.Fatalf("第一段此刻就走查不过：%v —— 那下面那句断言什么也证不了", verr)
	}

	// ② 再同步【早】的那一段：这是一次**合法调用**，而它要往盘上写更早的记录。
	rep2, err2 := syn.Sync(context.Background(), mk(20200805, 20200806), now)
	t.Logf("倒填那次：err=%v · Bars=%d · Halt=%v", err2, rep2.Bars, rep2.Halt)

	// ⛔ **承重的两句，而它们守的是不同的东西。**
	//
	// 🔴 第一句：那次失败必须发生在【写之前】。
	// 少了它，一个「写完再报错」的实现也能让第二句之外的一切看起来正常 ——
	// 而盘上已经多了两条谁的段都不属于的记录。
	if !errors.Is(err2, segfile.ErrOutOfOrder) {
		t.Errorf("倒填那次给的是 %v，而该给 segfile.ErrOutOfOrder\n"+
			"  ⇒ 若它是「coverage 未按 From 升序」，那是 CommitSpan 挡的 ——\n"+
			"     而 CommitSpan 在 AppendBars 【之后】：那两条记录已经在盘上了。", err2)
	}

	// 🔴 第二句：那个**本来走查得过**的段，必须**仍然**走查得过。
	// 这一句才是这条测试的名字所指 —— 前一句是手段，这一句是后果。
	if verr := store.Verify(good); verr != nil {
		t.Errorf("倒填失败之后，第一段走查不过了：%v\n"+
			"  ⇒ 一次【失败的】调用毁掉了一个已经好了的段：\n"+
			"     盘上留下不属于任何已提交段的记录，下一次同步会把这一段报成"+
			"「存储答不了·走查没跑成」，而报告里那次的 Bars 是 0（说没写，而写了）。", verr)
	}
}
