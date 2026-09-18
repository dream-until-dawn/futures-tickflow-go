package tickflow

import (
	"errors"
	"fmt"
	"iter"
	"math"
	"reflect"
	"strings"
)

// FeedConfig 描述一个 Feed。设计见 docs/design.md §十五「v0.9 起手」五（F1–F7）。
//
// v0.9：主周期（F-a）＋ 辅周期（F-b）；主连四方法在 F-c。
type FeedConfig struct {
	// Key 与 Calendar 用来回答「某个交易日有哪些格子」（预热往前数格子 F2、辅周期的边界与「已收盘」F4）。
	Key      ProductKey
	Calendar Calendar

	// Base 是步进的主周期：日内周期（IntradayPeriod），或 Daily。src 里存的就是这个周期的根。
	Base Period

	// Extra 是辅周期，只读不步进，永远停在【最后一根已收盘】的位置上（F3 / F4）：
	//
	//	日内周期  由主周期按 Rule 聚合；必须长于主周期、且是它的整数倍；主周期是 Daily 时不许有
	//	Daily     默认由主周期按交易日聚合（没有结算价，Settle 为 NaN）；给了 DailyWalker 就从它读（带结算价）
	//
	// 「已收盘」只看日历给的格子收盘时刻 ≤ 主周期当前那根的 TsEnd，**不看数据来没来**（F4）。
	Extra []Period

	// DailyWalker 给了，Daily 辅周期就从它读（新浪 / 中金所日线，带结算价）。
	// 它的根按交易日与主周期对齐；某个已收盘的交易日它没有根 ⇒ 那几步 TF("1d") 给无效视图（NaN），不补、不猜（F3 b）。
	// 它同样先核后流、同样只收单段；第二遍报错同样经 Err() 报 ErrFeedVoided。
	DailyWalker BarWalker

	// Main 给了 ⇒ **主连模式**：主周期必须是 Daily（于是也没有辅周期）；View 的四个主连方法
	// （RawClose · Contract · IsRollDay · Basis）按主周期那根的交易日问它（F5）。
	// continuous.Continuous 实现它。⛔ 主连模式下根由 Main.Walker() 供，NewFeed 的 src 必须传 nil（传了就报错）。
	// ⚠️ 日内主连 v0.9 不做（用户 2026-09-18 裁 U5）。
	Main MainSource

	// Rule 是聚合口径。⛔ **必填，零值 ⇒ ErrAggRuleUnset** —— 不论有没有日内辅周期
	// （用户 2026-09-18 裁：照「构造 Feed 不给就报错」的字面，不放宽）。
	Rule AggRule

	// From / To 是步进的交易日闭区间，都必填。
	// ⛔ [From, To] 必须整个落在 src 的**某一段** coverage 里（v0.9 只收单段，F1 二）；
	// 否则 NewFeed 报错且 errors.Is(err, ErrWalkOutsideCoverage) —— 那一段没拉过，先同步。
	// From / To 不会被悄悄缩：它们是调用方问的那一段。
	From, To TradingDay

	// Indicators 按周期挂指标，键是周期名（Base 或 Extra 的 String()，如 "15m"、"1d"）。
	Indicators map[string][]Indicator

	// Lookback 是视图能往回看多少根，决定 View.Prev(n) 的 n 上限。各周期相同。
	Lookback int

	// WarmFrom 覆盖自动算出的预热起点（交易日）；0 表示自动。NoAutoWarmup 关掉预热、直接从 From 开始读。
	//
	// 自动预热按【每个周期】各自要的根数往前数格子（辅周期按它自己的格子数），取最早的那个起点。
	// 预热起点（自动或给定）早于 From 所在那段 coverage 的起点时，**夹到段起点，不报错**；
	// 预热不够时 Ready() 如实为假（F2）。
	WarmFrom     TradingDay
	NoAutoWarmup bool
}

// ErrFeedVoided：Feed 在步进途中发现某个源的第二遍 Walk 报错 —— 两遍之间库变了，
// **本次已经步进过的根全部作废**（BarWalker 契约「作废」）。经 Feed.Err() 报出，
// 同时 errors.Is 认得出底层那个错误（F1 一）。
var ErrFeedVoided = errors.New("tickflow: Feed 读的库在两遍 Walk 之间变了，本次步进过的根作废")

