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

// —— 这条缝：`Syncer` ←→ 真的 `segfile.Store` ——
//
// ⛔ **它的由来是一个【封闭】的读数，不是一句「我搜了搜没找到」**
// （送审方 2026-09-10 量、评审方独立重取，三条逐条对上）：
//
//	grep -rn  "Syncer{"   --include=*.go .   ⇒ 全仓 **1** 处（sync.go，NewSyncer 里）
//	                                          ⇒ 它是**唯一构造器**
//	grep -rn  "NewSyncer" --include=*.go . | grep -v _test
//	                                          ⇒ 非测试里只有那一处声明
//	                                          ⇒ **没有第二条走 helper 的路**
//	用 NewSyncer 的 _test.go = 2 个，提到 segfile 的 _test.go = 12 个，**交集 0**
//
// 🔴 ⇒ **每一条 Syncer 测试用的都是 `fakeStore`。**
// ⚠️ 而 `sync_dispose_test.go` 里那句 `"segfile: 假装走查发现对不上"`
// **是 `fakeStore` 错误里的字符串字面量** —— 它让「提到 segfile 的文件」这张单子
// 看起来像有集成测试。⇒ 本仓那条的反向形态：**文件里出现那个词，不等于接了那个东西。**
//
// —— 它答的是【别的测试都答不了】的那一问 ——
//
//	segfile 那侧      对着 segfile 自己测      ✅ 已有
//	Syncer  那侧      对着 fakeStore 测        ✅ 已有
//	**合起来**        **今天没有**            ← 本文件
//
// 🔴 而缺的这一格不是「少一层覆盖」：**`fakeStore` 是我们自己写的，
// 它按【我们以为的】契约行事。** 两侧各自全绿，
// 而「真实现满不满足 Syncer 依赖的那条契约」**一个读数都没有**。
// ⇒ 本仓那条「五条分支各自全绿、合起来红；跨分支约束只有合并看得见」——
// 而这里连「合起来」这个动作都还没发生过。
//
// —— ⚠️ 它为什么住在 `package tickflow_test` ——
//
//	go list  根包 Imports / TestImports / XTestImports 里 segfile ⇒ 0 / 0 / 0
//	         segfile 的 Imports 里根包                          ⇒ 1
//	⇒ 方向是 **segfile → 根包** ⇒ 根包【内部】的测试文件 import segfile 就是循环
//	⇒ 外部测试包是唯一的家（本仓已有 6 个这种文件，真日历那条集成测试同理）
//
// —— ⛔ 而它【必须先落地并在今天的 main 上就绿】，理由是时序不是洁癖 ——
//
//	排在换读法【之前】：它在今天的库上绿 ⇒ **它证明「这条缝今天是通的」**；
//	                    换读法那一颗必须让它保持绿 ⇒ 那才是**缝这一层的等价性**
//	排在换读法【之后】：它只能断言新读法的行为 ⇒ **证不了这条缝曾经是通的** ⇒ 少一个基准
//
// （评审方 2026-09-10 判的排期，我认；他同时登记了「缝的集成测试没落地并绿之前，
// 换读法那一颗不放行」。）

// —— 桩件：只有源是假的，日历与库都是真的 ——

// seamSource 按一张【交易日 → 给不给根】的表答。
//
// ⚠️ 它假在「数据从哪来」，**不假在「库怎么答」** —— 而本文件问的正是后者。
type seamSource struct {
	give map[tickflow.TradingDay]bool
}

func (s seamSource) Caps(tickflow.ProductKey) tickflow.Capabilities {
	return tickflow.Capabilities{
		Periods: []tickflow.Period{tickflow.Daily},
		// ⚠️ `Since` 必填：`Validate()` 拒绝「在 Periods 里而不在 Since 里」——
		// 理由是本仓那条「『忘了填』和『真的从那天起』必须分得开」。
		// ⇒ 第一版我漏了它，`Sync` 当场大声拒绝。**那是设计在干活，不是我被挡住。**
		Since:     map[tickflow.Period]tickflow.TradingDay{tickflow.Daily: 20000101},
		MaxBars:   1000,
		BatchDays: 30,
		ClientUse: tickflow.ClientUseHTTP,
	}
}

