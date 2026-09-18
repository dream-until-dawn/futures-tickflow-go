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
// v0.9 F-a 只有主周期；辅周期（TF）在 F-b，主连四方法在 F-c。
type FeedConfig struct {
	// Key 与 Calendar 用来回答「某个交易日有几根主周期」（预热往前数格子，F2）。
	Key      ProductKey
	Calendar Calendar

	// Base 是步进的主周期：日内周期（IntradayPeriod），或 Daily。src 里存的就是这个周期的根。
	Base Period

	// Rule 是聚合口径。⛔ **必填，零值 ⇒ ErrAggRuleUnset** —— 不论有没有日内辅周期
	// （用户 2026-09-18 裁：照「构造 Feed 不给就报错」的字面，不放宽）。
	Rule AggRule

	// From / To 是步进的交易日闭区间，都必填。
	// ⛔ [From, To] 必须整个落在 src 的**某一段** coverage 里（v0.9 只收单段，F1 二）；
	// 否则 NewFeed 报错且 errors.Is(err, ErrWalkOutsideCoverage) —— 那一段没拉过，先同步。
	// From / To 不会被悄悄缩：它们是调用方问的那一段。
	From, To TradingDay

	// Indicators 按周期挂指标，键是周期名（Base.String()，如 "15m"、"1d"）。
	// v0.9 F-a 只认 Base 那一个键。
	Indicators map[string][]Indicator

	// Lookback 是视图能往回看多少根，决定 View.Prev(n) 的 n 上限。
	Lookback int

	// WarmFrom 覆盖自动算出的预热起点（交易日）；0 表示自动。NoAutoWarmup 关掉预热、直接从 From 开始读。
	//
	// 预热起点（自动或给定）早于 From 所在那段 coverage 的起点时，**夹到段起点，不报错**；
	// 预热不够时 Ready() 如实为假（F2）。
	WarmFrom     TradingDay
	NoAutoWarmup bool
}

// ErrFeedVoided：Feed 在步进途中发现 src 的第二遍 Walk 报错 —— 两遍之间库变了，
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
	src      BarWalker
	from, to TradingDay
	start    TradingDay // 实际读的起点（含预热）

	base *series

	next   func() (Bar, bool)
	stop   func()
	walked error // 第二遍 Walk 的结论；seq 返回之后才有值

	err    error
	closed bool
}

// NewFeed 构造一个 Feed。src 为 nil 报错（v0.9 没有 Push，nil 源什么都做不了，F6）。
//
// ⛔ 用完必须 Close（defer f.Close()）：它释放第二遍 Walk 的协程，并交出第二遍的结论（见 Close）。
func NewFeed(src BarWalker, cfg FeedConfig) (*Feed, error) {
	if src == nil {
		return nil, errors.New("tickflow: NewFeed 的 src 是 nil —— v0.9 没有 Push（v0.10），nil 源什么都读不到")
	}
	if err := cfg.Rule.check(); err != nil {
		return nil, err
	}
	if cfg.Calendar == nil {
		return nil, errors.New("tickflow: FeedConfig.Calendar 是 nil —— 预热要按交易日数格子")
	}
	if !cfg.From.Valid() || !cfg.To.Valid() || cfg.From > cfg.To {
		return nil, fmt.Errorf("tickflow: FeedConfig 的 [From, To] = [%s, %s] 不合法（都必填，From ≤ To）", cfg.From, cfg.To)
	}
	if cfg.Lookback < 0 {
		return nil, fmt.Errorf("tickflow: Lookback 不能为负，收到 %d", cfg.Lookback)
	}
	baseName, perDay, err := periodCells(cfg)
	if err != nil {
		return nil, err
	}
	for name := range cfg.Indicators {
		if name != baseName {
			return nil, fmt.Errorf("tickflow: 指标挂在了周期 %q 上，而本 Feed 只有主周期 %q（辅周期在 v0.9 F-b）", name, baseName)
		}
	}
	if err := checkSharedIndicators(cfg.Indicators[baseName]); err != nil {
		return nil, err
	}

	span, ok := spanContaining(src.Coverage(), cfg.From, cfg.To)
	if !ok {
		return nil, fmt.Errorf("tickflow: [%s, %s] 不整个落在 src 的任何一段 coverage 里 —— 这段没拉过（或跨了空档），先同步: %w",
			cfg.From, cfg.To, ErrWalkOutsideCoverage)
	}

	base, err := newSeries(baseName, cfg.Indicators[baseName], cfg.Lookback)
	if err != nil {
		return nil, err
	}
	start, err := warmStart(cfg, span, perDay, max(base.settle, base.warmup)+cfg.Lookback)
	if err != nil {
		return nil, err
	}

	// 第一遍：只要结论（契约「停」）。
	if err := src.Walk(start, cfg.To, func(Bar) bool { return false }); err != nil {
		return nil, fmt.Errorf("tickflow: NewFeed 核库没过，一根都不交: %w", err)
	}

	f := &Feed{src: src, from: cfg.From, to: cfg.To, start: start, base: base}
	seq := func(yield func(Bar) bool) {
		f.walked = src.Walk(start, cfg.To, yield)
	}
	f.next, f.stop = iter.Pull(seq)
	return f, nil
}

