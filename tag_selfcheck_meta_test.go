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

// —— 这一条钉的是【一个改不了的载体上的一句话】——
//
// `v0.4.1` 的 tag 注解里有一节「自查」，方法二逐字是：
//
//	或跑一次 `Sync`，看 `rep.Gaps` 里有没有 `GapStoreUnverified`。
//	⚠️ 而它**只在 `.meta` 还在时有效**：删掉 `.meta` 之后那一段会报成「没拉过」（实测）。
//
// 🔴 **tag 注解改不了。** 文档写错可以改，已发布的 tag 注解不能 ——
// 所以这句话的真值，只能由一颗会喊的钉子护着。
//
// —— ⛔ 它钉的不只是「报成没拉过」，更是【方法二在这种状态下失效】——
//
//	断言一  删掉 .meta 重开 ⇒ coverage 为空
//	断言二  再同步 ⇒ 那一段报成 GapNeverFetched（「没拉过」）
//	断言三  **方法二要人找的 GapStoreUnverified 一个都没有**
//
// 断言三才是那句限定的**要害**：照着方法二做的人会找不到它、
// 于是得出「这个库没问题」—— 而真相是**这次自查没有真值**。
// 📎 本仓那条：**一句处置有三种坏法 —— 为假 · 为真而有害 · 循环**；
// 而那句限定正是为了挡住第二种，所以它自己必须为真。
//
// —— ⚠️ 红了有两个方向，报文要说得出是哪一个 ——
//
//	红法一  构造被改坏（.meta 没删掉 / 第一次同步没落盘）⇒ 前提自检先说话
//	红法二  **有人把它改好了** —— 例如删掉 .meta 之后不再报「没拉过」，
//	        而是报一个专门的「coverage 丢了」类别
//	        ⇒ 那是**好事**，而 v0.4.1 的注解**改不了**
//	        ⇒ 该做的是：改这条测试，并在能改的载体上（release notes / README）
//	          写一条勘误说「v0.4.1 自查方法二那条限定已不适用」，
//	          **不是**把这里调绿了事
//
// —— 📌 排期：它必须落在「会改动它的那一片」之前 ——
//
// 登记它的时候我倾向不做，理由是「丙片会改 fetch 的行为，等于在一个即将变的行为上钉钉子」。
// ⛔ 反了：**钉子的价值恰恰在那次改变发生的那一刻兑现。**
// 现在钉 ⇒ 改的那天它红 ⇒ 有人被迫重取这句话的真值；
// 之后钉 ⇒ 钉的是改完之后的样子，而没有任何东西告诉你它变过 ⇒ 沉默地变假。
//
// ⚠️ 而**我不声称丙片一定会让它红**（那是一句我没量过的话）：`.meta` 被删之后
// coverage 是空的，丙片的「跳过已覆盖」在空 coverage 上跳不掉任何东西。
// 它防的是**任何一次改动**让这三条里的某一条变假 —— 丙片只是其中一个候选。
//
// —— 📐 突变（2026-09-11 实测，两个签名不同 ⇒ 断言二与断言三分得开）——
//
//	L1  classifyTradingDay 的 GapNeverFetched → GapStoreUnverified
//	    ⇒ 红：断言二（没有一段报「没拉过」）· 断言三（仍然报出了 GapStoreUnverified）
//	L2  同一处 → GapConfirmedEmpty
//	    ⇒ 红：**只有**断言二
//	基线（全名核过）：--- PASS: TestTagV041SelfCheckTwoLosesItsMeaningWithoutMeta
//
// ⛔ **断言一（coverage 空了）没有突变落在它身上，写在这儿而不是藏着。**
// 我试的那一格（`.meta` 缺失时 `Open` 直接报错）红在**第一次开库**上，够不到它。
// ⇒ 按「做断言的东西 vs 防御分支」那条分法问它：**删掉断言一，那个失败会变静默吗？**
// 不会 —— coverage 若不再随 `.meta` 消失，断言二会先红。
// 🔴 ⇒ **断言一是【诊断】不是【保证】**：它买的是「红的时候指得出是哪一步变了」，
// 留着的理由就这一条，而不是「它多守住了什么」。

