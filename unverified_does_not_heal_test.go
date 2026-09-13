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

// —— 这一条钉住的是一句【已经写进文档的话】的真值 ——
//
// 文档与两处源码注释都说 `GapStoreVerifyUnrun` / `ErrSpanUnverified` 是
// **「瞬时、自动可解 —— 走一遍就行，不必问人」**。
//
// ⛔ 而那句话只对**一种来历**成立：「这一段刚写进来，还没轮到走查」。
// 同一个类别还盖着另一种：**孤儿记录** —— `CommitSpan` 在 `AppendBars` 成功【之后】
// 失败，于是盘上有这一批而 coverage 里没有。
// ⇒ 那一种**走多少遍都不动**（2026-09-11 实测，这条测试就是那次测量的常驻形态）。
//
// 🔴 **照那句话做，得到的是一个稳定、静默、永远卡住的状态** ——
// 而「走一遍就行」读起来像它会好。
//
// ⚠️ **这条测试红了，不一定是回归** —— 写在这里免得下一个人误读：
//
//	红法一  有人改坏了构造（那一步不再留下孤儿记录）⇒ 前提自检会先说话
//	红法二  **有人把它修好了**（这一类真的能自愈了）
//	        ⇒ 那时该改的是【文档里那句话】和这条测试，**不是把它调绿**
//	红法三  **有人把【通往这一类的那条路】关掉了** ——
//	        这一类还在（性质没变），而**造它的那条路没了** ⇒ 该换构造，不是改结论
//
// ⇒ 也就是本仓那条：**一条钉住缺陷的测试，它的「红」有两个方向，而报文要说得出是哪一个。**
//
// —— ⛔ 2026-09-12：红法三**真的发生了**，而它是本条测试改成今天这个样子的全部理由 ——
//
// 上一版的构造是**用一次真调用造孤儿**：`Sync [d1..d2]` 之后 `Sync [d2..d3]`
// ⇒ `d2,d3` 不早于盘上末条 ⇒ `AppendBars` 收下 ⇒ 而 `d2` 已在 coverage 里
// ⇒ `CommitSpan` 报重叠 ⇒ 盘上留下孤儿。
//
// 丙片（挑段跳过已覆盖）之后，**那一步不再产生孤儿**：`d2` 被摘掉，只拉 `d3`，
// `CommitSpan [d3,d3]` 与 `[d1,d2]` 不重叠 ⇒ 成功。
// 🔴 ⇒ **丙片关掉了孤儿记录最平常的一条生成路径**（请求区间与已有 coverage 部分重叠）。
//
// ⇒ 处置照红法三：**换构造，不改结论**。今天的构造是**注入**（`commitFailsOnce`），
// 它测的是那个**性质**，而不再依赖「重叠」这个偶然的成因。
// 📎 而这样一来它也更准了：**孤儿记录的定义是「AppendBars 成了而 CommitSpan 没成」**，
// 与「为什么 CommitSpan 没成」无关 —— 上一版把成因焊进了构造里。
//
// ⚠️ 而丙片那个收益**另有一格钉着**（`TestPartialOverlapResyncLeavesNoOrphan`）——
// 没有它，把挑段那几行删掉，本条照样绿。
//
// —— ⛔ 射程：**它对「库在不在恶化」完全不敏感** ——
//
// 评审方 2026-09-11 去找「能让它红的突变」，没找到；**而他找到了一个它该说话却没说话的输入**。
// 把 `.dat` 的字节数和缺口指纹一起印出来（我独立复现，读数逐字相同）：
//
//	顺序检查【在】    孤儿 352 ⇒ 352 / 352 / 352   缺口指纹三遍相同
//	顺序检查【废掉】  孤儿 352 ⇒ **616 / 880 / 1144**  缺口指纹**仍然三遍相同**
//
// 🔴 ⇒ **同一份缺口指纹，一边库稳定、一边库每遍多 264 字节，而本测试两边都绿。**
//
//	它覆盖的        「这一类缺口不会自愈」          ✅
//	它的绿【不】意味着 「库没在恶化」                ⛔ 那由 segfile 自己的顺序检查与其测试守
//
// ⇒ 所以上面那个「绊线」的定位要说准：**它是给「修好了」留的绊线，而它对「正在变坏」是瞎的。**
// 📎 而这一格给本仓添了一条通则：**「找不到突变」和「这条断言没有射程」是两回事** ——
// **打不出突变时，改去找「它应当关心而没关心的那一维」。**

