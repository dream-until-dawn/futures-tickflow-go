package tickflow

import (
	"encoding/json"
	"testing"
)

// TestReportCompleteIsDerivedFromEveryContributingField
// `Complete()` 是**派生**的 —— 每一个参与派生的字段都要能【单独】把它翻成 false。
//
// ⛔ 只测「全空 ⇒ true」是不够的：那和「Complete() 恒返回 true」**绿得一模一样**。
func TestReportCompleteIsDerivedFromEveryContributingField(t *testing.T) {
	// ⚠️ **这一行的前提【翻过一次】，而翻的是【前提】不是断言**（丙三之二）：
	// 上一版写的是 `SyncReport{}.Complete()` 应当为真。
	// 而 `Halt` 落进报告之后，零值 = HaltUnknown = 「没有记录为什么停」⇒ 留声。
	// ⇒ 现在的「一份干净的报告」必须显式说出它跑完了。
	base := SyncReport{Halt: HaltDone}
	if !base.Complete() {
		t.Fatal("跑完且无痕迹的报告应当是 Complete —— 前提没成立，后面几条什么也没验")
	}
	for _, c := range []struct {
		name string
		r    SyncReport
	}{
		// ⚠️ 每一格都【显式】写上 Halt，不靠一个「名字以中止开头就不改」的判别。
		// 🔴 上一版就是那么写的（`c.name[:2] != "中止"`），而 Go 的字符串下标是
		// **按字节**的：「中止」在 UTF-8 里是 6 字节 ⇒ 那个条件恒真
		// ⇒ 它把三条中止用例的 Halt 全改成了 HaltDone，**正好废掉它们要测的那件事**。
		// ⇒ 判据：**测试内部的小聪明出错时，结果是【绿】不是红** ——
		// 所以用例表里宁可重复，也别在里面放条件。
		{"截断留声", SyncReport{Halt: HaltDone, TruncatedTails: []string{"1m.dat: 33 字节"}}},
		{"旧 meta 作废", SyncReport{Halt: HaltDone, LegacyMetaDiscarded: []string{"rb/1m"}}},
		{"旧 meta 标记", SyncReport{Halt: HaltDone, LegacyMetaUnverified: []string{"rb/1m"}}},
		{"对不上网格的根", SyncReport{Halt: HaltDone, Misaligned: 1}},
		{"可疑交易日", SyncReport{Halt: HaltDone, AnomalousDays: []TradingDay{20200807}}},
		{"中止：预算耗尽", SyncReport{Halt: HaltBudget}},
		{"中止：被取消", SyncReport{Halt: HaltContext}},
		{"中止：没记录理由（零值）", SyncReport{Halt: HaltUnknown}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if c.r.Complete() {
				t.Errorf("%s 单独出现时 Complete() 仍为 true —— 这一格没有参与派生", c.name)
			}
		})
	}
}

