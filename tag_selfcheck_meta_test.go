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
//
// ⚠️ **而断言三在当前（单段）构造下，红必伴随断言二红；它单独钉不住东西。**
// 这个构造只产出一条缺口 ⇒ 它若不是 `GapNeverFetched`，断言二必红；
// 而断言三只在它恰好是 `GapStoreUnverified` 时才多红一格 ⇒ **{三红} ⊆ {二红}**。
// ⇒ 它今天的价值是**把注解要人找的那个名字写进报文**；
// 等构造是多段时（那一格在「核不了的」单子上）它才独立。
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

// —— (l) 的姊妹格：同一节【自查】的方法一 ——
//
// `v0.4.1` 的 tag 注解里，自查方法一逐字是：
//
//	一、对 `store.Coverage()` 的每一段跑 `store.Verify(span)`：
//	    坏掉的段返回「记录的交易日不是非降序」。**这一条在删过文件之后仍然可读。**
//
// 🔴 **tag 注解改不了，而这一句逐字点名了一个 API 和一句报文。**
// ⇒ 钉的不是「存在一个叫 `Verify` 的方法」（那只钉住名字），
// 是**照着那句话做一遍，看它今天还走得通**：签名对得上 · 健康库全过 · 坏库给出那句话。
//
// ⚠️ 红了有两个方向：
//
//	红法一  构造被改坏（坏记录没留住 / coverage 不是一段）⇒ 前提自检先说话
//	红法二  **`Verify(span)` 该退休了**（签名变了 / 报文换了 / 方法没了）
//	        ⇒ 处置是**改这条测试 ＋ 在 release notes 写一条勘误**（tag 改不了，只能靠勘误），
//	          **不是把断言删掉**
//
// 📎 一份不可修的注解，值得**每一句**都有钉子 —— (l) 钉那一节的第二句，本条钉第一句。

