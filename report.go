package tickflow

import (
	"errors"
	"fmt"
)

// 本文件是 v0.3 同步层的【乙】：报告构造。对应 docs/design.md §七之十。
//
// ⛔ 三个都是**纯函数**：日历、周期、K 线、`now` 全由参数给，
// 内部不取时钟、不碰网络、不开文件 —— 同甲那一片的理由。
//
// 它们各自交出 `SyncReport` 里的一格，**而不组装那个结构体本身**：
// `Requested`/`Covered`/`Synced`/`Bars` 来自编排（丙）。

// NightAbsent 是「连续无夜盘」的那一段。
//
// ⛔ 具名，不用匿名内嵌结构体 —— 理由不是风格：`doccheck` 的围栏解析遇到嵌套的 `}`
// 会当成整个结构体结束，于是**它后面的声明全被丢掉**（2026-09-09 实测：
// 写成匿名内嵌，「文档声明」从 121 掉到 113，**8 处凭空消失，而 doccheck 报「一致」**）。
type NightAbsent struct {
	Days     int        // 连续多少个交易日
	From, To TradingDay // 哪一段
}

func (n NightAbsent) String() string {
	if n.Days == 0 {
		return "无连续无夜盘段"
	}
	return fmt.Sprintf("%s..%s 连续 %d 个交易日无夜盘", n.From, n.To, n.Days)
}

// ClipToLastClosed 把请求区间的末端截到**最后一个已经全部收盘**的交易日（SYN-1）。
//
// ⛔ **不用 `now - step`** —— 那是姊妹项目的一行算术，而本库必须问日历：
// 当前时刻属于哪个交易日、那一天的时段有没有走完。
// 新浪会把还在累积的那根一并返回并标上未来时刻（probe.md 坑四），
// 不主动挡就会存进去，**而且此后再不会去补**。
//
// ⛔ 第二个返回值**不可省**：请求区间里一天都还没收盘时，
// 「截到 `TradingDay(0)`」和「截到某一天」在返回值上**不可分辨** ——
// 同⑫ 那一格（`MaxBars: 0` 既是「没有硬顶」也是「一根都给不了」），也同 SYN-4。
//
// now 是毫秒时间戳，由调用方给。
func ClipToLastClosed(cal Calendar, k ProductKey, to TradingDay, now int64) (TradingDay, bool, error) {
	if cal == nil {
		return 0, false, errors.New("tickflow: ClipToLastClosed 需要一个日历——" +
			"「哪一根已经收盘」只有它答得了，而 now-step 是另一个库的算术")
	}
	if !to.Valid() {
		return 0, false, fmt.Errorf("tickflow: ClipToLastClosed 的 to=%d 不合法", int32(to))
	}
	cf, ct, ok := cal.Covers(k)
	if !ok {
		return 0, false, fmt.Errorf("tickflow: 日历覆盖不到 %s：%w", k, ErrUncovered)
	}
	if to < cf {
		return 0, false, nil // 请求整段都在覆盖之前 ⇒ 没有可用的一天，而这不是错误
	}
	hi := to
	if hi > ct {
		hi = ct
	}

	var last TradingDay
	var found bool
	if err := cal.Walk(k, cf, hi, func(d Day) bool {
		if len(d.Sessions) == 0 {
			return true // 那天一个时段都没有 ⇒ 无从判「收没收盘」，跳过
		}
		if d.Sessions[len(d.Sessions)-1].End <= now {
			last, found = d.Num, true
		}
		return true
	}); err != nil {
		return 0, false, fmt.Errorf("tickflow: ClipToLastClosed 遍历交易日失败：%w", err)
	}
	return last, found, nil
}