// periodCells 校验主周期，返回它的名字与「某个交易日有几根」的算法。
func periodCells(cfg FeedConfig) (string, func(Day) (int, error), error) {
	switch p := cfg.Base.(type) {
	case IntradayPeriod:
		if p.min <= 0 {
			return "", nil, fmt.Errorf("tickflow: 主周期 %s 不合法", p)
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
			return "", nil, fmt.Errorf("tickflow: 主周期 %s 在 v0.9 不支持（只收日内周期与 Daily）", p)
		}
		return p.String(), func(Day) (int, error) { return 1, nil }, nil
	}
	return "", nil, fmt.Errorf("tickflow: 主周期 %v 不合法（nil 或未知类型）", cfg.Base)
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

// warmStart 算读的起点（F2）：从 From 往前按交易日数格子，累计 ≥ need 为止；早于段起点就夹到段起点。
func warmStart(cfg FeedConfig, span Span, perDay func(Day) (int, error), need int) (TradingDay, error) {
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
	got := 0
	for i := len(days) - 1; i >= 0; i-- {
		n, err := perDay(days[i])
		if err != nil {
			return 0, fmt.Errorf("tickflow: 预热数 %s 的格子失败: %w", days[i].Num, err)
		}
		got += n
		if got >= need {
			return days[i].Num, nil
		}
	}
	return lo, nil // 不够也不报错：Ready() 会如实为假
}

// Next 前进一根主周期。返回 false 表示走完或出错，用 Err 区分。预热段只喂指标，不产出步进。
func (f *Feed) Next() bool {
	if f.err != nil || f.closed {
		return false
	}
	for {
		b, ok := f.next()
		if !ok {
			// seq 已经返回 ⇒ 第二遍的结论在手。⛔ 第一遍是 nil 不等于第二遍也是（F1 一）
			if f.walked != nil {
				f.err = fmt.Errorf("%w: %w", ErrFeedVoided, f.walked)
			}
			return false
		}
		if err := f.base.push(b); err != nil {
			f.err = err
			f.stop()
			return false
		}
		if b.TradingDay >= f.from {
			return true
		}
	}
}

// View 返回主周期当前这一根的视图。
func (f *Feed) View() View { return f.base.view() }

// Handle 预解析一个键名，供热路径使用。v0.9 F-a 只有主周期。
func (f *Feed) Handle(period, key string) (Handle, error) {
	if period != f.base.name {
		return Handle{}, fmt.Errorf("tickflow: 周期 %q 不属于本 Feed（有 %s）", period, f.base.name)
	}
	col, ok := f.base.keyIdx[key]
	if !ok {
		return Handle{}, fmt.Errorf("tickflow: 周期 %s 上没有指标 %q（有 %s）", period, key, strings.Join(f.base.keys, ", "))
	}
	return Handle{s: f.base, col: col}, nil
}

// Keys 返回某周期上全部指标的键名。
func (f *Feed) Keys(period string) []string {
	if period != f.base.name {
		return nil
	}
	return append([]string(nil), f.base.keys...)
}

// Ready 报告全部周期的指标是否都已预读够 Settle() 根（契约 C：≤ 2e-15，不是逐位相等）。
func (f *Feed) Ready() bool { return f.base.ready() }

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
	f.stop()
	if f.walked != nil && f.err == nil {
		f.err = fmt.Errorf("%w: %w", ErrFeedVoided, f.walked)
	}
	return f.err
}

// ── 单周期序列 ──

// series 是一个周期上的环形缓冲：根一份，指标各占一列（照搬姊妹仓 tfSeries）。
type series struct {
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

// Handle 是预解析过的指标键名，绑定在某个周期上。
type Handle struct {
	s   *series
	col int
}
