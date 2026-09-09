package sinasource

import (
	"errors"
	"fmt"
	"time"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
)

// 这一片是【组装】：解析出来的行 ＋ 交易日历 → tickflow.Bar。
//
// 为什么必须有日历（而不是从行里直接算）：
//
//	Ts / TsEnd  是【时段】的起止，而一个交易日开了哪几段是逐日事实 ——
//	            有夜盘的品种，第一段落在【前一个自然日】的 21:00。
//	            光看 "2026-03-16" 这个字符串推不出 Ts。
//	判完结      上游没有标志位（probe.md 坑四），只能拿时段的收盘时刻和「现在」比。
//
// ⛔ **本片不联网。** HTTP 拉取是下一件；分开的理由同前几片：
// 组装的不变量要先能被单独测红，否则一条红了分不清是网络、是解析、还是日历。

// ErrCalendarGap 日历覆盖不到请求区间的一部分。
//
// ⛔ **这不是「那段没有数据」，也不返回部分结果。** 理由是具体的：
// 内置日历的生效起点是 2020-05-06，而新浪日线回溯到 2009 —— 中间十一年
// 日历一律答「不知道」。把那一段静默丢掉，Syncer 会把它记成
// **「拉过，确认没有」，然后永远不再重拉**（design.md §七 的第二类缺口）。
//
// ⇒ 所以这里照 Calendar.Walk 的判据办：**区间有一端落在覆盖之外就当场炸，
// 绝不静默少给。** 要部分数据的人，请把请求区间自己收窄 ——
// **那是一个看得见的选择，不是一个默认。**
var ErrCalendarGap = errors.New("sinasource: 日历覆盖不到请求区间——这是「答不了」，不是「没有数据」")

// ErrSinaDisagreesWithCalendar 新浪在日历说「不是交易日」的那天给了一根 K 线。
//
// 单列出来是因为**两边都可能是对的**：日历的交易日是调用方注入的（内置表不自带），
// 注入漏一天就长这样；而新浪也可能真的多给了一根。
// ⇒ 这需要人看一眼，不是重试或跳过能解决的，所以不吞。
//
// ⛔ **射程：只查【新浪多给】这一个方向。**
// 反方向 —— **日历说是交易日，而新浪没给这一天** —— **不报错，也不记录。**
//
//	实测（评审方 2026-09-09 造的对照组，我复现过）：
//	日历有 6 个交易日，从 6 行里抽掉一个 ⇒ **5 根、err == nil，一声不响。**
//
// ⚠️ 而这**不是遗漏，是决定**：那个方向的合法原因至少两类 ——
// **停牌**，以及**合约未上市**（本包 fixture 里 RB2610 首日 2025-10-16，
// 在那之前每一个交易日都「缺」）。
// ⇒ 无条件报错会天天响，而**一个天天响的检查迟早被关掉，连带把甲方向一起关掉**。
//
// ⛔ **登记⑨**：所以「日历说有、新浪没给」与「那天本来就不该有」，
// **在本层的输出上完全一样** —— 而**只有前者将来会被补上**。
// 正确的处置是把那些日子作为**第二个返回值**交给 Syncer 去判，
// 那是解析层 `null` ≠ `[]` 那件事再上一层。**本片没有 Syncer，所以只登记。**
var ErrSinaDisagreesWithCalendar = errors.New("sinasource: 新浪给了一根 K 线，而日历说那天不是交易日")

// ErrNotAscending 上游给的行不是按日期升序。
//
// ⛔ **不排序，报错。** 排一次就把「上游变了」这个事实抹掉了，
// 而它恰恰是最该被看见的一类变化。
// 实测（2026-09-09，三份 fixture 共 499 行）：**新浪日线一直是升序、无重复日期**。
// ⇒ 所以真出现乱序时，它是一个信号，不是一种需要容忍的常态。
var ErrNotAscending = errors.New("sinasource: 上游行不是按日期升序——不排序，因为排一次就把「上游变了」抹掉了")