// Feed 把 src 里的根连同指标，变成一步一步走的视图。自姊妹项目 okx-tickflow-go v1.4.2 的 Feed 移植（形态照搬），
// 读法按本库的 BarWalker 契约改（F1 乙「先核后流」）：
//
//	一  NewFeed 先 Walk 一遍、回调立即返回 false —— 契约「停」：只停回调、结论照给 ⇒ 扫完全库拿到结论；
//	    结论非 nil ⇒ NewFeed 报错，一根都不交出去
//	二  Next 走第二遍 Walk（iter.Pull 把推式转成拉式）；第二遍的结论**照样检查**，非 nil ⇒ Err() 报 ErrFeedVoided
//
// ⚠️ 代价：库扫两遍（Walk 没有 seek 索引）；Close 若在第二遍中途调，Walk 仍要扫到底才返回（契约「停」）。
//
// Feed 不是并发安全的：一个回测循环就是一条时间线。
type Feed struct {
	cal      Calendar
	key      ProductKey
	rule     AggRule
	from, to TradingDay
	start    TradingDay // 实际读的起点（含预热）

	base   *series
	extras []*extra
	byName map[string]*series

	main   *pull
	daily  *pull // DailyWalker 的第二遍；没给为 nil
	curDay Day   // 主周期当前那根所属的交易日

	err    error
	closed bool
}

// pull 是一个源的第二遍 Walk（拉式）。
type pull struct {
	next   func() (Bar, bool)
	stop   func()
	walked error // seq 返回之后才有值
}

func newPull(src BarWalker, from, to TradingDay) *pull {
	p := &pull{}
	p.next, p.stop = iter.Pull(func(yield func(Bar) bool) { p.walked = src.Walk(from, to, yield) })
	return p
}

// extra 是一个辅周期：它自己的序列，加上当前交易日的格子与已到的主周期根。
type extra struct {
	s      *series
	period Period     // IntradayPeriod 或 Daily
	bounds []BarBound // 当前交易日的格子（日线 ＝ 一格：首段开盘到末段收盘）
	nb     int        // 下一个待收盘的格子
	bars   []Bar      // 当前交易日已到的主周期根
	nbar   int        // bars 里下一个还没归进格子的
	walker bool       // Daily 且从 DailyWalker 读
	valid  bool       // walker 时：最近一个已收盘交易日在 DailyWalker 里有根
	peek   *Bar       // walker 时：拉出来还没推进序列的那一根
	day    TradingDay // 当前格子所属交易日（walker 时：主周期当前那根的交易日）
	dayObj Day        // day 那一天（聚合用）
	prev   TradingDay // walker 时：day 之前的那个交易日（首日之前没有 ⇒ 0）
}