// guard: 「走一遍就行」在孤儿记录那一种来历上不成立 —— 走多少遍读数都不动。
func TestUnverifiedSpanDoesNotHealByRunningAgain(t *testing.T) {
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
	// ⚠️ 必须关：Windows 上 t.TempDir() 的清理会因为 .dat 还开着而失败。
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

	// ① 正常落盘并登记 [d1..d2]。
	r1 := base
	r1.From, r1.To = d1, d2
	if rep, err := syn.Sync(context.Background(), r1, now); err != nil || rep.Bars != 2 {
		t.Fatalf("前提没成立：第一次同步应当干净落盘 2 根，实得 err=%v Bars=%d", err, rep.Bars)
	}

	// ② 造孤儿：**让 CommitSpan 失败一次**，而 AppendBars 照常成功。
	//    ⇒ 盘上多出这一批，coverage 里没有它 —— 那就是孤儿记录的定义。
	//    ⛔ 上一版靠「区间重叠」来触发它，而丙片把那条路关掉了（见文件头红法三）。
	// ⛔ 施加自检按【增量】算，不按总数 —— ① 那一步也经过这一层（实测总数 2）。
	// 本仓那条：一个读数要问「它的输入域多大」。
	commitsBefore := failing.commits
	failing.armed = true
	r2 := base
	r2.From, r2.To = d3, d3
	rep, err := syn.Sync(context.Background(), r2, now)
	if err == nil {
		t.Fatalf("前提没成立：注入之后 CommitSpan 应当失败，实得 err=nil（Halt=%v）", rep.Halt)
	}
	if n := failing.commits - commitsBefore; n != 1 {
		t.Fatalf("前提没成立：这一步里 CommitSpan 应当被调 1 次（注入才算施加），实得 %d 次", n)
	}
	failing.armed = false
	if n := len(store.Coverage()); n != 1 {
		t.Fatalf("前提没成立：coverage 应当仍是 1 段（那一段没登记进去），实得 %d 段", n)
	}

	// ③ 现在照那句话做：「走一遍就行」。走三遍，读数应当**一个字都不变**。
	r3 := base
	r3.From, r3.To = d1, d3
	var first string
	for i := 1; i <= 3; i++ {
		rep, err := syn.Sync(context.Background(), r3, now)
		// ⛔ **这里断的是【后果】，不是 `err`** —— 而那是 2026-09-12 量出来的：
		//
		//	底座 8aad108（丙片之前）  err=「落盘失败·交易日倒退了」，coverage 不动
		//	丙片之后                  **err=<nil>**，coverage 把孤儿并了进去
		//	                          （声称 3 条而盘上 4 条），Complete() 仍为 false
		//
		// 🔴 而改前那个 `err` **是巧合，不是设计**：它来自「重拉 d1 ⇒ 交易日倒退」，
		// 而丙片把 d1 摘掉了，那堵墙跟着没了。走查失败一向只进 `Gaps`，从来不进 `err`。
		// ⇒ 照本仓那条（**断言后果，不断言当前的实现方式**）：
		// 这一格断「缺口不动」与「Complete() 为假」，**不断言它报不报错**。
		if rep.Complete() {
			t.Fatalf("第 %d 遍：报告说 Complete() —— 而盘上有一批不属于任何已提交段的记录。\n"+
				"  ⇒ 若这一类真的自愈了，该改的是【文档里那句「瞬时、自动可解」】和这条测试，"+
				"不是把它调绿。（err=%v）", i, err)
		}
		// 🔴 承重（评审方 2026-09-12 要求，理由是读代码读出来的）：
		// 这一格此前**唯一带内容的断言是 `got == first` —— 拿它自己跟自己比**。
		// ⇒ 一次把「走查没通过」退化成「走查没跑过」的回归，**这格照样绿**，
		// 而那两档正是 (p) 一整片去分开的两个东西。
		//
		//	GapStoreVerifyFailed  走查跑了，而它没通过  ⇒ **别再重跑**，去读包在里面的真因
		//	GapStoreVerifyUnrun   走查根本没跑成        ⇒ 再跑一遍是有意义的
		//
		// ⇒ 所以要断**类别**，不能只断「指纹稳不稳」。
		kindOK := false
		for _, g := range rep.Gaps {
			if g.Kind == tickflow.GapStoreVerifyFailed {
				kindOK = true
			}
		}
		if !kindOK {
			t.Errorf("第 %d 遍：缺口里没有一条是「走查没通过」：%v\n"+
				"  ⇒ 盘上确实有一批不属于任何已提交段的记录，走查【跑过了而没通过】；\n"+
				"     报成「没走查过」会把调用方送去再跑一遍 —— 而那一遍什么都不会变。",
				i, rep.Gaps)
		}
		// 判别符就是文档里给的那一个：**这一条缺口动没动。**
		got := gapsFingerprint(rep.Gaps)
		if i == 1 {
			first = got
			continue
		}
		if got != first {
			t.Errorf("第 %d 遍的缺口与第 1 遍不同 ——\n"+
				"  第 1 遍 %s\n  第 %d 遍 %s\n"+
				"  ⇒ 若它开始变化了，那句「走多少遍都不动」就不再准确。",
				i, first, i, got)
		}
	}
	t.Logf("走了三遍，缺口逐次相同：%s", first)
}

