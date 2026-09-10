package tickflow

import (
	"encoding/json"
	"math/rand"
	"reflect"
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
		// ⚠️ 这一格是【补的】—— UngatedSource 落地时没有被加进这张表，
		// 而表本身不会因为少一行而变红：
		// **一张「每一格都要在」的表，少一格的样子和它满的样子一模一样。**
		// ⇒ 它和 `absent` / `elsewhere` 那一族同形：登记表自己需要一张盯着它的网。
		{"闸门没被用到", SyncReport{Halt: HaltDone, UngatedSource: []string{"5 次 / 0 次"}}},
		{"中止：预算耗尽", SyncReport{Halt: HaltBudget}},
		{"中止：被取消", SyncReport{Halt: HaltContext}},
		{"中止：没记录理由（零值）", SyncReport{Halt: HaltUnknown}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if c.r.Complete() {
				t.Errorf("%s 单独出现时 Complete() 仍为 true —— 这一格没有参与派生", c.name)
			}
			// ⛔ **而「恰好一条」比「bool 翻了」强一格**（⑥ 落地之后才写得出来）：
			// 一个 bool 只答「有没有」——**它同时看不见「没参与」和「被数了两次」**。
			// 而 `Incidents()` 让这两种都变成读数：0 条 / 2 条。
			if got := c.r.Incidents(); len(got) != 1 {
				t.Errorf("%s 单独出现时交出 %d 条痕迹，期望【恰好 1 条】：%v"+
					"（0 条 ⇒ 这一格没有参与；2 条 ⇒ 它被数了两次）", c.name, len(got), got)
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

// randomGaps 造一份随机缺口：段数、跨度、类别、总天数、顺序**全随机**。
func randomGaps(rnd *rand.Rand) []Gap {
	kinds := []GapKind{GapNeverFetched, GapConfirmedEmpty, GapNotTrading,
		GapCalendarUnknown, GapStoreUnverified, GapStoreLegacy}
	n := rnd.Intn(13) // 0..12 段 —— 含 0，也含「多于 6」
	gs := make([]Gap, 0, n)
	for i := 0; i < n; i++ {
		from := TradingDay(20200101 + rnd.Intn(10000))
		gs = append(gs, Gap{
			From: from,
			To:   from + TradingDay(rnd.Intn(60)), // 跨度 0..59
			Kind: kinds[rnd.Intn(len(kinds))],     // 类别可重复 —— 绕法三钻的正是这一维
		})
	}
	return gs
}

// TestReportCompleteIsInvariantUnderRandomGaps ⑤：把上面那条不变性**随机化**。
//
// 🔴 **它补的是手挑形状的天花板**：上面那条用五个手挑的形状，
// 而评审方两轮里一共钻穿了它**四次**，每次钻的都是一个不同的维度 ——
//
//	绕法一 跨度（那五个形状里最长的是多日一段）
//	绕法二 段数（恰好 7）
//	绕法三 **同一类别出现两次**（五个形状里没有一个含 2× GapStoreLegacy）
//	绕法四 缺口覆盖的总天数（形状里最大恰好 31）
//
// ⇒ 根子是那句：**一个具体 fixture 天生在每一维上都取了一个值，
// 而断言只在那些值上成立。** 手挑形状永远补不完维度 —— 因为维度不是有限枚举的。
//
// ⇒ 随机化**一次盖住那四维**（段数 0..12 · 跨度 0..59 · 类别可重复 · 起点随机）。
// ⚠️ 而它**不取代**上面那条：手挑的那几个形状说清了「我们在意哪些边界」，
// 随机的这条说清「不止那几个」。**两条都留。**
//
// ⚠️ 种子写死，理由是**失败必须可复现** —— 一条「每次随机、红了查不出为什么」
// 的测试，迟早会被人加个 skip（本仓已记过那个下场）。
func TestReportCompleteIsInvariantUnderRandomGaps(t *testing.T) {
	const seed = 20260910
	rnd := rand.New(rand.NewSource(seed))

	// 前提自检：两组基底必须给出不同的 Complete()（同上面那条）。
	a := SyncReport{Halt: HaltDone}
	b := SyncReport{Halt: HaltDone, Misaligned: 1}
	if a.Complete() == b.Complete() {
		t.Fatal("两组基底的 Complete() 相同 —— 前提没成立，这条测试什么也不证明")
	}

	const rounds = 500
	var maxSegs, maxSpan, maxDays int
	for i := 0; i < rounds; i++ {
		gs := randomGaps(rnd)
		if len(gs) > maxSegs {
			maxSegs = len(gs)
		}
		days := 0
		for _, g := range gs {
			if s := int(g.To - g.From); s > maxSpan {
				maxSpan = s
			}
			days += int(g.To-g.From) + 1
		}
		if days > maxDays {
			maxDays = days
		}
		for _, base := range []SyncReport{a, b} {
			r := base
			r.Gaps = gs
			if got := r.Complete(); got != base.Complete() {
				t.Fatalf("第 %d 轮（seed=%d）：基底 Complete()=%v，挂上 %d 段缺口之后变成 %v\n"+
					"  缺口：%v\n  ⇒ Complete() 跟着 Gaps 动了，而它本该不看 Gaps",
					i, seed, base.Complete(), len(gs), got, gs)
			}
		}
	}
	// ⛔ **基线：随机化必须真的走到过那几个边界** ——
	// 一份「每轮都生成 0 段」的随机器会让这 500 轮**全部空转**，而它照样绿。
	if maxSegs < 7 || maxSpan < 10 || maxDays < 32 {
		t.Fatalf("%d 轮里最大段数=%d 最大跨度=%d 最大总天数=%d ——"+
			"没有走到评审方钻过的那几维（段数>6 · 多日 · 总天数>31），"+
			"这时【不能】当成通过", rounds, maxSegs, maxSpan, maxDays)
	}
	t.Logf("%d 轮：最大段数=%d 最大跨度=%d 最大总天数=%d", rounds, maxSegs, maxSpan, maxDays)
}

// intProbes 是喂给整数型字段的候选值。**报文里会原样印出来**，
// 好让「没找到」保持是一个【读数】（我试过这几个），而不是一句【成因】（它不产生痕迹）。
var intProbes = []int64{1, 2, 3, 4}

// candidates 给一个字段造【若干个】非零值，用来问「动它会不会产生痕迹」。
//
// 🔴 **它返回的是一组值，不是一个 —— 这一格是被自己的守卫当场抓到的**：
// 上一版只造一个 `1`，而 `HaltReason(1)` 恰好是 `HaltDone`，**干净的那个**
// ⇒ 守卫报「Halt 不再产生痕迹 —— 幽灵项」，而 `Halt` 明明是参与派生的。
//
// > ⛔ **一个探针只喂一个值时，「这一格不影响结果」与
// > 「这个值恰好是干净的那一个」不可分辨。**
// ⇒ 而这正是本仓那条阶梯的第三次换装（`tools/probe/README.md`）：
// **1 个已知值挡不住「恰好等于那个值」；要两个不同的。**
// 这里是它的第三个用处：**不是写断言，不是读别人给的值，是【造探针的输入】。**
func candidates(f reflect.Value) ([]reflect.Value, bool) {
	t := f.Type()
	switch t.Kind() {
	case reflect.Int, reflect.Int32, reflect.Int64:
		// intProbes：枚举型字段的「干净值」常常是 1，只喂 1 会把它读成「不参与」。
		// ⚠️ 它是一个**魔法范围**，而报文里会把它原样印出来 ——
		// 这样「没找到」读起来才是一个读数，不是一句成因。
		var out []reflect.Value
		for _, n := range intProbes {
			out = append(out, reflect.ValueOf(n).Convert(t))
		}
		return out, true
	case reflect.Bool:
		return []reflect.Value{reflect.ValueOf(true)}, true
	case reflect.Slice:
		e := t.Elem()
		v := reflect.MakeSlice(t, 1, 1)
		switch e.Kind() {
		case reflect.String:
			v.Index(0).SetString("x")
		case reflect.Int32, reflect.Int, reflect.Int64:
			v.Index(0).SetInt(20200807)
		case reflect.Struct:
			// Gap 这类：留零值元素即可，它只需要「切片非空」
		default:
			return nil, false
		}
		return []reflect.Value{v}, true
	case reflect.Array:
		v := reflect.New(t).Elem()
		if t.Len() > 0 && v.Index(0).CanSet() && v.Index(0).Kind() == reflect.Int32 {
			v.Index(0).SetInt(20200807)
			return []reflect.Value{v}, true
		}
		return nil, false
	case reflect.Struct:
		v := reflect.New(t).Elem()
		if t.NumField() > 0 && v.Field(0).CanSet() {
			switch v.Field(0).Kind() {
			case reflect.Int, reflect.Int32, reflect.Int64:
				v.Field(0).SetInt(1)
				return []reflect.Value{v}, true
			}
		}
		return nil, false
	}
	return nil, false
}

// TestEveryIncidentProducingFieldIsInTheTable 是那张表的**第二张网**。
//
// 🔴 **它存在的理由是一处刚发生的漏登记**：`UngatedSource` 落地时
// **没有被加进上面那张「每一格都要能单独翻它」的表**，而表照绿。
//
// > ⛔ **一张「每一格都要在」的表，少一格的样子和它满的样子一模一样。**
//
// ⇒ 而这与本仓 `absent` / `elsewhere` / `guardNames` 那一族同形：
// **登记表自己需要一张盯着它的网** —— 否则「登记」这个动作本身没有守卫。
//
// ⇒ 这一张网的做法：**反射枚举 `SyncReport` 的每一个字段**，逐个把它设成非零值，
// 看 `Incidents()` 会不会因此多出东西。**会的那些，必须在上面那张表里出现过。**
//
// ⚠️ 射程两条，和它一起读：
//
//	一 它按【字段名】对表，不按语义 —— 表里那一行有没有真的断言对，它管不了。
//	二 造不出「非零值」的字段（本函数 nonZero 返回 false 的那些）它跳过 ——
//	  而**跳过了几个会被打印出来**：跳过数一旦变多，说明这张网在悄悄变松。
func TestEveryIncidentProducingFieldIsInTheTable(t *testing.T) {
	// 上面那张表覆盖到的字段（人工登记 —— 而它正是被这条测试盯着的那一半）。
	covered := map[string]bool{
		"TruncatedTails":       true,
		"LegacyMetaDiscarded":  true,
		"LegacyMetaUnverified": true,
		"Misaligned":           true,
		"AnomalousDays":        true,
		"UngatedSource":        true,
		"Halt":                 true,
	}

	base := SyncReport{Halt: HaltDone}
	if len(base.Incidents()) != 0 {
		t.Fatal("基底就带着痕迹 —— 前提没成立，下面每一格的比较都没有意义")
	}

	rt := reflect.TypeOf(base)
	var skipped []string
	var found []string
	for i := 0; i < rt.NumField(); i++ {
		name := rt.Field(i).Name
		v := reflect.New(rt).Elem()
		v.Set(reflect.ValueOf(base))
		cands, ok := candidates(v.Field(i))
		if !ok {
			skipped = append(skipped, name)
			continue
		}
		produces := false
		for _, c := range cands {
			v.Set(reflect.ValueOf(base)) // 每一轮都从干净的基底重来
			v.Field(i).Set(c)
			if len(v.Interface().(SyncReport).Incidents()) > 0 {
				produces = true
				break
			}
		}
		if !produces {
			continue // 这一格不产生痕迹（Requested / Covered / Bars / Gaps …）
		}
		found = append(found, name)
		if !covered[name] {
			t.Errorf("字段 %s 动一下就会产生痕迹，而它【不在那张表里】——\n"+
				"  ⇒ 表少一行不会变红：少一格的样子和它满的样子一模一样。\n"+
				"  ⇒ 去上面那张用例表里补一行，再回来把它加进 covered", name)
		}
	}
	for name := range covered {
		hit := false
		for _, f := range found {
			if f == name {
				hit = true
			}
		}
		if !hit {
			// ⛔ **报文只说【读数】，不说【成因】，也不给动作**（评审方 2026-09-10 提，我认）：
			// 上一版写的是「而它【不再产生痕迹】—— 幽灵项，删掉它」，
			// 而本函数实际知道的只是「**我试过的那几个取值里**没有一个产生痕迹」。
			//
			// 🔴 而它比一句普通的未验成因**多错一格：它还给出了一个动作**。
			// ⇒ `1..4` 是一个魔法范围，今天够用只因为 `HaltReason` 的留声值恰好落在 2、3；
			// 一个痕迹值都 >= 5 的未来枚举，会让上一版**建议你删掉一条真的登记**。
			//
			// > **一条基于未验成因的【建议】，比一条未验的【陈述】多错一格。**
			t.Errorf("covered 里登记了 %s，而【我试过的取值 %v 里没有一个产生痕迹】"+
				"（若它的痕迹值落在这几个之外，该扩的是 candidates，不是删登记；"+
				"确认它真的不再产生痕迹了，才把它从 covered 删掉）", name, intProbes)
		}
	}
	// ⛔ 基线：不能一格都没找到（那时上面每一条都会「通过」）。
	if len(found) == 0 {
		t.Fatal("一个会产生痕迹的字段都没找到 —— 多半是 nonZero 造不出值了；" +
			"这时【不能】当成通过")
	}
	// ⛔ **`skipped` 的条数也是一个要盯的数**（评审方 2026-09-10 提）：
	// `candidates` 造不出值的字段会**静默地**落进这一栏 ——
	// 而那时这条守卫对它是瞎的，**却仍然全绿**。
	// ⇒ 今天是 0。它一旦不是 0，两条出路，二选一：
	//	一 扩 `candidates`，让它造得出那个类型的值
	//	二 那个字段确实不可能产生痕迹 ⇒ 在这里写明是哪一个、凭什么
	// **而「让它留在 skipped 里不管」不是出路** —— 那是让守卫悄悄变松。
	if len(skipped) != 0 {
		t.Errorf("有 %d 个字段造不出探针值，被跳过了：%v"+
			"（这条守卫对它们是瞎的，而它仍然会全绿 —— 扩 candidates，或在这儿写明凭什么跳过）",
			len(skipped), skipped)
	}
	t.Logf("会产生痕迹的字段 %d 个：%v；跳过 %d 个（整数候选值 %v）",
		len(found), found, len(skipped), intProbes)
}