// ScanBars 交出报告里的两格：**对不上网格的根数**（SYN-2）与**可疑的交易日**（SYN-5）。
//
// ⛔ **两个返回值的单位【不同】，而那是有意的**：
//
//	misaligned  **根数** —— 它问的是「有多少根落在网格之外」
//	anomalous   **天**   —— 它问的是「哪几天可疑」
//
// **写在一起，免得下一个人去「统一」它们。**
//
// ⛔ 可疑与否**扫 `BarBound.Anomalous`，不自己再判一遍**（SYN-2）：
// v0.1 里那个标志一度挂在周期上，于是同一个「标称 330 / 实际 310」的事实
// 在 15m/30m 报、在 5m/60m/90m 不报。现在它由 `Day.TemplateMismatch` 按**交易日**判、
// 那天每一根都带 —— **上层只要扫这一位就不会按周期漏**。自己另写一套判定，
// 就是把那个坑重挖一遍。
//
// ⛔ 而 `anomalous` 记**交易日不记根数**（SYN-5）：记根数的话，
// 同一个事实在 60m 给约 6/交易日、在 1m 给约 345/交易日 —— **标志修到了交易日一级，
// 计数又把它挂回周期上**。而且报告是给人看的：人问的是「哪几天的数据可疑」。
func ScanBars(cal Calendar, k ProductKey, p IntradayPeriod, bars []Bar) (int, []TradingDay, error) {
	if cal == nil {
		return 0, nil, errors.New("tickflow: ScanBars 需要一个日历——网格由它的时段表算出来")
	}

	type dayGrid struct {
		bounds    []BarBound
		anomalous bool
	}
	grids := map[TradingDay]dayGrid{}
	var anomalous []TradingDay
	misaligned := 0

	for i, b := range bars {
		if !b.TradingDay.Valid() {
			return 0, nil, fmt.Errorf("tickflow: 第 %d 根的 TradingDay 是 %d——"+
				"本层不猜它属于哪一天（SRC-7：源必须填这一格）", i, int32(b.TradingDay))
		}
		g, seen := grids[b.TradingDay]
		if !seen {
			d, err := cal.DayOf(k, b.TradingDay)
			if err != nil {
				return 0, nil, fmt.Errorf("tickflow: 问日历要 %s 那一天失败：%w", b.TradingDay, err)
			}
			tmpl, err := cal.Template(k, b.TradingDay)
			if err != nil {
				return 0, nil, fmt.Errorf("tickflow: 问日历要 %s 的模板失败：%w", b.TradingDay, err)
			}
			g.bounds = p.Bars(tmpl, d)
			// ⛔ 扫这一位，不自己判。它按【交易日】给，所以在这里读一次就够。
			for _, bb := range g.bounds {
				if bb.Anomalous {
					g.anomalous = true
					break
				}
			}
			grids[b.TradingDay] = g
			if g.anomalous {
				anomalous = append(anomalous, b.TradingDay)
			}
		}
		if !onGrid(g.bounds, b) {
			misaligned++
		}
	}
	sortDays(anomalous)
	return misaligned, anomalous, nil
}

// onGrid 判「这一根的 Ts/TsEnd 落在网格的某一格上」。
//
// ⚠️ **两端都要比**：只比 Ts 的话，一根跨了两格的 K 线会被判成对齐 ——
// 而「起点对、终点不对」正是聚合口径出错时最常见的样子。
func onGrid(bounds []BarBound, b Bar) bool {
	for _, bb := range bounds {
		if bb.Open == b.Ts && bb.Close == b.TsEnd {
			return true
		}
	}
	return false
}

func sortDays(ds []TradingDay) {
	for i := 1; i < len(ds); i++ {
		for j := i; j > 0 && ds[j] < ds[j-1]; j-- {
			ds[j], ds[j-1] = ds[j-1], ds[j]
		}
	}
}

