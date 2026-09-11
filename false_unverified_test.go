package tickflow_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/embedded"
	"github.com/dream-until-dawn/futures-tickflow-go/source/pacing"
	"github.com/dream-until-dawn/futures-tickflow-go/store/segfile"
)

// —— ⛔ 这一条钉住的是【一个今天就存在的生产缺陷】，不是一条性质 ——
//
// **一次完全成功的同步，会把它刚拉好的整段报成「存储答不了·未走查」** ——
// 只要请求区间比 `BatchDays` 长（即：不止一块）。
//
// 根因是一个**键**，不是一个集合：
//
//	sync.go   span := Span{From: chunk[0], To: chunk[len-1], Bars: len(bars), Days: distinctDays(bars)}
//	          touched = append(touched, span)      ⇒ verified 的键 ＝【分块】的 span
//	          verifyTouched(touched) ⇒ verified[{d1,d1,1,1}] = true …
//	sync.go   for _, sp := range s.store.Coverage()  ⇒ 查的键 ＝【并段之后】的 span
//	          case !verified[sp]: st.Err = ErrSpanUnverified
//
// `CommitSpan` 把相邻块并成一段 ⇒ `Coverage()` 给 `{d1,d3,3,3}`，
// 而 `verified` 里躺着 `{d1,d1,1,1} {d2,d2,1,1} {d3,d3,1,1}`。
// 🔴 **`Span` 是四字段结构体，做 map 键时这两组值不相等** —— 两处代码都写着 `verified[sp]`，
// **长得一模一样**。
//
// ⚠️ 它不是理论缺陷：`source/cffexsource/client.go` 的 `BatchDays` 就是 **1**
// ⇒ 那个源上任何跨天的同步都中招。而 `source/sinasource` 是 `BatchDaysUnbounded`（永远一块）
// ⇒ **一个必然中招、一个必然不中招** —— 拿后者试的人什么都看不见。
//
// —— ⛔ 后果比「多报一条缺口」重，因为那条缺口的处置是【循环】的 ——
//
// `GapStoreUnverified` 的处置写着「瞬时，自动可解 —— 走一遍就行」。而照做：
// 第二次 `Sync` 不看 coverage 就重拉 ⇒ 落盘撞倒序 ⇒ `Halt=落盘失败`（实测，健康库上也一样）。
// ⇒ **用户被告知「再跑一次就好」，而再跑一次会撞墙。**
//
// —— ⚠️ 红了有两个方向，报文要说得出是哪一个 ——
//
//	红法一  构造被改坏（块数不对 / 同步没跑完）⇒ 前提自检先说话
//	红法二  **有人把它修好了**（多块同步不再报这一类）⇒ 那就是 (iv) 落地了
//	        ⇒ 该做的是：删掉这条测试、改 `gap.go` 那段处置里的第三种情形，
//	          **不是把断言改回去**
//
// 🔴 **这条测试断言的是「一个缺陷今天存在」** —— 它最容易被下一个人顺手调绿。
// 所以说死：**绿→红的那一刻是【修好了】。**

// oneChunkPerDaySource 把 `BatchDays` 压成 1 —— 一天一块。
//
// ⚠️ 它只改这一个开关，**别的能力与 `seamSource` 逐字相同**：
// 本条测试的全部判别力都压在「块数」这一个变量上。
type oneChunkPerDaySource struct{ seamSource }

func (s oneChunkPerDaySource) Caps(k tickflow.ProductKey) tickflow.Capabilities {
	c := s.seamSource.Caps(k)
	c.BatchDays = 1
	return c
}