// NewFeed 构造一个 Feed。非主连模式下 src 为 nil 报错（v0.9 没有 Push，nil 源什么都做不了，F6）；
// 主连模式（cfg.Main 非 nil）下 src 必须为 nil，根由 cfg.Main.Walker() 供。
//
// ⛔ 用完必须 Close（defer f.Close()）：它释放第二遍 Walk 的协程，并交出第二遍的结论（见 Close）。
func NewFeed(src BarWalker, cfg FeedConfig) (*Feed, error) {
	// 主连模式：根只从 Main 来（评审方 2026-09-18）—— 两处各传一次、靠调用方保证是同一个 Continuous，
	// 传错时步进一套价格、RawClose 答另一套，而不报错：「成交用真实价」就押在了调用方的记性上
	if cfg.Main != nil {
		if src != nil {
			return nil, errors.New("tickflow: 主连模式（FeedConfig.Main）的根由 Main 供，src 必须传 nil —— 两处各给一个，传错了步进的价格与 RawClose 会来自两条不同的主连")
		}
		src = cfg.Main.Walker()
	}
	if src == nil {
		return nil, errors.New("tickflow: NewFeed 的 src 是 nil —— v0.9 没有 Push（v0.10），非主连模式下 nil 源什么都读不到")
	}
	if err := cfg.Rule.check(); err != nil {
		return nil, err
	}
	if cfg.Calendar == nil {
		return nil, errors.New("tickflow: FeedConfig.Calendar 是 nil —— 预热与辅周期都要按交易日的格子算")
	}
	if !cfg.From.Valid() || !cfg.To.Valid() || cfg.From > cfg.To {
		return nil, fmt.Errorf("tickflow: FeedConfig 的 [From, To] = [%s, %s] 不合法（都必填，From ≤ To）", cfg.From, cfg.To)
	}
	if cfg.Lookback < 0 {
		return nil, fmt.Errorf("tickflow: Lookback 不能为负，收到 %d", cfg.Lookback)
	}
	baseName, basePerDay, err := periodCells(cfg, cfg.Base, "主周期")
	if err != nil {
		return nil, err
	}
	names := []string{baseName}
	perDay := []func(Day) (int, error){basePerDay}
	hasDaily := false
	for _, p := range cfg.Extra {
		name, pd, err := periodCells(cfg, p, "辅周期")
		if err != nil {
			return nil, err
		}
		if err := checkExtra(cfg.Base, p); err != nil {
			return nil, err
		}
		for _, n := range names {
			if n == name {
				return nil, fmt.Errorf("tickflow: 周期 %q 出现了两次", name)
			}
		}
		if p == Period(Daily) {
			hasDaily = true
		}
		names = append(names, name)
		perDay = append(perDay, pd)
	}
	if cfg.Main != nil {
		// 辅周期不用另查：日线主周期本来就不许有辅周期（checkExtra 已拦）
		if cfg.Base != Period(Daily) {
			return nil, fmt.Errorf("tickflow: 主连模式（FeedConfig.Main）只收日线主周期（日内主连 v0.9 不做）；收到主周期 %v", cfg.Base)
		}
	}
	if cfg.DailyWalker != nil && !hasDaily {
		return nil, errors.New("tickflow: 给了 DailyWalker，而 Extra 里没有 Daily —— 它读出来没有地方放")
	}
	var all []Indicator
	for name, inds := range cfg.Indicators {
		found := false
		for _, n := range names {
			found = found || n == name
		}
		if !found {
			return nil, fmt.Errorf("tickflow: 指标挂在了周期 %q 上，而它既不是主周期也不在 Extra 里（有 %s）", name, strings.Join(names, ", "))
		}
		all = append(all, inds...)
	}
	if err := checkSharedIndicators(all); err != nil {
		return nil, err
	}

	span, ok := spanContaining(src.Coverage(), cfg.From, cfg.To)
	if !ok {
		return nil, fmt.Errorf("tickflow: [%s, %s] 不整个落在 src 的任何一段 coverage 里 —— 这段没拉过（或跨了空档），先同步: %w",
			cfg.From, cfg.To, ErrWalkOutsideCoverage)
	}

	f := &Feed{cal: cfg.Calendar, key: cfg.Key, rule: cfg.Rule, from: cfg.From, to: cfg.To, byName: map[string]*series{}}
	needs := make([]int, len(names))
	for i, name := range names {
		s, err := newSeries(name, cfg.Indicators[name], cfg.Lookback)
		if err != nil {
			return nil, err
		}
		f.byName[name] = s
		needs[i] = max(s.settle, s.warmup) + cfg.Lookback
		if i == 0 {
			f.base = s
			s.main = cfg.Main
			continue
		}
		p := cfg.Extra[i-1]
		f.extras = append(f.extras, &extra{s: s, period: p, walker: p == Period(Daily) && cfg.DailyWalker != nil})
	}
	start, err := warmStart(cfg, span, perDay, needs)
	if err != nil {
		return nil, err
	}
	f.start = start

	// 第一遍：只要结论（契约「停」）。
	if err := src.Walk(start, cfg.To, func(Bar) bool { return false }); err != nil {
		return nil, fmt.Errorf("tickflow: NewFeed 核库没过，一根都不交: %w", err)
	}
	if cfg.DailyWalker != nil {
		if _, ok := spanContaining(cfg.DailyWalker.Coverage(), start, cfg.To); !ok {
			return nil, fmt.Errorf("tickflow: [%s, %s]（含预热）不整个落在 DailyWalker 的任何一段 coverage 里 —— 日线没拉全，先同步: %w",
				start, cfg.To, ErrWalkOutsideCoverage)
		}
		if err := cfg.DailyWalker.Walk(start, cfg.To, func(Bar) bool { return false }); err != nil {
			return nil, fmt.Errorf("tickflow: NewFeed 核 DailyWalker 没过，一根都不交: %w", err)
		}
		f.daily = newPull(cfg.DailyWalker, start, cfg.To)
	}
	f.main = newPull(src, start, cfg.To)
	return f, nil
}