// gapsFingerprint 把一份缺口清单折成一个可比的串。
//
// ⚠️ 它**只用来比「动没动」**，不用来判断内容对不对 ——
// 后者由别的测试守，而把两件事塞进一个判别符会让红出来的时候分不清是哪一件。
func gapsFingerprint(gaps []tickflow.Gap) string {
	s := ""
	for _, g := range gaps {
		s += g.String() + " | "
	}
	return s
}

// commitFailsOnce 包一层 `tickflow.Store`，在 `armed` 时让 `CommitSpan` 失败一次。
//
// ⛔ 它存在的理由是**把成因从构造里摘出去**：孤儿记录的定义是
// 「`AppendBars` 成了而 `CommitSpan` 没成」，**与 CommitSpan 为什么没成无关**。
// 上一版靠「区间重叠」触发，于是丙片一改挑段，那条构造就塌了（见文件头红法三）。
//
// —— ⛔ 它模拟的是**哪一种**真实失败（评审方 2026-09-12 要求写死，理由我认）——
//
// 不写这一句，下一个人读到的只是「我们造了一个让 CommitSpan 失败的 Store」，
// 而在「因重叠而失败」那条路已被丙片关掉之后，**那看起来像在给一个不会发生的事写测试**。
//
//	这一格注入的是「**AppendBars 成功之后、CommitSpan 没成**」那一种。
//	它今天的真实成因有两个：
//	  · **两次写之间崩溃** —— AppendBars 与 CommitSpan 是两次写，本仓没有原子性
//	  · **写 `.meta` 本身失败**（盘满 / IO 错）
//	而「因区间重叠而失败」那条路**已被丙片关掉**（见 TestPartialOverlapResyncLeavesNoOrphan）。
//
// ⚠️ `commits` 是**施加自检**：注入若没被走到，下面那一格的「前提成立」就是假的。
type commitFailsOnce struct {
	tickflow.Store
	armed   bool
	commits int
}

func (c *commitFailsOnce) CommitSpan(cal tickflow.Calendar, k tickflow.ProductKey,
	span tickflow.Span, out tickflow.Outcome) error {
	c.commits++
	if c.armed {
		c.armed = false
		return errors.New("注入：CommitSpan 失败一次（造孤儿记录用）")
	}
	return c.Store.CommitSpan(cal, k, span, out)
}