// AssembleDaily 把解析出来的行组装成【已完结】的日线。
//
// now 是「现在」的墙钟毫秒，用来判完结。**由调用方给，不在内部取 time.Now()** ——
// 理由同 tickflow.CheckBars：内部取时钟的东西没法被测试固定住。
//
// 它做的事，以及每一件的判据：
//
//	日历覆盖    [req.From, req.To] 必须整段落在 cal.Covers 之内，否则 ErrCalendarGap
//	区间过滤    落在 [From, To] 之外的行直接跳过（新浪一次给全部历史，这是常态）
//	升序        检查，不排序 —— 乱序报 ErrNotAscending
//	Ts/TsEnd    取该交易日【实际】时段的首段开盘与末段收盘
//	完结        TsEnd > now 的整根丢掉（probe.md 坑四：上游会给未完结的那一根）
//	来源标记    FlagSrcSina
//
// ⚠️ 它**不**在返回前调用 tickflow.CheckBars，尽管那看起来很顺手：
// 那样一来「组装出来的东西满足 Source 契约」这句话就由被测者自己证明，
// 测试跟着变成同义反复。**契约由测试拿 CheckBars 去核，不由实现自己核。**
func AssembleDaily(rows []DailyRow, cal tickflow.Calendar, req tickflow.BarRequest, now int64) ([]tickflow.Bar, error) {
	if err := req.Validate(); err != nil {
		return nil, fmt.Errorf("sinasource: 请求不合法：%w", err)
	}
	if req.Period != tickflow.Daily {
		return nil, fmt.Errorf("sinasource: AssembleDaily 只组装日线，收到周期 %s", req.Period)
	}
	if cal == nil {
		return nil, errors.New("sinasource: 没给日历——Ts/TsEnd 与「已完结」都要靠它，不猜")
	}
	k := req.Symbol.ProductKey()

	// ── 一、覆盖：整段都要在，缺一端就炸 ──
	from, to, ok := cal.Covers(k)
	if !ok {
		return nil, fmt.Errorf("%w：日历完全答不了 %s", ErrCalendarGap, k)
	}
	if req.From < from || req.To > to {
		return nil, fmt.Errorf("%w：请求 [%s, %s]，而 %s 的日历只覆盖 [%s, %s]"+
			"（要那一段就把请求收窄，别指望这里静默少给）",
			ErrCalendarGap, req.From, req.To, k, from, to)
	}

	out := make([]tickflow.Bar, 0, len(rows))
	var prevDate tickflow.TradingDay
	for i, r := range rows {
		td, err := parseDate(r.Date)
		if err != nil {
			return nil, fmt.Errorf("sinasource: 第 %d 行：%w", i, err)
		}
		if prevDate != 0 && td <= prevDate {
			return nil, fmt.Errorf("%w：第 %d 行 %s 不晚于上一行 %s", ErrNotAscending, i, td, prevDate)
		}
		prevDate = td

		if td < req.From || td > req.To {
			continue // 新浪一次给全部历史，区间外是常态，不是异常
		}

		day, err := cal.DayOf(k, td)
		switch {
		case errors.Is(err, tickflow.ErrNotTradingDay):
			return nil, fmt.Errorf("%w：%s 的 %s。两边都可能对——"+
				"日历的交易日是调用方注入的（内置表不自带），注入漏一天就长这样",
				ErrSinaDisagreesWithCalendar, k, td)
		case err != nil:
			return nil, fmt.Errorf("sinasource: 第 %d 行取交易日 %s 失败：%w", i, td, err)
		}
		if len(day.Sessions) == 0 {
			return nil, fmt.Errorf("sinasource: %s 的 %s 一个时段都没有——"+
				"Ts/TsEnd 无从取值，而留零值上层只能猜", k, td)
		}

		ts := day.Sessions[0].Start
		tsEnd := day.Sessions[len(day.Sessions)-1].End
		if tsEnd > now {
			// 未完结的那一根。丢掉，不报错：上游会给它是【已知常态】（probe.md 坑四），
			// 而「未完结的绝不进入本库任何一层」是硬约定。
			continue
		}

		out = append(out, tickflow.Bar{
			Ts:           ts,
			TsEnd:        tsEnd,
			TradingDay:   td,
			Open:         r.Open,
			High:         r.High,
			Low:          r.Low,
			Close:        r.Close,
			Volume:       r.Volume,
			OpenInterest: r.OpenInterest,
			Settle:       r.Settle,
			Flags:        tickflow.FlagSrcSina,
		})
	}
	return out, nil
}

// parseDate 把 "2026-03-16" 变成 TradingDay(20260316)。
//
// ⚠️ 用 time.Parse 而不是自己切字符串：切字符串对 "2026-3-6" 这种
// **看起来对、位数不同**的输入会静默给出错误结果，而 time.Parse 会报错。
// 日线的日期就是交易日 —— 一根日线覆盖整个交易日，含前一晚的夜盘。
// （需要自然日→交易日换算的是【分钟线】，不是这里。）
func parseDate(s string) (tickflow.TradingDay, error) {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return 0, fmt.Errorf("日期 %q 不是 yyyy-mm-dd：%w", s, err)
	}
	td := tickflow.TradingDay(t.Year()*10000 + int(t.Month())*100 + t.Day())
	if !td.Valid() {
		return 0, fmt.Errorf("日期 %q 解析成 %d，不像一个交易日", s, int32(td))
	}
	return td, nil
}
