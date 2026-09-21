package tickflow

import (
	"errors"
	"fmt"
	"time"
)

// v0.10 P-a：Feed.Push —— 读完历史之后接着收实时的 1m（设计 design.md §十五「v0.10 起手」五 L2 / L3）。

// ErrPushGap：Push 进来的这根不紧接上一根，中间漏了至少一格。
//
// 天勤不管那一分钟有没有成交都给根（probe.md 6.13 c″、6.20 E2）⇒ 跳格就是漏了 —— 常见的来由是断线重连之后
// 从「新的当前根」接着推、中间收盘的根没补（design.md 二·戊）。报文写出缺的第一格；Feed 的状态不动。
// ⚠️ 射程：「每分钟有根」是活跃品种的读数；不活跃品种若某分钟真没有根，这里会误报 —— 误报的方向是停下来，不是静默。
var ErrPushGap = errors.New("tickflow: Push 跳格了 —— 推入的这根不紧接上一根，中间漏了至少一格")

// pushState 是 Push 的接续状态。
type pushState struct {
	started bool
	pushed  bool       // 至少有一根 Push 成功过 ⇒ lastTs 才是「推过的那根」
	lastTs  int64      // 上一根推入的 1m 的开盘时刻 —— 用来认「重复」（pushed 为假时不用：起步时早于接续点的根不是推过的）
	nextTs  int64      // 下一根必须是这一分钟
	nextDay TradingDay // ……且属于这个交易日
	day     Day        // nextDay 那一天（缓存）
	nextErr error      // 下一格算不出来（日历覆盖到头）⇒ 这一根照收，错误留到下一次 Push 才报

	// 主周期长于 1m 时：当前主周期格子已攒的 1m、这一格、这一格所在的那一天（格子不跨交易日）
	pend    []Bar
	cell    BarBound
	cellDay Day
}