// guard: 多块同步今天会把刚拉好的整段报成「未走查」—— 钉住这个缺陷，(iv) 落地那天它必红。
func TestSuccessfulMultiChunkSyncStillReportsUnverified(t *testing.T) {
	const (
		d1 = tickflow.TradingDay(20200805)
		d2 = tickflow.TradingDay(20200806)
		d3 = tickflow.TradingDay(20200807)
	)

	// run 跑一次同步，回（报告，coverage）。days 是日历与请求区间，batch 决定切几块。
	run := func(t *testing.T, days []tickflow.TradingDay, oneDayBatch bool) (tickflow.SyncReport, []tickflow.Span) {
		t.Helper()
		cal, err := embedded.New(days)
		if err != nil {
			t.Fatalf("造日历失败：%v", err)
		}
		store, truncated, err := segfile.Open(t.TempDir(), tickflow.Daily)
		if err != nil {
			t.Fatalf("开库失败：%v", err)
		}
		if truncated != 0 {
			t.Fatalf("前提没成立：新库报了 %d 字节残尾 ⇒ 读数作废", truncated)
		}
		t.Cleanup(func() {
			if cerr := store.Close(); cerr != nil {
				t.Errorf("关库失败：%v", cerr)
			}
		})
		give := map[tickflow.TradingDay]bool{}
		for _, d := range days {
			give[d] = true
		}
		base := seamSource{give: give}
		var src tickflow.Source = base
		if oneDayBatch {
			src = oneChunkPerDaySource{base}
		}
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
		rep, serr := syn.Sync(context.Background(), tickflow.SyncRequest{
			Symbol: tickflow.Symbol{Exchange: "SHFE", Product: "rb", YearMon: 2101},
			Period: tickflow.Daily,
			From:   days[0], To: days[len(days)-1],
		}, dayStartMs(20200901))
		if serr != nil {
			t.Fatalf("同步出错：%v", serr)
		}
		// ⛔ 前提自检：这一跑必须是【完全成功】的。
		// 少了它，下面每一格都可能在量「halt 之后的报告」，而那是另一个缺陷。
		if rep.Halt != tickflow.HaltDone {
			t.Fatalf("前提没成立：Halt=%v（要「跑完」）⇒ 读数作废", rep.Halt)
		}
		if rep.Bars != len(days) {
			t.Fatalf("前提没成立：落盘 %d 条，期望 %d 条 ⇒ 读数作废", rep.Bars, len(days))
		}
		return rep, store.Coverage()
	}

	kinds := func(rep tickflow.SyncReport) []tickflow.GapKind {
		var out []tickflow.GapKind
		for _, g := range rep.Gaps {
			out = append(out, g.Kind)
		}
		return out
	}
	sawUnverified := func(rep tickflow.SyncReport) bool {
		for _, g := range rep.Gaps {
			if g.Kind == tickflow.GapStoreUnverified {
				return true
			}
		}
		return false
	}

	// —— ⛔ 两格标定，把【除块数以外】的变量各钉一次 ——
	//
	// 评审方 2026-09-11 指出我第一版对照组不够：我用「3 天三块」对「1 天一块」，
	// **区间长度跟着块数一起变了**。
	// 📎 收：**两端标定不是「一个正例一个反例」，是把每一个你以为无关的变量各钉一次。**

	t.Run("标定甲 同样三天而只有一块_不报这一类", func(t *testing.T) {
		rep, cov := run(t, []tickflow.TradingDay{d1, d2, d3}, false)
		if sawUnverified(rep) {
			t.Fatalf("三天一块也报了这一类 ⇒ 块数不是那个变量，本条的归因不成立：\n"+
				"  coverage=%v gaps=%v", cov, kinds(rep))
		}
	})

	t.Run("标定乙 同样BatchDays而只有一天_不报这一类", func(t *testing.T) {
		rep, cov := run(t, []tickflow.TradingDay{d1}, true)
		if sawUnverified(rep) {
			t.Fatalf("BatchDays=1 而只有一天（仍是一块）也报了这一类 ⇒ 归因不成立：\n"+
				"  coverage=%v gaps=%v", cov, kinds(rep))
		}
	})

	t.Run("被测 三天三块_报出一条伪缺口", func(t *testing.T) {
		rep, cov := run(t, []tickflow.TradingDay{d1, d2, d3}, true)

		// ⛔ 前提自检：三块真的并成了一段 —— 否则键不会分岔，这一格量的是别的东西。
		if len(cov) != 1 {
			t.Fatalf("前提没成立：期望并成 1 段，实得 %v ⇒ 读数作废", cov)
		}
		if !sawUnverified(rep) {
			t.Fatalf("多块同步没有报出「未走查」——这是红法二：有人把它修好了。\n"+
				"  ⇒ 那应该是 (iv)（一遍扫描按 Coverage() 的段分桶核算）落地了。\n"+
				"  ⇒ 该做的是：删掉这条测试，并改 gap.go 里 GapStoreUnverified 那段处置的第三种情形，\n"+
				"     不是把断言改回去。\n"+
				"  coverage=%v gaps=%v", cov, kinds(rep))
		}
	})
}
