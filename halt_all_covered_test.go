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

// —— 这一条守的是【`HaltNoTradingDays` 与 `HaltAllCovered` 在调用方那里分得开】——
//
// 🔴 它们在调用方能读到的**每一个数上都相同**：`err=nil` · `Bars=0` ·
// `Gaps` 非空 · `Complete()` 为真。⇒ 少了这一档，两种状态**共用一句话**，
// 而它们的处置相反：
//
//	HaltNoTradingDays  请求区间里一天都不交易   ⇒ 换一个区间
//	HaltAllCovered     交易日有，而已经拉过     ⇒ 什么都不用做
//
// ⛔ 而这一格是**评审方 2026-09-12 点名要的**，理由是我们刚在 `tierOf` 那一格上付过学费：
// **「两档共用一个数」这件事，光靠一段注释挡不住** —— 注释不会红。
//
// ⚠️ 而分开它们的不只是 `Halt` 那个枚举值：**`SkippedCovered` 必须跟着分岔** ——
// 「跳过了 N 天」与「一天都没得跳」在报告上要看得出来，否则枚举值是唯一的判别符，
// 而一个枚举值**不带任何解释**。
// guard: HaltNoTradingDays 与 HaltAllCovered 在调用方那里必须分得开。
func TestHaltNoTradingDaysAndAllCoveredAreDistinguishable(t *testing.T) {
	const (
		d1 = tickflow.TradingDay(20200805)
		d2 = tickflow.TradingDay(20200810)
	)
	// 日历里只有这两天 ⇒ [20200806, 20200809] 落在覆盖内，而一天都不交易。
	cal, err := embedded.New([]tickflow.TradingDay{d1, d2})
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
	src := seamSource{give: map[tickflow.TradingDay]bool{d1: true, d2: true}}
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
	base := tickflow.SyncRequest{
		Symbol: tickflow.Symbol{Exchange: "SHFE", Product: "rb", YearMon: 2101},
		Period: tickflow.Daily,
	}
	now := time.Date(2020, 9, 1, 0, 0, 0, 0, time.UTC).UnixMilli()

	// 先把 [d1..d2] 拉进来，好让第二格有「已覆盖」可跳。
	r0 := base
	r0.From, r0.To = d1, d2
	rep0, err := syn.Sync(context.Background(), r0, now)
	if err != nil || rep0.Bars != 2 {
		t.Fatalf("前提没成立：打底那次应当干净落盘 2 根，实得 err=%v Bars=%d", err, rep0.Bars)
	}
	// ⛔ 前提自检：打底那次**自己不许**跳过任何东西 —— 否则下面两格比的是同一种状态。
	if len(rep0.SkippedCovered) != 0 {
		t.Fatalf("打底那次就跳过了东西：%v ⇒ 构造不成立", rep0.SkippedCovered)
	}

	t.Run("一 区间里一天都不交易", func(t *testing.T) {
		r := base
		r.From, r.To = 20200806, 20200809 // 都在日历覆盖内，而都不是交易日
		rep, err := syn.Sync(context.Background(), r, now)
		if err != nil {
			t.Fatalf("没什么可同步不是错误：%v", err)
		}
		if rep.Halt != tickflow.HaltNoTradingDays {
			t.Fatalf("Halt = %v，期望 HaltNoTradingDays —— 构造不成立，下面那句证不了什么", rep.Halt)
		}
		// 🔴 判别符二：这一档**一天都没得跳** ⇒ 那一栏必须是空的。
		if len(rep.SkippedCovered) != 0 {
			t.Errorf("「一天都不交易」这一档报了跳过：%v\n"+
				"  ⇒ 那会让它和「全都拉过了」共用同一张脸，而两者的处置相反"+
				"（一个换区间，一个什么都不用做）。", rep.SkippedCovered)
		}
		// 🔴 承重：这一档**必须是干净的结果** —— Complete() 为真、一条痕迹都没有。
		//
		// ⛔ 为什么这一档必须 Complete()：**「请求区间里一天都不交易」是一个干净的结果，
		// 不是一次需要人看一眼的事件**。它一旦留声，`Complete()` 变假，
		// 而兜底那句会说「这份报告没有记录它为什么停下来（可能是它压根没有跑过）」——
		// **那是假话，它明明跑过了**。
		// ⚠️ 而这一条此前只被 t.Logf 印着：实测把 HaltAllCovered 从 note() 的
		// 不留声白名单里拿掉，**全仓 540 个用例一格都没红**（评审方 2026-09-12 打的，我复量）。
		if !rep.Complete() || len(rep.Incidents()) != 0 {
			t.Errorf("这一档留下了痕迹（Complete=%v，痕迹 %v）——\n"+
				"  ⇒ 「请求区间里一天都不交易」是结果不是事件；它一旦留声，\n"+
				"     每一次「只请求了非交易日」的同步都会被报成「没同步完」。",
				rep.Complete(), rep.Incidents())
		}
		t.Logf("一 ⇒ Halt=%v · Bars=%d · 跳过=%v · Complete=%v · 痕迹=%v",
			rep.Halt, rep.Bars, rep.SkippedCovered, rep.Complete(), rep.Incidents())
	})

	t.Run("二 区间里的交易日全部已覆盖", func(t *testing.T) {
		r := base
		r.From, r.To = d1, d2
		rep, err := syn.Sync(context.Background(), r, now)
		if err != nil {
			t.Fatalf("全都拉过了不是错误：%v", err)
		}
		if rep.Halt != tickflow.HaltAllCovered {
			t.Fatalf("Halt = %v，期望 HaltAllCovered", rep.Halt)
		}
		if rep.Bars != 0 {
			t.Errorf("不该再拉到任何根，实得 %d", rep.Bars)
		}
		// 🔴 判别符二：这一档**必须点名跳过了几天**。
		if len(rep.SkippedCovered) == 0 {
			t.Fatalf("跳过了已覆盖的日子，而报告里一条痕迹都没有\n" +
				"  ⇒ 「跳过了 N 天」与「拉了 0 根」共用同一个 Bars=0，" +
				"而它们的处置相反（前者不必管，后者要查源）。")
		}
		// ⚠️ 而「有一条痕迹」不够：它要说得出**跳了几天**，否则它只是另一个枚举值。
		//
		// ⛔ 这一句原来写的是 `strings.Contains(got, "2")` —— **那是恒真的**：
		// 同一句话里有「2020-08-05」。本仓那条（恒真的过滤器）在断言上的形态。
		// ⇒ 判据要带上那个名词：**「跳过 2 个」**，而不是光一个数字。
		if got := rep.SkippedCovered[0]; !strings.Contains(got, "跳过 2 个") {
			t.Errorf("那条痕迹没说出跳过了几天：%q\n"+
				"  ⇒ 它只是换了个说法的枚举值；而 %q 里的日期含着数字，"+
				"只找数字的判据在这里恒真。", got, got)
		}
		// 🔴 承重：这一档**必须是干净的结果** —— Complete() 为真、一条痕迹都没有。
		//
		// ⛔ 为什么这一档必须 Complete()：**「该拉的都拉过了」是一个干净的结果，
		// 不是一次需要人看一眼的事件**。它一旦留声，`Complete()` 变假，
		// 而兜底那句会说「这份报告没有记录它为什么停下来（可能是它压根没有跑过）」——
		// **那是假话，它明明跑过了**。
		// ⚠️ 而这一条此前只被 t.Logf 印着：实测把 HaltAllCovered 从 note() 的
		// 不留声白名单里拿掉，**全仓 540 个用例一格都没红**（评审方 2026-09-12 打的，我复量）。
		if !rep.Complete() || len(rep.Incidents()) != 0 {
			t.Errorf("这一档留下了痕迹（Complete=%v，痕迹 %v）——\n"+
				"  ⇒ 「该拉的都拉过了」是结果不是事件；它一旦留声，\n"+
				"     每一次稳态增量同步都会被报成「没同步完」。",
				rep.Complete(), rep.Incidents())
		}
		t.Logf("二 ⇒ Halt=%v · Bars=%d · 跳过=%v · Complete=%v · 痕迹=%v",
			rep.Halt, rep.Bars, rep.SkippedCovered, rep.Complete(), rep.Incidents())
	})
}
