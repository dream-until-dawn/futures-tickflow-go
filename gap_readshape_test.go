package tickflow

import "testing"

// —— 换读法那一颗的第三道门：**读取的【形状】，不是它的答案** ——
//
// ⛔ 评审方 2026-09-10 登记成一道门：
// 「实现那一颗：`cov` 为空 ⇒ store 读取次数为 0，这一条**必须有断言**；没有断言就不放行。」
//
// 而他否掉的那个替代方案（「或者把『它没有守卫』写成一句明话」）理由我认：
// **明话拦不住把调用挪出循环的那个人** —— 本仓那条「写下来的边界拦不住下一个人」。
//
// ⚠️ 它守的是 `docs/design.md` 二十·六「问五」里那个**被钉死的调用点**：
//
//	DaysWithBars 按 coverage 段调用，且只对【与请求区间相交】的段调用
//
// 🔴 这一条承重的理由不是洁癖：`cov` 为空正是「**全新品种／周期的首次同步**」——
// 旧读法在那一族上**一次都不读**，而一个无条件预取的新读法会**读整个文件**。
// ⇒ 那一族是纯亏，且它恰好是最常见的第一次。

// countingReads 数「按段读」被调了几次、都读了哪些段。
type countingReads struct {
	n    int
	seen []Span
}

func (c *countingReads) fn(sp Span) (map[TradingDay]bool, error) {
	c.n++
	c.seen = append(c.seen, sp)
	return map[TradingDay]bool{}, nil
}

// guard: cov 为空 ⇒ 一次都不读存储（问五·甲那一族的唯一判据）。
// TestPlanGapsReadsNothingWhenNoCoverage 钉的就是那道门。
func TestPlanGapsReadsNothingWhenNoCoverage(t *testing.T) {
	var c countingReads
	gaps, err := PlanGaps(week(), testKey, 20200106, 20200110, nil, c.fn)
	if err != nil {
		t.Fatalf("不该出错：%v", err)
	}

	// 前提自检：这次调用真的走到了分类那一步 —— 否则「读了 0 次」是空转的。
	// 🔴 一个提前返回（区间不合法、日历不覆盖……）也会给出 0 次读，
	// 而那种 0 与「走到了、但按设计没读」是两回事。
	if len(gaps) == 0 {
		t.Fatal("一段缺口都没报 —— 这次调用多半没走到分类那一步，读数作废")
	}
	for _, g := range gaps {
		if g.Kind != GapNeverFetched {
			t.Fatalf("cov 为空时应当全是「没拉过」，却出现 %s —— 构造不成立", g.Kind)
		}
	}

	if c.n != 0 {
		t.Errorf("cov 为空，而按段读被调了 %d 次（读了 %v）\n"+
			"  ⇒ 调用点被挪出了循环，或者有人加了预取。\n"+
			"  ⇒ 这一族正是【全新品种／周期的首次同步】：\n"+
			"     旧读法在这里一次都不读，而一次无条件预取要扫整个文件。",
			c.n, c.seen)
	}
}

// guard: 这一天不落在任何 coverage 段里 ⇒ 也不读（同一条性质的另一半入口）。
// TestPlanGapsReadsNothingWhenDayOutsideEverySpan 补的是「cov 非空但不相交」那一格。
//
// ⛔ 它与上面那条**不是同一个入口**：上面 `cov == nil`（循环体一次都不进），
// 这里 `cov` 有一段而请求的那些天全都落在它之外（**循环进了，但每次都 continue**）。
//
// 🔴 **而这不是「多铺一格」，是那道门自己够不到的一格 —— 实测**：
// 突变「把调用挪到循环外面，无条件预取每一段」（＝问五·甲那一族回来）⇒
//
//	TestPlanGapsReadsNothingWhenNoCoverage        **绿**  ← 登记的那道门没抓住
//	TestPlanGapsReadsNothingWhenDayOutsideEverySpan **红**  ← 抓住它的是这一条
//
// 成因很直白：`cov == nil` 时那个预取循环**转 0 次**，读数照样是 0。
// ⇒ 归纳：**「一次都不读」这个断言，要挑一个【存储里确实有东西可读】的输入去问**
// —— 否则它和「没东西可读」区分不开，而后者与被测性质无关。
func TestPlanGapsReadsNothingWhenDayOutsideEverySpan(t *testing.T) {
	var c countingReads
	// 日历覆盖 0106..0112；coverage 只有 0111..0112（周末，不是交易日）
	// ⇒ 每个被 Walk 走到的交易日都落在那一段之外。
	cov := []SpanStatus{{Span: Span{From: 20200111, To: 20200112}}}
	gaps, err := PlanGaps(week(), testKey, 20200106, 20200110, cov, c.fn)
	if err != nil {
		t.Fatalf("不该出错：%v", err)
	}
	if len(gaps) == 0 {
		t.Fatal("一段缺口都没报 —— 读数作废")
	}
	if c.n != 0 {
		t.Errorf("请求的那些天都不落在任何 coverage 段里，而按段读仍被调了 %d 次（%v）",
			c.n, c.seen)
	}
}

// guard: 同一段最多读一次 —— 换读法的全部收益就在这里。
// TestPlanGapsReadsEachSpanAtMostOnce 钉的是那个按段记忆。
//
// ⛔ 没有它的话，「按段读」会退化成「**每天读一整段**」——
// 那比旧读法**更慢**（旧读法至少还有提前返回），而所有答案照旧正确。
// 🔴 **一次性能回退不会让任何断言变红**，所以它必须由一个数着次数的断言来守。
func TestPlanGapsReadsEachSpanAtMostOnce(t *testing.T) {
	var c countingReads
	// 一段盖住 0106..0110 全部五个交易日 ⇒ 逐日分类会问它五次，而只该读一次。
	cov := []SpanStatus{{Span: Span{From: 20200106, To: 20200110}}}
	gaps, err := PlanGaps(week(), testKey, 20200106, 20200110, cov, c.fn)
	if err != nil {
		t.Fatalf("不该出错：%v", err)
	}
	// 前提自检：这一段真的被问过 —— 0 次读在这里说明构造没走到，不是「记忆生效了」。
	// ⚠️ **「读 1 次」和「读 0 次」的区别，恰恰是这条测试的全部内容**：
	// 只断言 `<= 1` 的话，一个「根本没走到」的构造也能过。
	if c.n != 1 {
		t.Fatalf("这一段被读了 %d 次，期望【恰好 1 次】（读了 %v）\n"+
			"  ⇒ 大于 1：按段记忆没生效，退化成「每天读一整段」——\n"+
			"     那比旧读法更慢（旧读法还有提前返回），而答案全对、无人报警。\n"+
			"  ⇒ 等于 0：这个构造没走到分类那一步，读数作废。", c.n, c.seen)
	}
	if len(gaps) != 1 || gaps[0].Kind != GapConfirmedEmpty {
		t.Fatalf("期望五天合成一段「拉过确认没有」，实得 %v —— 构造不成立", gaps)
	}
}