// TestReportCompleteIsInvariantUnderGaps 缺口**不参与** `Complete()`，而这是写死的决定。
//
// ⛔ 缺口不是异常，是**结果**：一次正常的同步本来就会报出「不是交易日」「拉过确认没有」。
// 而把六类折成一个 bool，正是⑱ 那一格 ——
// **第五类「走一遍就行」与第六类「必须问人」处置完全不同，合成一位就把刚分开的两者又粘回去。**
//
// 🔴 **上一版这一条【被绕过去了，两次】**（评审方 2026-09-09 造的，我精确复现）：
//
//	绕法一  「有【多日】缺口就翻脸」 ⇒ 上一版 fixture 六段**全是单日** ⇒ **绿**
//	绕法二  「缺口多于 6 段就翻脸」   ⇒ 上一版 fixture **恰好 6 段**   ⇒ **绿**
//	对照    「len(Gaps) == 0」（显然那版）⇒ 红 —— 它只挡得住显然那版
//
// ⇒ **它钉住的是「在那一个 fixture 的形状上不看 Gaps」，不是「从不看 Gaps」。**
// 而两个绕法各自钻的是那个 fixture 的一个**维度**（跨度 / 段数）——
// **一个具体 fixture 天生在每一维上都取了一个值，而断言只在那些值上成立。**
//
// ⇒ 改成【不变性】：其它字段固定，让 Gaps 在**段数 · 跨度 · 类别**三维上变，
// 断言 `Complete()` **一动不动**。
// ⚠️ **而它仍然是一个样本** —— 不变性只在下面这几个形状上被验过。
func TestReportCompleteIsInvariantUnderGaps(t *testing.T) {
	all := []GapKind{GapNeverFetched, GapConfirmedEmpty, GapNotTrading,
		GapCalendarUnknown, GapStoreUnverified, GapStoreLegacy}
	sixKinds := make([]Gap, 0, len(all))
	for i, k := range all {
		d := TradingDay(20200801 + i)
		sixKinds = append(sixKinds, Gap{From: d, To: d, Kind: k})
	}
	var seven []Gap
	for i := 0; i < 7; i++ {
		d := TradingDay(20200801 + i)
		seven = append(seven, Gap{From: d, To: d, Kind: GapNeverFetched})
	}

	shapes := []struct {
		name string
		gs   []Gap
	}{
		{"没有缺口", nil},
		{"单日一段", []Gap{{20200801, 20200801, GapNeverFetched}}},
		{"多日一段（绕法一钻的那一维）", []Gap{{20200801, 20200831, GapNeverFetched}}},
		{"七段（绕法二钻的那一维）", seven},
		{"六类各一（类别那一维）", sixKinds},
	}

	// 两组基底：一组本该 Complete、一组本该不 Complete ——
	// ⛔ 只用前者的话，「Complete() 恒真」也会通过这一条
	//（评审方 2026-09-09 实测：只留 {} ＋ Complete 恒真 ⇒ **0 红**）。
	//
	// 🔴 **而这两行【被改过一次，因为 Halt 落地时它们塌成了一组】**：
	// 上一版是 `{}` 与 `{Misaligned: 1}`，而 `Halt` 进来之后 `{}` 也不 Complete 了
	// ⇒ 两组基底同值 ⇒ **那个「前提」当场失效，而这条测试仍然是绿的**。
	// ⇒ 判据：**一条断言的前提写在别的字段上时，改那个字段要回头重量这里** ——
	// 前提失效不会让测试变红，它只会让测试**不再证明任何东西**。
	for _, base := range []SyncReport{
		{Halt: HaltDone},
		{Halt: HaltDone, Misaligned: 1},
	} {
		// 前提当场自检：两组基底必须给出【不同】的 Complete()。
		// 不写这一句的话，下一次同样的塌陷仍然是静默的。
		if (SyncReport{Halt: HaltDone}).Complete() == (SyncReport{Halt: HaltDone, Misaligned: 1}).Complete() {
			t.Fatal("两组基底的 Complete() 相同 —— 这条不变性测试的前提没成立，" +
				"它现在什么也不证明")
		}
		want := base.Complete()
		for _, s := range shapes {
			r := base
			r.Gaps = s.gs
			if got := r.Complete(); got != want {
				t.Errorf("基底 Complete()=%v，而挂上「%s」之后变成 %v\n"+
					"  ⇒ Complete() 跟着 Gaps 动了，而它本该不看 Gaps", want, s.name, got)
			}
		}
	}
}

// TestReportCompleteSurvivesRoundTrip 派生值跨出这份内存表示之后，仍然要对得上。
//
// ⛔ 「派生值不该有存储位」只在**同一份内存表示**里成立 ——
// 报告要被序列化、被别的进程读，**跨出去那一刻它终究会变成一个副本**。
// 而「序列化那一步」是一个**位置**，所以它可以被守（评审方 2026-09-09 提）：
// **反序列化回来的 `Complete()` 必须等于序列化前的。**
func TestReportCompleteSurvivesRoundTrip(t *testing.T) {
	for _, r := range []SyncReport{
		{},
		{Misaligned: 3, AnomalousDays: []TradingDay{20200807}},
		{TruncatedTails: []string{"1m.dat: 33 字节"}, Bars: 12,
			Requested: [2]TradingDay{20200801, 20200810}, CoversOK: true},
	} {
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatalf("序列化失败：%v", err)
		}
		var back SyncReport
		if err := json.Unmarshal(b, &back); err != nil {
			t.Fatalf("反序列化失败：%v", err)
		}
		if back.Complete() != r.Complete() {
			t.Errorf("往返之后 Complete() 变了：%v -> %v\n  报告：%s",
				r.Complete(), back.Complete(), b)
		}
	}
}

// TestSyncRequestZeroToIsAQuestionNotAnAnswer `To == 0` 是**合法的哨兵**。
//
// ⚠️ 它与本仓别处「零值不合法」不冲突，而区别要写下来：
//
//	别处的 0  是一个 **answer**（会被误读成日期 / 根数）⇒ 危险
//	这里的 0  是一个 **question**（「你替我定末端」）⇒ 由 ClipToLastClosed 回答
//
// ⇒ 本条只钉住「它是被设计成哨兵的」这件事：`Validate` 一类的检查不该拒绝它。
func TestSyncRequestZeroToIsAQuestionNotAnAnswer(t *testing.T) {
	r := SyncRequest{Symbol: Symbol{Exchange: "SHFE", Product: "rb", YearMon: 2610},
		Period: Daily, From: 20200806}
	if r.To != 0 {
		t.Fatalf("这一条要的就是 To 的零值，实得 %s", r.To)
	}
	if !r.From.Valid() {
		t.Error("From 必填，而它应当是合法的")
	}
}