// Push 收一根【已完结】的 1m，接在 src 的最后一根之后，与 Next 走同一个步进（L1 / L2）。
//
// 主周期是 1m ⇒ 每推一根步进一次；主周期更长（日内周期或 Daily）⇒ 按 FeedConfig.Rule 攒 1m，推入那根的 TsEnd
// 等于当前主周期格子的收盘时刻时，用与 Aggregate / 辅周期同一个 aggCell 出一根、步进一次；其余的 Push 只攒不步进。
// 返回值 stepped 报告这一次有没有步进（有 ⇒ View / TF / 指标都前进了一根主周期）。
//
// 只在 Next 已返回 false 且 Err() 为 nil 之后收（src 读完、第二遍核过；V2 甲：一条时间线）。
// Feed 不判完结：推进来的必须已经完结（判完结是源的事，shinnysource.Live）。
//
// 任一不过 ⇒ 报错，**Feed 的状态一格不动**（L3）：
//
//	重复（＝ 上一根）· 乱序（早于下一格）· 跳格（晚于下一格，errors.Is(err, ErrPushGap)，报文写缺的第一格）·
//	不在 1m 格子上（开盘不整分、长度不是一分钟、不在交易时段里）· 交易日与日历不符
//
// 「下一格」按日历算：跨小节休息 / 午休 / 日夜盘 / 交易日都是日历上的下一个交易分钟。
// 第一根必须是 src 最后一根主周期根【之后那一格】的第一分钟 —— 主周期更长时不许从格子中间接（攒出来的那根会缺头）。
//
// ⚠️ src 里主周期根的聚合口径由写库的一方决定，Push 攒出来的按 FeedConfig.Rule；两者不一致时 Feed 只能查到
// 「src 最后一根的收盘时刻不是 Rule 的格子收盘」这一种（起步时报错），格子相同而字段取法不同的，查不到 —— 由调用方保证同一口径。
func (f *Feed) Push(b Bar) (stepped bool, err error) {
	if f.err != nil {
		return false, f.err
	}
	if f.closed {
		return false, errors.New("tickflow: Feed 已经 Close，不再收 Push")
	}
	if f.base.main != nil {
		return false, errors.New("tickflow: 主连模式（FeedConfig.Main）不收 Push —— v0.10 不做实时主连")
	}
	for _, e := range f.extras {
		if e.walker {
			return false, errors.New("tickflow: 日线辅周期来自 DailyWalker，实时里没有新日线 —— 改用由主周期聚合的日线（不给 DailyWalker）；" +
				"不让 TF(\"1d\") 停在库里最后一天：停住是静默的，看起来正常、其实落后")
		}
	}
	if !f.srcDone {
		return false, errors.New("tickflow: src 还没读完（Next 还没返回 false）—— Push 只接在历史之后，两条时间线不能交错")
	}
	st := f.push
	if !st.started {
		if st, err = f.pushAnchor(); err != nil {
			return false, err
		}
	}
	if st.nextErr != nil {
		return false, st.nextErr
	}
	if b.Ts%60000 != 0 || b.TsEnd != b.Ts+60000 {
		return false, fmt.Errorf("tickflow: Push 只收 1m：这根 [%s, %s) 不在 1m 格子上", showTs(b.Ts), showTs(b.TsEnd))
	}
	if b.Ts != st.nextTs {
		switch {
		case !st.pushed && b.Ts < st.nextTs:
			// 起步后还没推成过：早于接续点的那一分钟是 src 里的（主周期更长时甚至不在 src 里），不是「推过的」（评审方 09-21）
			return false, fmt.Errorf("tickflow: Push 的这根 %s 早于接续点 %s（src 已含到 %s 之前）", showTs(b.Ts), showTs(st.nextTs), showTs(st.nextTs))
		case b.Ts == st.lastTs:
			return false, fmt.Errorf("tickflow: Push 重复了 —— %s 那根已经推过（下一格应是 %s）", showTs(b.Ts), showTs(st.nextTs))
		case b.Ts < st.nextTs:
			return false, fmt.Errorf("tickflow: Push 乱序了 —— %s 早于下一格 %s", showTs(b.Ts), showTs(st.nextTs))
		case !f.onTradingMinute(b.Ts):
			return false, fmt.Errorf("tickflow: Push 的这根 %s 不在任何交易时段的 1m 格子上", showTs(b.Ts))
		default:
			return false, fmt.Errorf("%w：收到 %s，缺的第一格是 %s（交易日 %s）", ErrPushGap, showTs(b.Ts), showTs(st.nextTs), st.nextDay)
		}
	}
	if b.TradingDay != st.nextDay {
		return false, fmt.Errorf("tickflow: Push 的这根 %s 标的交易日是 %s，日历说是 %s", showTs(b.Ts), b.TradingDay, st.nextDay)
	}
	// 下一格先算好。算不出来（日历覆盖到头）不是这一根的错 ⇒ 这一根照收，错误留到下一次 Push 报（那时状态照样不动）
	nextDay, nextTs, nd, nextErr := f.minuteAfter(st.day, b.TsEnd)
	bday := st.day // 校验过：b 属于 st.nextDay，而 st.day 就是那一天
	aggBase := f.basePer != Period(MustIntraday(1))
	cell, cellDay := st.cell, st.cellDay
	if aggBase && len(st.pend) == 0 {
		if cell, err = f.baseCell(bday, b); err != nil {
			return false, err
		}
		cellDay = bday
	}

	// ── 以下改状态（上面任一处报错都走不到这里）──
	st.pushed, st.lastTs, st.nextTs, st.nextDay, st.day, st.nextErr = true, b.Ts, nextTs, nextDay, nd, nextErr
	if !aggBase {
		f.push = st
		if err := f.step(b); err != nil {
			f.err = err
			return false, err
		}
		return true, nil
	}
	st.cell, st.cellDay = cell, cellDay
	st.pend = append(st.pend, b)
	if b.TsEnd != cell.Close {
		f.push = st
		return false, nil
	}
	agg, _ := aggCell(cell, cellDay, st.pend)
	st.pend = nil
	f.push = st
	if err := f.step(agg); err != nil {
		f.err = err
		return false, err
	}
	return true, nil
}

