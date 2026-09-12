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
//
// ⇒ 也就是本仓那条：**一条钉住缺陷的测试，它的「红」有两个方向，而报文要说得出是哪一个。**
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

	// ① 正常落盘并登记 [d1..d2]。
	r1 := base
	r1.From, r1.To = d1, d2
	if rep, err := syn.Sync(context.Background(), r1, now); err != nil || rep.Bars != 2 {
		t.Fatalf("前提没成立：第一次同步应当干净落盘 2 根，实得 err=%v Bars=%d", err, rep.Bars)
	}

	// ② [d2..d3]：这一批 d2,d3 不早于盘上末条 d2 ⇒ AppendBars 收下；
	//    而 d2 已在 coverage 里 ⇒ CommitSpan 报重叠 ⇒ 留下孤儿记录。
	r2 := base
	r2.From, r2.To = d2, d3
	rep, err := syn.Sync(context.Background(), r2, now)
	if err == nil {
		t.Fatalf("前提没成立：第二次同步应当因 CommitSpan 重叠而失败，实得 err=nil（Halt=%v）", rep.Halt)
	}
	if n := len(store.Coverage()); n != 1 {
		t.Fatalf("前提没成立：coverage 应当仍是 1 段（那一段没登记进去），实得 %d 段", n)
	}

	// ③ 现在照那句话做：「走一遍就行」。走三遍，读数应当**一个字都不变**。
	r3 := base
	r3.From, r3.To = d1, d3
	var first string
	for i := 1; i <= 3; i++ {
		rep, err := syn.Sync(context.Background(), r3, now)
		if err == nil {
			t.Fatalf("第 %d 遍：它没报错 —— 若这一类真的自愈了，\n"+
				"  ⇒ 该改的是【文档里那句「瞬时、自动可解」】和这条测试，不是把它调绿。", i)
		}
		if rep.Complete() {
			t.Errorf("第 %d 遍：报告说 Complete()，而它同时返回了错误", i)
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