func (s seamSource) Bars(_ context.Context, req tickflow.BarRequest) ([]tickflow.Bar, error) {
	var out []tickflow.Bar
	for d := req.From; d <= req.To; d++ {
		if !s.give[d] {
			continue
		}
		ts := dayStartMs(d)
		out = append(out, tickflow.Bar{
			Ts: ts, TsEnd: ts + int64(24*time.Hour/time.Millisecond) - 1,
			TradingDay: d,
			Open:       1, High: 1, Low: 1, Close: 1,
			Volume: 1,
		})
	}
	return out, nil
}

// dayStartMs 把一个 YYYYMMDD 变成当天 00:00 UTC 的毫秒。
func dayStartMs(d tickflow.TradingDay) int64 {
	y, m, day := d.Split()
	return time.Date(y, time.Month(m), day, 0, 0, 0, 0, time.UTC).UnixMilli()
}

// guard: Syncer ←→ 真 segfile.Store 那条缝 —— 真库给的三种答案穿不穿得过它。
// TestSyncerOverRealSegfileStore_ThreeValuedAnswerSurvivesTheSeam
// 是这条缝的第一条测试。它问的**不是**「Sync 能不能跑通」，
// 而是：**真库给出的那三种答案，有没有原样穿过这条缝落进报告里。**
//
// 构造：三个交易日，源**只给中间那一天**的根。
//
//	20200805  源不给  ⇒ 落在本次提交的 coverage 里、走查过、那天没根
//	                    ⇒ 真库答「确认没有」⇒ 报告里必须是 GapConfirmedEmpty
//	20200806  源给了  ⇒ 真库答「有」        ⇒ **它根本不该出现在缺口里**
//	20200807  源不给  ⇒ 同 0805
//
// ⛔ 承重的是**两个方向**，缺一个都能被一个坏实现骗过：
//
//	只断言「0805/0807 是 GapConfirmedEmpty」 ⇒ 一个恒答「没有」的库照样过
//	只断言「0806 不在缺口里」                 ⇒ 一个恒答「有」的库照样过
//
// ⚠️ 而 `GapConfirmedEmpty` 这一类正是本仓标为**最危险的错认**的那一格的对面：
// 「没拉过」被当成「拉过确认没有」⇒ 静默漏数据且不自愈。
// ⇒ 这条测试证的是：**真库分得清这两者，而且这个区分穿得过 Syncer。**
//
// —— ✅ 它的价值是量出来的，不是声称的（2026-09-10，突变打在【生产代码】上）——
//
//	MS1  segfile.HasBars 恒答「没有」  ⇒ 根包里**只有这一条**红（segfile 包另红 2 条）
//	MS2  segfile.HasBars 恒答「有」    ⇒ 根包里**只有这一条**红（segfile 包另红 1 条）
//
// 🔴 **根包里没有任何别的测试接得住这两个突变** —— 因为它们用的全是 `fakeStore`。
// ⇒ 「这条缝没有测试」不再是一句判断，它是一个可以被复现的读数。
//
// —— ⛔ 而它【接不住】的那一格，也是量出来的，写在这儿而不是藏着 ——
//
//	MS3  摘掉 sync.go 里那句 `st.Err = ErrSpanUnverified`
//	     ⇒ 红的是**既有的** TestGapsAreComputedNotLeftEmpty，**不是本条**
//
// ⇒ 本条的射程是「**真库给出的答案**穿不穿得过这条缝」，
// **不是**「Syncer 自己那半（谁被走查过）对不对」—— 后者已经有人守着。
// ⚠️ 写清楚是因为：不写的话，下一个人会把这条测试的绿读成「整条缝都验过了」。
func TestSyncerOverRealSegfileStore_ThreeValuedAnswerSurvivesTheSeam(t *testing.T) {
	const (
		d1 = tickflow.TradingDay(20200805)
		d2 = tickflow.TradingDay(20200806)
		d3 = tickflow.TradingDay(20200807)
	)

	cal, err := embedded.New([]tickflow.TradingDay{d1, d2, d3})
	if err != nil {
		t.Fatalf("造日历失败：%v", err)
	}

	// **真库**，不是替身。
	store, truncated, err := segfile.Open(t.TempDir(), tickflow.Daily)
	if err != nil {
		t.Fatalf("开库失败：%v", err)
	}
	if truncated != 0 {
		t.Fatalf("新库不该有残尾，却报了 %d —— 前提不成立，读数作废", truncated)
	}
	// ⚠️ 必须关：Windows 上 `t.TempDir()` 的清理会因为 `.dat` 还开着而失败
	// （`unlinkat …: The process cannot access the file`）。
	// ⇒ 第一版漏了它，测试本身红不了，**而清理阶段红** —— 一个只在这个 OS 上出现的失败。
	t.Cleanup(func() {
		if cerr := store.Close(); cerr != nil {
			t.Errorf("关库失败：%v", cerr)
		}
	})

	src := seamSource{give: map[tickflow.TradingDay]bool{d2: true}}
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
		Period: tickflow.Daily,
		From:   d1,
		To:     d3,
	}
	// now 取到区间之后很远，让三天都算「已收盘」。
	rep, err := syn.Sync(context.Background(), req, dayStartMs(20200901))
	if err != nil {
		t.Fatalf("Sync 出错：%v", err)
	}

	// 前提自检一：源真的给出了那一天的根 —— 否则下面两句都在问一个空库。
	if rep.Bars != 1 {
		t.Fatalf("落盘 %d 条，期望 1 条 —— 这条测试的构造没成立，读数作废\n"+
			"  ⇒ 报告：Synced=%v CoversOK=%v Gaps=%v",
			rep.Bars, rep.Synced, rep.CoversOK, rep.Gaps)
	}

	kindOf := map[tickflow.TradingDay]tickflow.GapKind{}
	for _, g := range rep.Gaps {
		for d := g.From; d <= g.To; d++ {
			kindOf[d] = g.Kind
		}
	}

	// 前提自检二：报告里真的有缺口 —— 一个空的 Gaps 会让下面第一句空转。
	if len(rep.Gaps) == 0 {
		t.Fatal("一段缺口都没报 —— 而源只给了三天里的一天，这条测试是空转的，读数作废")
	}

	// ⛔ 方向一：源没给的那两天，真库必须答「确认没有」，而不是「没拉过」。
	for _, d := range []tickflow.TradingDay{d1, d3} {
		got, ok := kindOf[d]
		if !ok {
			t.Errorf("%s 不在任何缺口里 —— 而源没给过它的根。\n"+
				"  ⇒ 真库把「这一天没有根」答成了「有」，或者这一天根本没进 coverage。", d)
			continue
		}
		if got != tickflow.GapConfirmedEmpty {
			t.Errorf("%s 报成了 %v，期望 GapConfirmedEmpty。\n"+
				"  ⇒ 若是 GapNeverFetched：coverage 没提交，或提交了而这一天不在段里；\n"+
				"  ⇒ 若是 GapStoreUnverified：走查没发生 —— 而那正是本仓标为"+
				"【最危险的错认】的那一格的邻居。", d, got)
		}
	}

	// ⛔ 方向二：源给了的那一天，**不该出现在缺口里**。
	// 少了这一句，一个恒答「确认没有」的库也能让上面那两格全绿。
	if got, ok := kindOf[d2]; ok {
		t.Errorf("%s 出现在缺口里（%v）—— 而源给过它的根、也落盘了（rep.Bars=%d）。\n"+
			"  ⇒ 真库把「有」答成了「没有」；这个方向的错会让同一天被反复重拉。",
			d2, got, rep.Bars)
	}
}