// periodCells 校验一个周期，返回它的名字与「某个交易日有几格」的算法。
func periodCells(cfg FeedConfig, per Period, role string) (string, func(Day) (int, error), error) {
	switch p := per.(type) {
	case IntradayPeriod:
		if p.min <= 0 {
			return "", nil, fmt.Errorf("tickflow: %s %s 不合法", role, p)
		}
		return p.String(), func(d Day) (int, error) {
			tmpl, err := cfg.Calendar.Template(cfg.Key, d.Num)
			if err != nil {
				return 0, err
			}
			bs, err := cfg.Rule.Bounds(p, tmpl, d)
			return len(bs), err
		}, nil
	case CalendarPeriod:
		if p != Daily {
			return "", nil, fmt.Errorf("tickflow: %s %s 在 v0.9 不支持（只收日内周期与 Daily）", role, p)
		}
		return p.String(), func(Day) (int, error) { return 1, nil }, nil
	}
	return "", nil, fmt.Errorf("tickflow: %s %v 不合法（nil 或未知类型）", role, per)
}

// checkExtra：辅周期必须长于主周期；日内辅周期必须是主周期的整数倍；主周期是 Daily 时不许有日内辅周期（F3）。
func checkExtra(base, p Period) error {
	bi, baseIntraday := base.(IntradayPeriod)
	switch e := p.(type) {
	case IntradayPeriod:
		if !baseIntraday {
			return fmt.Errorf("tickflow: 主周期是 %v，不许有日内辅周期 %s（日线聚合不出日内）", base, e)
		}
		if e.min <= bi.min || e.min%bi.min != 0 {
			return fmt.Errorf("tickflow: 辅周期 %s 必须长于主周期 %s、且是它的整数倍", e, bi)
		}
	case CalendarPeriod:
		if !baseIntraday {
			return fmt.Errorf("tickflow: 主周期已是 %v，辅周期 %s 不长于它", base, e)
		}
	}
	return nil
}

// spanContaining 找整个包住 [from, to] 的那一段。
func spanContaining(cov []Span, from, to TradingDay) (Span, bool) {
	for _, sp := range cov {
		if sp.From <= from && to <= sp.To {
			return sp, true
		}
	}
	return Span{}, false
}

// warmStart 算读的起点（F2）：每个周期各自从 From 往前按交易日数自己的格子，累计 ≥ 自己要的根数为止，取最早的那个；
// 早于段起点就夹到段起点。
func warmStart(cfg FeedConfig, span Span, perDay []func(Day) (int, error), needs []int) (TradingDay, error) {
	need := 0
	for _, n := range needs {
		need = max(need, n)
	}
	if cfg.NoAutoWarmup || need <= 0 {
		return cfg.From, nil
	}
	if cfg.WarmFrom != 0 {
		if cfg.WarmFrom > cfg.From {
			return 0, fmt.Errorf("tickflow: WarmFrom %s 晚于 From %s", cfg.WarmFrom, cfg.From)
		}
		return max(cfg.WarmFrom, span.From), nil
	}
	lo := span.From
	if calFrom, _, ok := cfg.Calendar.Covers(cfg.Key); !ok {
		return 0, fmt.Errorf("tickflow: 日历答不了 %s", cfg.Key)
	} else if calFrom > lo {
		lo = calFrom
	}
	if lo >= cfg.From {
		return cfg.From, nil
	}
	var days []Day
	if err := cfg.Calendar.Walk(cfg.Key, lo, cfg.From, func(d Day) bool {
		if d.Num < cfg.From {
			days = append(days, d)
		}
		return true
	}); err != nil {
		return 0, fmt.Errorf("tickflow: 预热往前数交易日失败: %w", err)
	}
	start := cfg.From
	for k, pd := range perDay {
		if needs[k] <= 0 {
			continue
		}
		got, reached := 0, lo // 不够时这个周期夹到 lo：Ready() 会如实为假
		for i := len(days) - 1; i >= 0; i-- {
			n, err := pd(days[i])
			if err != nil {
				return 0, fmt.Errorf("tickflow: 预热数 %s 的格子失败: %w", days[i].Num, err)
			}
			got += n
			if got >= needs[k] {
				reached = days[i].Num
				break
			}
		}
		start = min(start, reached)
	}
	return start, nil
}

// Next 前进一根主周期。返回 false 表示走完或出错，用 Err 区分。预热段只喂指标，不产出步进。
//
// 每前进一根，辅周期被推进到【本根收盘时刻为止已收盘】的最后一根（F4）。
func (f *Feed) Next() bool {
	if f.err != nil || f.closed {
		return false
	}
	for {
		b, ok := f.main.next()
		if !ok {
			f.settle()
			return false
		}
		if err := f.step(b); err != nil {
			f.err = err
			f.Close()
			return false
		}
		if b.TradingDay >= f.from {
			return true
		}
	}
}