// ScanNightAbsent 交出「连续无夜盘的那一段」（SYN-7 / 8 / 9）。
//
// ⛔ **只报不判**（SYN-8）：给出那一段，**不下「模板过期」这个结论**。
// 一次政策性停夜盘与一次永久取消，在【发生的时候】是同一件事 ——
// 区别只在【后来会不会恢复】，而判据在下判断的那一刻并不拥有「后来」。
//
// 阈值 ≥2 **不是拍的**：`probe.md` 6.9，`KQ.m@SHFE.rb` 十年 2595 个交易日，
// 无夜盘的连续段分布是 **1 个交易日 × 52 段 ／ 64 个交易日 × 1 段**，
// **2 到 63 一次都没出现过**。
// ⚠️ 而那个分布**只量了一个品种 ⇒ 是下界不是全市场**。别的品种夜盘时长不同，
// 2020 年那次的起止也可能不同 —— 但「双峰、中间是空的」这个**形状**比那两个数字更可能一般化。
//
// ⛔ **第二个返回值 = 这个品种适不适用**（标称夜盘是否 > 0）。
// `CFFEX.IF` 标称夜盘就是 0（实测），它**从来没有过夜盘** ——
// 照直报的话它的每一天都是「无夜盘」，一报报它的整段历史，
// 而 SYN-7 防的是「**本来有夜盘、后来永久取消**」（那时相位网格仍按陈旧标称算，每根错 30 分钟）。
// **标称为 0 的品种没有这个失效模式** ⇒ 对它「无夜盘」不是事件，是常态。
// ⇒ 而「不适用」与「适用但没找到」在 `NightAbsent{Days: 0}` 上**不可分辨**，所以要分开返回。
//
// ⛔ **SYN-9：排除 `Covers().from` 那一天，不是排除序列的头一天。**
// 夜盘挂在**上一个交易日的自然日**上，而覆盖区间的第一天没有上一个交易日
// ⇒ 它的 `actual` 恒为 0，那是**日历的假象**。
// 而请求起点晚于 `Covers().from` 时，序列的头一天是**真实读数** ——
// 照「排除头一天」实现，`CFFEX.IF` 上两天皆真无夜盘会剩 1 天、低于阈值 ⇒ **整段静默不报**。
//
// ⚠️ **本函数只报【最长】的那一段**（并列取最早）。
// 多段的情形因此不可分辨 —— `NightAbsent` 这个类型装不下「有几段」。
// **这是已知的、写下来的边界，不是遗漏**；要分辨得先改那个公开类型。
func ScanNightAbsent(cal Calendar, k ProductKey, days []TradingDay) (NightAbsent, bool, error) {
	if cal == nil {
		return NightAbsent{}, false, errors.New("tickflow: ScanNightAbsent 需要一个日历")
	}
	if len(days) == 0 {
		return NightAbsent{}, false, nil
	}
	cf, _, covered := cal.Covers(k)
	if !covered {
		return NightAbsent{}, false, fmt.Errorf("tickflow: 日历覆盖不到 %s：%w", k, ErrUncovered)
	}

	// 一、先看标称。标称为 0 ⇒ 这个品种不适用，而那不是「没找到」。
	tmpl, err := cal.Template(k, days[0])
	if err != nil {
		return NightAbsent{}, false, fmt.Errorf("tickflow: 问日历要 %s 的模板失败：%w", days[0], err)
	}
	if tmpl.NightMinutes() <= 0 {
		return NightAbsent{}, false, nil
	}

	// 二、逐日看实际夜盘；排除 Covers().from 那一天（SYN-9）。
	//
	// ⛔ **days 必须严格升序** —— 这个前提此前没写出来，而它是「连续」这个词的定义：
	// 乱序或有重复时，「相邻两项」就不再是「相邻两个交易日」，
	// 而算出来的段**读起来仍然像一个段**（同⑫ 那一族：坏输入产出一个像样的答案）。
	// ⇒ 这一条是自查对照组逼出来的：那个「覆盖首日不断开当前段」的突变**打不中**，
	// 因为在升序里 cf 只可能是【头一个】—— 于是那一行断开的代码是死的。
	// **一行打不中的代码，要么删掉，要么说明它为什么打不中；这里两样都做了。**
	for i := 1; i < len(days); i++ {
		if days[i] <= days[i-1] {
			return NightAbsent{}, false, fmt.Errorf(
				"tickflow: ScanNightAbsent 要求 days 严格升序，而 days[%d]=%s <= days[%d]=%s"+
					"——乱序时「连续 N 天」算出来仍然像个段，而它不是",
				i, days[i], i-1, days[i-1])
		}
	}

	var best, cur NightAbsent
	for _, n := range days {
		if n == cf {
			// 升序 ⇒ cf 只可能是头一个 ⇒ 此处 cur 必为零值，不必再断开。
			continue
		}
		d, err := cal.DayOf(k, n)
		if err != nil {
			return NightAbsent{}, false, fmt.Errorf("tickflow: 问日历要 %s 那一天失败：%w", n, err)
		}
		t, err := cal.Template(k, n)
		if err != nil {
			return NightAbsent{}, false, fmt.Errorf("tickflow: 问日历要 %s 的模板失败：%w", n, err)
		}
		_, actual, _ := d.TemplateMismatch(t)
		if actual > 0 {
			cur = NightAbsent{}
			continue
		}
		if cur.Days == 0 {
			cur = NightAbsent{Days: 1, From: n, To: n}
		} else {
			cur.Days++
			cur.To = n
		}
		if cur.Days > best.Days {
			best = cur
		}
	}
	if best.Days < 2 {
		return NightAbsent{}, true, nil // 适用，而没有 ≥2 的段
	}
	return best, true, nil
}