// guard: v0.4.1 注解自查方法一（对每一段跑 Verify，坏段报「不是非降序」）今天照着做得通。
func TestTagV041SelfCheckOneStillWorks(t *testing.T) {
	const want = "坏掉的段返回「记录的交易日不是非降序」"
	src, err := os.ReadFile(filepath.Join("docs", "release", "v0.4.1.md"))
	if err != nil {
		t.Fatalf("读发布注解失败：%v", err)
	}
	// ⛔ 前提自检：这条测试钉的是【那一句】，所以先证明那一句还在它该在的地方。
	// 少了它，注解改了而测试照旧绿 —— 那时它钉的是一句已经不存在的话。
	if !strings.Contains(string(src), want) {
		t.Fatalf("发布注解里找不到那句自查原话：%q\n"+
			"  ⇒ 若是注解被改了：tag 里那份改不了，这条测试钉的是 tag 里那一句。", want)
	}

	const (
		d1 = tickflow.TradingDay(20200805)
		d2 = tickflow.TradingDay(20200806)
	)
	cal, err := embedded.New([]tickflow.TradingDay{d1, d2})
	if err != nil {
		t.Fatalf("造日历失败：%v", err)
	}
	key := tickflow.ProductKey{Exchange: "SHFE", Product: "rb"}
	dir := t.TempDir()
	s, _, err := segfile.Open(dir, tickflow.Daily)
	if err != nil {
		t.Fatalf("开库失败：%v", err)
	}
	mk := func(d tickflow.TradingDay, ts int64) tickflow.Bar {
		return tickflow.Bar{Ts: ts, TsEnd: ts + 1, TradingDay: d,
			Open: 1, High: 1, Low: 1, Close: 1, Volume: 1}
	}
	if err := s.AppendBars([]tickflow.Bar{mk(d1, 1), mk(d2, 2)}); err != nil {
		t.Fatalf("落盘失败：%v", err)
	}
	if err := s.CommitSpan(cal, key,
		tickflow.Span{From: d1, To: d2, Bars: 2, Days: 2}, tickflow.OutcomeComplete); err != nil {
		t.Fatalf("登记失败：%v", err)
	}

	t.Run("健康库 照着做一遍_每一段都过", func(t *testing.T) {
		cov := s.Coverage()
		if len(cov) != 1 {
			t.Fatalf("前提没成立：期望 1 段，实得 %v ⇒ 读数作废", cov)
		}
		for _, sp := range cov {
			if err := s.Verify(sp); err != nil {
				t.Fatalf("健康库上 Verify(%v) 就报错了：%v\n"+
					"  ⇒ 自查方法一在一个没问题的库上给出假警报，那句注解就不成立了。", sp, err)
			}
		}
	})

	if err := s.Close(); err != nil {
		t.Fatalf("关库失败：%v", err)
	}
	// 造一条【倒序】记录：写入口拒收，只能绕过它直接写 .dat。
	var dat string
	if werr := filepath.Walk(dir, func(p string, fi os.FileInfo, e error) error {
		if e == nil && !fi.IsDir() && strings.HasSuffix(p, ".dat") {
			dat = p
		}
		return e
	}); werr != nil {
		t.Fatalf("走一遍库目录失败：%v", werr)
	}
	rec := segfile.EncodeBar(mk(d1, 3)) // 交易日回到 d1，而盘上最后一条是 d2
	f, err := os.OpenFile(dat, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("打开 .dat 失败：%v", err)
	}
	if _, err := f.Write(rec[:]); err != nil {
		f.Close()
		t.Fatalf("写倒序记录失败：%v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("关 .dat 失败：%v", err)
	}

	t.Run("坏库 报的必须是注解点名的那句话", func(t *testing.T) {
		s2, truncated, err := segfile.Open(dir, tickflow.Daily)
		if err != nil {
			t.Fatalf("重开失败：%v", err)
		}
		t.Cleanup(func() {
			if cerr := s2.Close(); cerr != nil {
				t.Errorf("关库失败：%v", cerr)
			}
		})
		// ⛔ 前提自检：那条倒序记录留住了（OpenDat 会砍半截记录）。
		if truncated != 0 {
			t.Fatalf("前提没成立：重开砍掉了 %d 字节 ⇒ 坏记录没留住，读数作废", truncated)
		}
		cov := s2.Coverage()
		if len(cov) != 1 {
			t.Fatalf("前提没成立：期望 1 段，实得 %v ⇒ 读数作废", cov)
		}
		err = s2.Verify(cov[0])
		if err == nil {
			t.Fatalf("坏库上 Verify 竟然通过了 ⇒ 自查方法一查不出它该查的东西")
		}
		if !strings.Contains(err.Error(), "记录的交易日不是非降序") {
			t.Errorf("坏段报的不是注解点名的那句话。\n"+
				"  注解逐字：%s\n  实得：%v\n"+
				"  ⇒ 这是红法二：报文换了。tag 改不了 ——\n"+
				"     先在 release notes 写一条勘误，再改这条测试，别把断言删掉。", want, err)
		}
	})
}

// —— (l) 与「方法一」那格的**第三个姊妹**：同一节自查，三句话，三句都有钉子 ——
//
// ⛔ v0.5 的 (iv) 把走查绑到【库】上之后，自查方法二**失效**了，而失效的方式是
// **为真而有害**：照着做的人找不到 `GapStoreUnverified`，于是得出「这个库没问题」——
// **而库是坏的**。
//
//	v0.4.1  坏库 ＋ 再同步 ⇒ 落盘先失败 ⇒ 一段都没走查 ⇒ 每段报 (5) ⇒ 找得到 ✅
//	v0.5    走查按 coverage 的每一段走一遍 ⇒ 坏库每段都【走查过而没通过】
//	        ⇒ 报的是 (7) GapStoreVerifyFailed ⇒ 找不到 (5) ⛔
//
// 🔴 **tag 注解改不了 ⇒ 唯一的通道是勘误**，而这一格钉的就是「勘误在，且说对了」。
//
// ⚠️ 红了有两个方向：
//
//	红法一  勘误被删了 / 没点名改看哪一类 ⇒ 补回去
//	红法二  **方法二又管用了**（坏库上又找得到 (5)）⇒ 那多半是 (iv) 被回退了；
//	        处置是**再写一条勘误**说明它何时恢复，**不是把这里的断言删掉**
//
// 📎 而这份勘误是怎么被逼出来的：计划里有一步「把 whole_library 那条翻面」——
// **翻面的那一刻，就是方法二失效的那一刻。**
// ⇒ **一颗钉在现状上的钉子，它响的那一刻要连着问「有没有哪份不可修的东西引用了这个现状」。**

// guard: v0.4.1 自查方法二已失效，而勘误必须在、且点名改看 GapStoreVerifyFailed。
func TestTagV041SelfCheckTwoHasAnErratum(t *testing.T) {
	const sentence = "看 `rep.Gaps` 里有没有 `GapStoreUnverified`"
	src, err := os.ReadFile(filepath.Join("docs", "release", "v0.4.1.md"))
	if err != nil {
		t.Fatalf("读发布注解失败：%v", err)
	}
	// ⛔ 前提自检：钉的是【那一句】，先证明它还在。
	if !strings.Contains(string(src), sentence) {
		t.Fatalf("发布注解里找不到方法二那句原话：%q", sentence)
	}

	errata, err := os.ReadFile(filepath.Join("docs", "errata.md"))
	if err != nil {
		t.Fatalf("读勘误失败：%v\n"+
			"  ⇒ tag 注解改不了，勘误是唯一的通道 —— 这份文件不许消失。", err)
	}
	for _, want := range []string{"v0.4.1", "自查", "方法二", "GapStoreVerifyFailed"} {
		if !strings.Contains(string(errata), want) {
			t.Errorf("勘误里没有 %q。\n"+
				"  ⇒ 一条勘误要说清【哪一版的哪一句】失效了、以及【改看什么】——"+
				"少了后者，读的人只知道旧路不通，不知道新路在哪。", want)
		}
	}

	// —— 而勘误说的那件事，要有一个当场的读数撑着 ——
	const (
		a1 = tickflow.TradingDay(20200805)
		a2 = tickflow.TradingDay(20200806)
	)
	cal, err := embedded.New([]tickflow.TradingDay{a1, a2})
	if err != nil {
		t.Fatalf("造日历失败：%v", err)
	}
	dir := t.TempDir()
	seed, _, err := segfile.Open(dir, tickflow.Daily)
	if err != nil {
		t.Fatalf("开库失败：%v", err)
	}
	if cerr := seed.Close(); cerr != nil {
		t.Fatalf("关库失败：%v", cerr)
	}
	writeZeroRecord(t, dir) // 盘上先有一条全库坏记录
	store, truncated, err := segfile.Open(dir, tickflow.Daily)
	if err != nil {
		t.Fatalf("重开失败：%v", err)
	}
	if truncated != 0 {
		t.Fatalf("前提没成立：重开砍掉了 %d 字节 ⇒ 坏记录没留住，读数作废", truncated)
	}
	t.Cleanup(func() {
		if cerr := store.Close(); cerr != nil {
			t.Errorf("关库失败：%v", cerr)
		}
	})
	syn, err := tickflow.NewSyncer(tickflow.SyncerConfig{
		Calendar: cal, Store: store,
		NewSource: func(*http.Client) tickflow.Source {
			return seamSource{give: map[tickflow.TradingDay]bool{a1: true, a2: true}}
		},
		Pacer: pacing.NoPacing(), Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("造 Syncer 失败：%v", err)
	}
	rep, serr := syn.Sync(context.Background(), tickflow.SyncRequest{
		Symbol: tickflow.Symbol{Exchange: "SHFE", Product: "rb", YearMon: 2101},
		Period: tickflow.Daily, From: a1, To: a2,
	}, dayStartMs(20200901))
	if serr != nil {
		t.Fatalf("同步出错：%v", serr)
	}
	// ⛔ 前提自检：真的报出了缺口 —— 空的 Gaps 会让下面两句一起空转。
	if len(rep.Gaps) == 0 {
		t.Fatalf("一段缺口都没报 ⇒ 下面两句是空转的，读数作废（Halt=%v Bars=%d）", rep.Halt, rep.Bars)
	}
	sawOld, sawNew := false, false
	var kinds []string
	for _, g := range rep.Gaps {
		kinds = append(kinds, g.From.String()+".."+g.To.String()+"="+g.Kind.String())
		switch g.Kind {
		case tickflow.GapStoreUnverified:
			sawOld = true
		case tickflow.GapStoreVerifyFailed:
			sawNew = true
		}
	}
	if !sawNew {
		t.Errorf("坏库上没有报出 GapStoreVerifyFailed，实得：%s\n"+
			"  ⇒ 勘误里写的「改看这一类」就落空了。", strings.Join(kinds, " "))
	}
	if sawOld {
		t.Errorf("坏库上仍然报得出 GapStoreUnverified，实得：%s\n"+
			"  ⇒ 这是红法二：方法二又管用了（多半是 (iv) 被回退）。\n"+
			"  ⇒ 处置是【再写一条勘误】说明它何时恢复，不是把这里的断言删掉。",
			strings.Join(kinds, " "))
	}
}