// settle 停掉每个源的第二遍、并看它们的结论（主周期读完时、Close 时都走这里）：
// ⛔ 第一遍是 nil 不等于第二遍也是（F1 一）；iter.Pull 的 stop 会等 seq 返回，之后 walked 才在手。
func (f *Feed) settle() {
	for _, p := range []*pull{f.main, f.daily} {
		if p != nil {
			p.stop()
		}
	}
	for _, p := range []*pull{f.main, f.daily} {
		if p != nil && p.walked != nil && f.err == nil {
			f.err = fmt.Errorf("%w: %w", ErrFeedVoided, p.walked)
		}
	}
}

// step 处理一根主周期：先推主周期，再把每个辅周期推进到「收盘时刻 ≤ 这根 TsEnd」的格子为止。
func (f *Feed) step(b Bar) error {
	// 只有辅周期要当天的时段；只有主周期时不问日历（日历覆盖不到的老数据照样能走，F-a 的行为不变）
	if len(f.extras) > 0 && b.TradingDay != f.curDay.Num {
		d, err := f.cal.DayOf(f.key, b.TradingDay)
		if err != nil {
			return fmt.Errorf("tickflow: Feed 问不到 %s 那一天的时段: %w", b.TradingDay, err)
		}
		f.curDay = d
	}
	if err := f.base.push(b); err != nil {
		return err
	}
	for _, e := range f.extras {
		var err error
		if e.walker {
			err = f.stepDailyWalker(e, b)
		} else {
			err = f.stepAgg(e, b)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// stepAgg：由主周期聚合的辅周期。
func (f *Feed) stepAgg(e *extra, b Bar) error {
	if e.day != b.TradingDay {
		// 换了交易日：前一天剩下的格子收盘时刻都早于这根 ⇒ 全部收盘
		if err := f.emit(e, math.MaxInt64); err != nil {
			return err
		}
		d := f.curDay
		switch p := e.period.(type) {
		case IntradayPeriod:
			tmpl, err := f.cal.Template(f.key, d.Num)
			if err != nil {
				return fmt.Errorf("tickflow: Feed 问不到 %s 的时段模板: %w", d.Num, err)
			}
			if e.bounds, err = f.rule.Bounds(p, tmpl, d); err != nil {
				return err
			}
		default: // Daily：一格，首段开盘到末段收盘
			if len(d.Sessions) == 0 {
				return fmt.Errorf("tickflow: %s 那一天日历里没有时段", d.Num)
			}
			e.bounds = []BarBound{{Open: d.Sessions[0].Start, Close: d.Sessions[len(d.Sessions)-1].End, Full: true}}
		}
		e.day, e.dayObj, e.nb, e.bars, e.nbar = d.Num, d, 0, e.bars[:0], 0
	}
	// 这根必须整个落在当天的某一格里（低周期与这套格子不相容 ⇒ 报错，不猜）
	in := false
	for _, bd := range e.bounds[e.nb:] {
		if b.Ts >= bd.Open && b.TsEnd <= bd.Close {
			in = true
			break
		}
	}
	if !in {
		return fmt.Errorf("tickflow: 主周期那根 [%d, %d) 不整个落在辅周期 %s 的任何一格里（周期与聚合规则不相容）", b.Ts, b.TsEnd, e.s.name)
	}
	e.bars = append(e.bars, b)
	return f.emit(e, b.TsEnd)
}

// emit 把当天收盘时刻 ≤ upTo 的格子依次收盘（没有根的格子不出根：规则一）。
func (f *Feed) emit(e *extra, upTo int64) error {
	for e.nb < len(e.bounds) && e.bounds[e.nb].Close <= upTo {
		bd := e.bounds[e.nb]
		i := e.nbar
		for e.nbar < len(e.bars) && e.bars[e.nbar].TsEnd <= bd.Close {
			e.nbar++
		}
		if agg, ok := aggCell(bd, e.dayObj, e.bars[i:e.nbar]); ok {
			if err := e.s.push(agg); err != nil {
				return err
			}
		}
		e.nb++
	}
	return nil
}

// stepDailyWalker：Daily 辅周期从 DailyWalker 读。已收盘的最后一个交易日：
// 这根主周期所属的交易日若已收盘（这根 TsEnd ≥ 当天末段收盘）⇒ 就是它；否则是它之前的那个交易日（F4 / §十·二）。
func (f *Feed) stepDailyWalker(e *extra, b Bar) error {
	if b.TradingDay != e.day {
		e.prev, e.day = e.day, b.TradingDay
	}
	closed := e.prev
	if ss := f.curDay.Sessions; len(ss) > 0 && b.TsEnd >= ss[len(ss)-1].End {
		closed = e.day
	}
	if closed == 0 {
		return nil
	}
	for {
		if e.peek == nil {
			x, ok := f.daily.next()
			if !ok {
				break
			}
			e.peek = &x
		}
		if e.peek.TradingDay > closed {
			break
		}
		if err := e.s.push(*e.peek); err != nil {
			return err
		}
		e.peek = nil
	}
	// 最近一个已收盘交易日它有没有根：序列末根的交易日 ＝ closed
	e.valid = e.s.n > 0 && e.s.bars[int((e.s.n-1)%int64(e.s.capN))].TradingDay == closed
	return nil
}

// View 返回主周期当前这一根的视图。
func (f *Feed) View() View { return f.base.view() }

// TF 返回某个周期的视图：主周期给当前这根，辅周期给【最后一根已收盘】的（F4）。
// 周期不属于本 Feed ⇒ 无效视图（取值 NaN）。Daily 从 DailyWalker 读、而最近一个已收盘交易日它没有根 ⇒ 也是无效视图（F3 b）。
func (f *Feed) TF(period string) View {
	s, ok := f.byName[period]
	if !ok {
		return View{}
	}
	for _, e := range f.extras {
		if e.s == s && e.walker && !e.valid {
			return View{}
		}
	}
	return s.view()
}

// Handle 预解析一个键名，供热路径使用。
func (f *Feed) Handle(period, key string) (Handle, error) {
	s, ok := f.byName[period]
	if !ok {
		return Handle{}, fmt.Errorf("tickflow: 周期 %q 不属于本 Feed（有 %s）", period, strings.Join(f.Periods(), ", "))
	}
	col, ok := s.keyIdx[key]
	if !ok {
		return Handle{}, fmt.Errorf("tickflow: 周期 %s 上没有指标 %q（有 %s）", period, key, strings.Join(s.keys, ", "))
	}
	return Handle{s: s, col: col}, nil
}

// Periods 返回本 Feed 的全部周期名，主周期在最前。
func (f *Feed) Periods() []string {
	out := []string{f.base.name}
	for _, e := range f.extras {
		out = append(out, e.s.name)
	}
	return out
}

// Keys 返回某周期上全部指标的键名。
func (f *Feed) Keys(period string) []string {
	s, ok := f.byName[period]
	if !ok {
		return nil
	}
	return append([]string(nil), s.keys...)
}

// Ready 报告全部周期的指标是否都已预读够 Settle() 根（契约 C：≤ 2e-15，不是逐位相等）。
func (f *Feed) Ready() bool {
	for _, s := range f.byName {
		if !s.ready() {
			return false
		}
	}
	return true
}

// Err 返回步进中发生的错误；正常走完为 nil。
func (f *Feed) Err() error { return f.err }

// Close 释放第二遍 Walk，并**交出它的结论**；返回值与之后的 Err() 一致。
//
// ⛔ 用完必须 Close（defer f.Close()）：不 Close 就丢掉的 Feed，iter.Pull 背后那个协程会一直挂着。
// ⛔ 中途 break 的用户同样用过那些根 ⇒ stop 之后（iter.Pull 的 stop 会等 seq 返回，第二遍的结论此时在手）
// 结论非 nil 就返回 ErrFeedVoided，并写进 Err()（评审方 2026-09-18：不许因为没走到底就拿不到「作废」）。
// ⚠️ 中途调用时 Walk 仍要扫到底才返回（契约「停」）。
func (f *Feed) Close() error {
	if f.closed {
		return f.err
	}
	f.closed = true
	f.settle()
	return f.err
}

// ── 单周期序列 ──

// series 是一个周期上的环形缓冲：根一份，指标各占一列（照搬姊妹仓 tfSeries）。
type series struct {
	main   MainSource // 主连模式时主周期那条序列带着它；其余为 nil
	name   string
	inds   []Indicator
	widths []int
	keys   []string
	keyIdx map[string]int
	warmup int // 值从此有定义
	settle int // 值从此与从哪根开始喂基本无关（契约 C）

	capN int
	bars []Bar
	cols [][]float64
	n    int64
}

func newSeries(name string, inds []Indicator, lookback int) (*series, error) {
	s := &series{name: name, inds: inds, keyIdx: map[string]int{}, capN: lookback + 1}
	for _, ind := range inds {
		if ind == nil {
			return nil, fmt.Errorf("tickflow: 周期 %s 上挂了一个 nil 指标", name)
		}
		ks := IndicatorKeys(ind)
		for _, k := range ks {
			if _, dup := s.keyIdx[k]; dup {
				return nil, fmt.Errorf("tickflow: 周期 %s 上有两个指标都叫 %q；用 indicator.Named(...) 给其中一个改名", name, k)
			}
			s.keyIdx[k] = len(s.keys)
			s.keys = append(s.keys, k)
		}
		s.widths = append(s.widths, len(ks))
		s.warmup = max(s.warmup, ind.Warmup())
		s.settle = max(s.settle, IndicatorSettle(ind))
		ind.Reset()
	}
	s.bars = make([]Bar, s.capN)
	s.cols = make([][]float64, len(s.keys))
	for i := range s.cols {
		s.cols[i] = make([]float64, s.capN)
		for j := range s.cols[i] {
			s.cols[i][j] = math.NaN()
		}
	}
	return s, nil
}

func (s *series) push(b Bar) error {
	slot := int(s.n % int64(s.capN))
	s.bars[slot] = b
	off := 0
	for i, ind := range s.inds {
		v := ind.Update(b)
		if len(v) != s.widths[i] {
			return fmt.Errorf("tickflow: 指标 %s 的 Update 返回了 %d 个值，而它声明的字段数是 %d —— 两者必须一致", ind.Name(), len(v), s.widths[i])
		}
		for k, x := range v {
			s.cols[off+k][slot] = x
		}
		off += s.widths[i]
	}
	s.n++
	return nil
}

func (s *series) ready() bool   { return s.n >= int64(s.settle) }
func (s *series) defined() bool { return s.n >= int64(s.warmup) }
func (s *series) view() View    { return View{s: s, abs: s.n - 1} }

// checkSharedIndicators 挡住同一个指标实例挂在多处（指标有状态，共用会互相污染而不报错）。按指针身份判。
func checkSharedIndicators(inds []Indicator) error {
	seen := map[any]bool{}
	for _, ind := range inds {
		if ind == nil || reflect.ValueOf(ind).Kind() != reflect.Pointer {
			continue
		}
		if seen[ind] {
			return fmt.Errorf("tickflow: 指标 %q 的同一个实例挂了两次；指标是有状态的，共用一个实例会让两边的值互相污染", ind.Name())
		}
		seen[ind] = true
	}
	return nil
}

// ── 视图 ──

// View 是某个周期上某一根的视图：一个指针加一个下标，值类型，取值没有分配。
// 无效视图（超出 Lookback、还没走到、周期不属于本 Feed）取值一律 NaN（价格）或零值（时刻），不 panic。
type View struct {
	s   *series
	abs int64
}

// Valid 报告本视图是否指向一根真实存在、且还在回看窗口里的根。
func (v View) Valid() bool {
	return v.s != nil && v.abs >= 0 && v.abs < v.s.n && v.abs >= v.s.n-int64(v.s.capN)
}

// Period 返回本视图所属的周期名。
func (v View) Period() string {
	if v.s == nil {
		return ""
	}
	return v.s.name
}

// Prev 往回看 n 根；n 超过 Lookback 得到无效视图。
func (v View) Prev(n int) View { return View{s: v.s, abs: v.abs - int64(n)} }

// Ready 报告本周期的指标是否已预读够 Settle() 根 —— 「已收敛」在本库的含义是
// 与从头喂到底只差几个 ULP（契约 C，≤ 2e-15），不是逐位相等（design.md §九 补注）。
//
// ⚠️ 它读的是序列【当前】的状态，不是取这个视图那一步的：把视图存下来、走了几步再调，答案会变（评审方 2026-09-18 提）。
func (v View) Ready() bool { return v.s != nil && v.s.ready() }

// Defined 报告指标值已有定义（不是 NaN），但不保证已收敛。比 Ready 弱。
func (v View) Defined() bool { return v.s != nil && v.s.defined() }

// Bar 返回这一根；视图无效时返回零值。
func (v View) Bar() Bar {
	if !v.Valid() {
		return Bar{}
	}
	return v.s.bars[int(v.abs%int64(v.s.capN))]
}

// Ts / TsEnd / TradingDay：视图无效时为零值。
func (v View) Ts() int64              { return v.Bar().Ts }
func (v View) TsEnd() int64           { return v.Bar().TsEnd }
func (v View) TradingDay() TradingDay { return v.Bar().TradingDay }

// Open / High / Low / Close / Volume / OpenInterest：视图无效时为 NaN（0 是个看起来正常的价格）。
func (v View) Open() float64         { return v.px(func(b Bar) float64 { return b.Open }) }
func (v View) High() float64         { return v.px(func(b Bar) float64 { return b.High }) }
func (v View) Low() float64          { return v.px(func(b Bar) float64 { return b.Low }) }
func (v View) Close() float64        { return v.px(func(b Bar) float64 { return b.Close }) }
func (v View) Volume() float64       { return v.px(func(b Bar) float64 { return b.Volume }) }
func (v View) OpenInterest() float64 { return v.px(func(b Bar) float64 { return b.OpenInterest }) }

func (v View) px(get func(Bar) float64) float64 {
	if !v.Valid() {
		return math.NaN()
	}
	return get(v.s.bars[int(v.abs%int64(v.s.capN))])
}

// Ind 按键名取指标值；键名未知或视图无效时 NaN。热路径用 Handle ＋ At。
func (v View) Ind(key string) float64 {
	if !v.Valid() {
		return math.NaN()
	}
	col, ok := v.s.keyIdx[key]
	if !ok {
		return math.NaN()
	}
	return v.s.cols[col][int(v.abs%int64(v.s.capN))]
}

// At 按预解析的 Handle 取值；Handle 来自另一个周期时 NaN。
func (v View) At(h Handle) float64 {
	if !v.Valid() || h.s != v.s {
		return math.NaN()
	}
	return v.s.cols[h.col][int(v.abs%int64(v.s.capN))]
}

// ── 主连模式（F5）──

// MainDay 是主连在某个交易日的那几个事实（View 的四个主连方法读它）。
type MainDay struct {
	Contract Symbol  // 那一根属于哪个合约 —— 下单用它
	RawClose float64 // 未复权收盘价 —— 成交用它（复权价只拿来算信号，§八）
	RollDay  bool    // 这一天是不是换月日
	Basis    float64 // 换月日两合约的价差；不是换月日为 NaN
}

// MainSource 回答「主连在交易日 d 的那几个事实」。continuous.Continuous 实现它。
//
// ⚠️ 为什么是根包里的一个接口，而不是直接收 continuous.Continuous：continuous 要用根包的 Bar，
// 根包再 import continuous 就成环了 —— 与 Indicator 放在根包是同一个理由。
type MainSource interface {
	MainAt(d TradingDay) (MainDay, bool)
	// Walker 交出拼好的（复权后的）序列 —— 主连模式下 Feed 的根**只从这里来**（NewFeed 的 src 必须为 nil），
	// 于是步进的价格与四方法答的事实出自同一条主连，由库保证，不靠调用方
	Walker() BarWalker
}

func (v View) mainDay() (MainDay, bool) {
	if !v.Valid() || v.s.main == nil {
		return MainDay{}, false
	}
	return v.s.main.MainAt(v.TradingDay())
}

// RawClose 返回这一根的**未复权**收盘价（成交用它）；不是主连模式、视图无效或主连里没有这一天 ⇒ NaN。
func (v View) RawClose() float64 {
	if m, ok := v.mainDay(); ok {
		return m.RawClose
	}
	return math.NaN()
}

// Contract 返回这一根属于哪个合约；不是主连模式、视图无效或主连里没有这一天 ⇒ (零值, false)。
func (v View) Contract() (Symbol, bool) {
	m, ok := v.mainDay()
	return m.Contract, ok
}

// IsRollDay 报告这一天是不是换月日（那一天的收益有一部分来自换月、不是行情；换月本身要付两笔成本）。
func (v View) IsRollDay() bool {
	m, ok := v.mainDay()
	return ok && m.RollDay
}

// Basis 返回换月日两合约的价差；不是换月日、不是主连模式或视图无效 ⇒ NaN（不给 0：0 是一个看起来正常的基差）。
func (v View) Basis() float64 {
	if m, ok := v.mainDay(); ok {
		return m.Basis
	}
	return math.NaN()
}

// Handle 是预解析过的指标键名，绑定在某个周期上。
type Handle struct {
	s   *series
	col int
}