// guard: v0.4.1 注解里「删掉 .meta 之后那一段会报成『没拉过』」这句话必须为真。
func TestTagV041SelfCheckTwoLosesItsMeaningWithoutMeta(t *testing.T) {
	const (
		d1 = tickflow.TradingDay(20200805)
		d2 = tickflow.TradingDay(20200806)
		d3 = tickflow.TradingDay(20200807)
	)
	cal, err := embedded.New([]tickflow.TradingDay{d1, d2, d3})
	if err != nil {
		t.Fatalf("造日历失败：%v", err)
	}
	dir := t.TempDir()

	store, truncated, err := segfile.Open(dir, tickflow.Daily)
	if err != nil {
		t.Fatalf("开库失败：%v", err)
	}
	if truncated != 0 {
		t.Fatalf("前提没成立：新库报了 %d 字节残尾 ⇒ 读数作废", truncated)
	}

	src := seamSource{give: map[tickflow.TradingDay]bool{d1: true, d2: true, d3: true}}
	newSyncer := func(st tickflow.Store) *tickflow.Syncer {
		t.Helper()
		syn, serr := tickflow.NewSyncer(tickflow.SyncerConfig{
			Calendar:  cal,
			Store:     st,
			NewSource: func(*http.Client) tickflow.Source { return src },
			Pacer:     pacing.NoPacing(),
			Timeout:   5 * time.Second,
		})
		if serr != nil {
			t.Fatalf("造 Syncer 失败：%v", serr)
		}
		return syn
	}
	req := tickflow.SyncRequest{
		Symbol: tickflow.Symbol{Exchange: "SHFE", Product: "rb", YearMon: 2101},
		Period: tickflow.Daily,
		From:   d1, To: d3,
	}

	rep1, err := newSyncer(store).Sync(context.Background(), req, dayStartMs(20200901))
	if err != nil {
		t.Fatalf("第一次同步出错：%v", err)
	}

	// ⛔ 前提自检一：第一次同步真的建起了一个【健康的、有 coverage 的】库。
	// 少了它，下面三句会在一个空库上全部「成立」，而那不是这句话说的那件事。
	if rep1.Bars != 3 || len(store.Coverage()) != 1 {
		t.Fatalf("前提没成立：落盘 %d 条、coverage %v（期望 3 条、1 段）⇒ 读数作废",
			rep1.Bars, store.Coverage())
	}
	if err := store.Close(); err != nil {
		t.Fatalf("关库失败：%v", err)
	}

	// —— 那句话说的那个动作：把 .meta 删掉 ——
	var onDisk []string
	if werr := filepath.Walk(dir, func(p string, fi os.FileInfo, e error) error {
		if e == nil && !fi.IsDir() {
			onDisk = append(onDisk, p)
		}
		return e
	}); werr != nil {
		t.Fatalf("走一遍库目录失败：%v", werr)
	}
	removed := 0
	for _, p := range onDisk {
		if strings.HasSuffix(p, ".meta") {
			if rerr := os.Remove(p); rerr != nil {
				t.Fatalf("删 .meta 失败：%v", rerr)
			}
			removed++
		}
	}
	// ⛔ 前提自检二：真的删掉了【一个】.meta。
	// 删掉 0 个（文件名换了）会让这条测试去量一个健康库，而它照样能绿。
	if removed != 1 {
		t.Fatalf("前提没成立：删掉了 %d 个 .meta（期望 1 个）⇒ 读数作废。\n"+
			"  盘上的文件：%v", removed, onDisk)
	}

	store2, _, err := segfile.Open(dir, tickflow.Daily)
	if err != nil {
		t.Fatalf("删掉 .meta 之后重开失败：%v", err)
	}
	t.Cleanup(func() {
		if cerr := store2.Close(); cerr != nil {
			t.Errorf("关库失败：%v", cerr)
		}
	})

	// —— 断言一：coverage 空了 ——
	if cov := store2.Coverage(); len(cov) != 0 {
		t.Fatalf("删掉 .meta 之后 coverage 仍有 %d 段（%v）。\n"+
			"  这是红法二：有人让 coverage 不再只住在 .meta 里。\n"+
			"  ⇒ v0.4.1 注解那句限定随之失效，而 tag 改不了 ——\n"+
			"     请在 release notes 写一条勘误，再改这条测试。", len(cov), cov)
	}

	rep2, _ := newSyncer(store2).Sync(context.Background(), req, dayStartMs(20200901))

	// ⛔ 前提自检三：真的报出了缺口。空的 Gaps 会让下面两句一起空转，
	// 而「一段都没报」和「报对了」在断言三那一侧长得一模一样。
	if len(rep2.Gaps) == 0 {
		t.Fatalf("一段缺口都没报 ⇒ 下面两句是空转的，读数作废。\n"+
			"  报告：Halt=%v Bars=%d", rep2.Halt, rep2.Bars)
	}

	var kinds []string
	sawNeverFetched, sawUnverified := false, false
	for _, g := range rep2.Gaps {
		kinds = append(kinds, g.From.String()+".."+g.To.String()+"="+g.Kind.String())
		switch g.Kind {
		case tickflow.GapNeverFetched:
			sawNeverFetched = true
		case tickflow.GapStoreUnverified:
			sawUnverified = true
		}
	}

	// —— 断言二：那一段报成「没拉过」——
	if !sawNeverFetched {
		t.Errorf("删掉 .meta 之后没有任何一段报成「没拉过」，实得：%s\n"+
			"  ⇒ v0.4.1 注解那句「删掉 .meta 之后那一段会报成『没拉过』」不再为真。\n"+
			"  ⇒ tag 注解改不了：先在 release notes 写勘误，再改这条测试。",
			strings.Join(kinds, " "))
	}

	// —— 断言三（要害）：方法二要人找的那一类，一个都没有 ——
	if sawUnverified {
		t.Errorf("删掉 .meta 之后仍然报出了 GapStoreUnverified，实得：%s\n"+
			"  ⇒ 那意味着 v0.4.1 自查方法二在这种状态下【仍然有效】——\n"+
			"     而注解那句限定说它无效。两者只能有一个为真。\n"+
			"  ⇒ 若是有人把它修好了：那是好事，改这条测试，"+
			"并在 release notes 写一条勘误。", strings.Join(kinds, " "))
	}
}