// —— 这一条钉的是【丙片修掉的那个缺陷】——
//
// 🔴 没有它，把挑段那几行删掉，全仓一格都不会红 ——
// 而本仓那条：**一个不会红的删除，是最难被拦住的那一种删除。**
//
// 改前（底座 8aad108，我量的）：`Sync [d1..d2]` 之后 `Sync [d2..d3]`
// ⇒ `d2` 已在 coverage 里 ⇒ `CommitSpan` 报重叠 ⇒ **盘上留下孤儿记录**，
// 而报告里那次的 `Bars` 是 0（说没写，而写了）。
//
// —— 它的两条红法（写死，免得下一个人把它调绿）——
//
//	红法一  构造坏了 ⇒ **前提自检会先说话**（第一次没落盘 2 根 / 走查回了 0 段）
//	红法二  **有人把挑段改回去了** ⇒ 那是把一个【已经修好的缺陷】放回来
//	        ⇒ 去看丙片那几行，**别调绿**
//
// guard: 一次【部分重叠】的重跑不得留下孤儿记录 —— 丙片挑段的承重后果。
func TestPartialOverlapResyncLeavesNoOrphan(t *testing.T) {
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

	// ① 先拉 [d1..d2]。
	r1 := base
	r1.From, r1.To = d1, d2
	rep1, err := syn.Sync(context.Background(), r1, now)
	if err != nil || rep1.Bars != 2 {
		t.Fatalf("前提没成立：第一次应当干净落盘 2 根，实得 err=%v Bars=%d", err, rep1.Bars)
	}

	// ② 再拉 [d2..d3]：**与已有 coverage 部分重叠** —— 改前正是这一步造出孤儿。
	r2 := base
	r2.From, r2.To = d2, d3
	rep2, err2 := syn.Sync(context.Background(), r2, now)
	t.Logf("部分重叠那次：err=%v · Bars=%d · Halt=%v · 跳过=%v",
		err2, rep2.Bars, rep2.Halt, rep2.SkippedCovered)

	// 🔴 承重一：它不该失败了。
	if err2 != nil {
		t.Fatalf("部分重叠的重跑失败了：%v\n"+
			"  ⇒ 改前它在 CommitSpan 上报重叠，而记录已经在盘上（孤儿）。\n"+
			"  ⇒ 丙片把已覆盖的那一天摘掉，这一步本该成功。", err2)
	}
	// 🔴 承重二：**跳过要留下事实** —— 否则它与「源什么都没给」同形（都是 Bars=0）。
	if len(rep2.SkippedCovered) == 0 {
		t.Errorf("跳过了已覆盖的日子，而报告里一条痕迹都没有 ⇒ " +
			"「跳过了」与「源什么都没给」共用一句话")
	}
	// 🔴 承重三：**盘上不许有孤儿** —— 走查每一段都得过。
	res, verr := store.VerifyCoverage()
	if verr != nil {
		t.Fatalf("走查跑不起来：%v", verr)
	}
	for key, e := range res {
		if e != nil {
			t.Errorf("这一段走查不过：%s ⇒ %v\n"+
				"  ⇒ 盘上留下了不属于任何已提交段的记录（孤儿）。", key, e)
		}
	}
	// ⛔ 前提自检：真的走查了东西 —— 空 map 会让上面那个循环一次都不失败。
	if len(res) == 0 {
		t.Fatalf("走查回了 0 段 ⇒ 上面那个循环什么也证不了，读数作废")
	}
	// 🔴 承重四：coverage 必须已经把 d3 并进来 —— 否则「没失败」可能只是「什么都没做」。
	cov := store.Coverage()
	last := cov[len(cov)-1]
	if last.To != d3 {
		t.Errorf("coverage 末端是 %s，而该到 %s ⇒ 这一次其实什么都没拉进来：%v",
			last.To, d3, cov)
	}
}