// pushAnchor 算起步的接续状态：src 最后一根主周期根之后那一格的第一分钟。
func (f *Feed) pushAnchor() (pushState, error) {
	if f.base.n == 0 {
		return pushState{}, errors.New("tickflow: src 一根主周期根都没有 —— 不知道 Push 该从哪一格接（v0.10 只做「先读历史、再接推送」）")
	}
	last := f.base.view().Bar()
	// src 末根不完整 ⇒ 不许接（评审方 2026-09-21 实测）：盘中 Sync 完、主周期 15m 时末根大概率就是这个形状 ——
	// 接上的话，这一格停在不完整的值上、后面照常步进，格子里剩下的分钟永远进不来，也没有任何报错
	if last.Flags&FlagPartial != 0 {
		return pushState{}, fmt.Errorf("tickflow: src 最后一根 [%s, %s) 带 FlagPartial —— src 截在了格子中间（多半是盘中拉的），Push 接不上："+
			"把 NewFeed 的 src 截到上一个完整的格子，剩下的交给源侧从历史通道补（shinnysource.Live 的起步补齐）", showTs(last.Ts), showTs(last.TsEnd))
	}
	d, err := f.cal.DayOf(f.key, last.TradingDay)
	if err != nil {
		return pushState{}, fmt.Errorf("tickflow: Push 起步问不到 %s 那一天的时段: %w", last.TradingDay, err)
	}
	var from int64
	switch p := f.basePer.(type) {
	case IntradayPeriod:
		// src 最后一根的收盘时刻必须是 Rule 的某个格子收盘（口径一致性唯一查得到的一格）
		tmpl, err := f.cal.Template(f.key, d.Num)
		if err != nil {
			return pushState{}, fmt.Errorf("tickflow: Push 起步问不到 %s 的时段模板: %w", d.Num, err)
		}
		cells, err := f.rule.Bounds(p, tmpl, d)
		if err != nil {
			return pushState{}, err
		}
		found := false
		for _, c := range cells {
			found = found || c.Close == last.TsEnd
		}
		if !found {
			return pushState{}, fmt.Errorf("tickflow: src 最后一根 [%s, %s) 的收盘不是 %s 在 FeedConfig.Rule 下的任何格子收盘 —— 库里的根与 Rule 口径不一致，Push 接不上",
				showTs(last.Ts), showTs(last.TsEnd), p)
		}
		from = last.TsEnd
	default: // Daily：下一个交易日的第一分钟
		from = d.Sessions[len(d.Sessions)-1].End
	}
	nextDay, nextTs, nd, err := f.minuteAfter(d, from)
	if err != nil {
		return pushState{}, err
	}
	lastTs := last.TsEnd - 60000
	return pushState{started: true, lastTs: lastTs, nextTs: nextTs, nextDay: nextDay, day: nd}, nil
}

// minuteAfter 给 t 之后（含 t）的第一个交易分钟：先在 d 里找，d 里没有了就到下一个交易日的第一段开盘。
func (f *Feed) minuteAfter(d Day, t int64) (TradingDay, int64, Day, error) {
	for _, s := range d.Sessions {
		if s.End > t {
			return d.Num, max(t, s.Start), d, nil
		}
	}
	_, to, ok := f.cal.Covers(f.key)
	if !ok || d.Num >= to {
		return 0, 0, Day{}, fmt.Errorf("tickflow: 日历覆盖不到 %s 之后的交易日 —— Push 算不出下一格", d.Num)
	}
	var nd Day
	found := false
	err := f.cal.Walk(f.key, d.Num, to, func(x Day) bool {
		if x.Num > d.Num {
			nd, found = x, true
			return false
		}
		return true
	})
	if err != nil {
		return 0, 0, Day{}, fmt.Errorf("tickflow: Push 找 %s 的下一个交易日: %w", d.Num, err)
	}
	if !found || len(nd.Sessions) == 0 {
		return 0, 0, Day{}, fmt.Errorf("tickflow: 日历里 %s 之后没有带时段的交易日 —— Push 算不出下一格", d.Num)
	}
	return nd.Num, nd.Sessions[0].Start, nd, nil
}

// baseCell 给 b 所在的主周期格子（日内周期按 Rule.Bounds；Daily 是一整天）。
func (f *Feed) baseCell(d Day, b Bar) (BarBound, error) {
	if p, ok := f.basePer.(IntradayPeriod); ok {
		tmpl, err := f.cal.Template(f.key, d.Num)
		if err != nil {
			return BarBound{}, fmt.Errorf("tickflow: Push 问不到 %s 的时段模板: %w", d.Num, err)
		}
		cells, err := f.rule.Bounds(p, tmpl, d)
		if err != nil {
			return BarBound{}, err
		}
		for _, c := range cells {
			if b.Ts >= c.Open && b.TsEnd <= c.Close {
				return c, nil
			}
		}
		return BarBound{}, fmt.Errorf("tickflow: Push 的这根 %s 不在 %s 的任何主周期格子里", showTs(b.Ts), p)
	}
	return BarBound{Open: d.Sessions[0].Start, Close: d.Sessions[len(d.Sessions)-1].End, Full: true}, nil
}

// onTradingMinute：ts 开盘的那一分钟整个落在某个交易时段里。
func (f *Feed) onTradingMinute(ts int64) bool {
	d, err := f.cal.DayAt(f.key, ts)
	if err != nil {
		return false
	}
	for _, s := range d.Sessions {
		if s.Contains(ts) {
			return ts+60000 <= s.End
		}
	}
	return false
}

func showTs(ms int64) string { return time.UnixMilli(ms).In(CST).Format("2006-01-02 15:04") }
