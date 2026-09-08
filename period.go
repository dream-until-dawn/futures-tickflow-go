package tickflow

import (
	"fmt"
	"time"
)

// BarBound 是一根 K 线的墙钟边界，左闭右开 [Open, Close)。
//
// 【Close - Open 通常不等于周期长度】——中间可能跨了休市段，
// 甚至跨了隔夜缺口。这不是异常，是中国期货 K 线的常态：
// 沪银 60m 的某一根 = 前一日 02:00–02:30 + 当日 09:00–09:30。
type BarBound struct {
	Open  int64
	Close int64
	// Full 表示这是一个【完整的网格格子】，而不是交易日末尾的冲刷残段。
	//
	// 注意它【不】等于「装了整整一个周期的交易时间」：停夜盘那天沪银的
	// 第一根日盘 K 线是完整格子（Full=true），却只装了 30 分钟——
	// 另外 30 分钟来自一个没有开的夜盘。要问装了多少，用 Minutes。
	//
	// 反过来【绝不成立】：任何一根都不会装【超过】一个周期。见 Anomalous。
	Full bool

	// Anomalous 表示【这个交易日】的标称模板与实际时段互相矛盾，
	// 也就是交易所改了夜盘而模板没跟上（contract.md 的「时段表过期」那条风险）。
	//
	// 它是【交易日一级】的事实，所以那天的**每一根**都会带上，不是只标某一根。
	// 判据见 Day.TemplateMismatch——比的是分钟数，**不是取模后的余数**：
	// 按余数比会让同一个矛盾在 15m/30m 报、在 5m/60m/90m 不报，
	// 标志骑在周期上，上层按周期扫就按周期漏（评审 K1）。
	//
	// 与它相关但【不是同一件事】的是超装：矛盾发生时，夜盘残段可能多于
	// 相位所能容纳的，硬塞进第一根日盘会造出一根装了 1.5 个周期的「完整」格子，
	// 而收盘标签序列与覆盖检查都看不出来。所以那段单独冲刷成一根短的。
	//
	// 上层（v0.3 的 SyncReport.Misaligned）扫这一位就能报「模板过期」。
	// **不要静默跳过它**：跳过等于把丢数据换成一个更安静的丢数据。
	Anomalous bool
}

// Minutes 返回这根 K 线实际装了多少分钟【交易时间】（不含中间的休市段）。
func (b BarBound) Minutes(d Day) int {
	n := 0
	for _, s := range d.Sessions {
		lo, hi := max64(s.Start, b.Open), min64(s.End, b.Close)
		if hi > lo {
			n += int((hi - lo) / 60000)
		}
	}
	return n
}

// IntradayPeriod 是日内周期：1m / 5m / 15m / 30m / 60m …
//
// 它在【一个交易日之内】把交易时间等分。与日线及以上分开成两个类型，
// 是因为后者按交易日【成组】——周线跨 5 个交易日，
// 给它一个 Day 无从下手，硬塞进同一个方法里，注释承诺的东西签名实现不了。
type IntradayPeriod struct {
	min int // 分钟数
}

// Minutes 返回周期的分钟数。
func (p IntradayPeriod) Minutes() int { return p.min }

func (p IntradayPeriod) String() string { return fmt.Sprintf("%dm", p.min) }

// Intraday 构造一个日内周期。分钟数必须为正。
func Intraday(minutes int) (IntradayPeriod, error) {
	if minutes <= 0 {
		return IntradayPeriod{}, fmt.Errorf("tickflow: 日内周期必须为正，收到 %d", minutes)
	}
	return IntradayPeriod{min: minutes}, nil
}

// MustIntraday 同 Intraday，参数非法时 panic。只给常量用。
func MustIntraday(minutes int) IntradayPeriod {
	p, err := Intraday(minutes)
	if err != nil {
		panic(err)
	}
	return p
}

// Phase 返回该品种在该周期上的【相位】——日盘开盘时，网格上已经「欠」了多少。
//
// 它等于「标称夜盘长度 mod 周期」，是【品种的固定属性】，
// **不依赖某一天是否真的开了夜盘**。
//
// 这一条是被实测证伪出来的。第一版模型是「沿当天实际时段累计」，
// 它有个可证伪的预言：长假前停夜盘，那天沪银没有 30 分钟余数，
// 日盘就该从 09:00 干净重开、网格退化成沪铜那一族。
// 实测两个停夜盘日（2026-05-06 五一后、2026-06-22 端午后）× 三个品种：
// **日盘网格与普通日完全相同**。预言错了。
//
// 所以相位看【标称模板】，切分看【当天实际时段】——两层不能合并。
// 照第一版写，会在每个长假前后错一整天，且只错 au/ag/sc 这类夜盘不整除的品种，
// 不报错、不崩溃，只是那几天的每一根 K 线都错开 30 分钟。
//
// 置信度：「与当天实际交易无关」是实测；「= 标称夜盘长度 mod 周期」这个公式
// 是推定——只在 330 分与 240 分两种夜盘长度上验过。见 docs/probe.md 坑三之三。
func (p IntradayPeriod) Phase(tmpl SessionTemplate) time.Duration {
	if p.min <= 0 {
		return 0
	}
	return time.Duration(tmpl.NightMinutes()%p.min) * time.Minute
}

// Bars 给出某个交易日内该周期的全部 K 线边界。
//
// 这是本库最核心的一个函数：判完结、聚合、对齐、补洞全走它。
//
// 规则分两半：
//
//  1. 相位按 tmpl（标称）算，是品种常量；
//  2. 切分沿 d.Sessions（当天实际）累计交易时间，从相位起算，
//     交易日结束时强制冲刷（最后一根可以是短的）。
//
// tmpl 少不了：停夜盘那天 d.Sessions 里根本没有夜盘段，相位就无从算起。
func (p IntradayPeriod) Bars(tmpl SessionTemplate, d Day) []BarBound {
	if p.min <= 0 || len(d.Sessions) == 0 {
		return nil
	}
	night, day := splitNightDay(d.Sessions)

	// 夜盘块：从 0 起算，【中间不冲刷】——它的余数要带进日盘块。
	out, nightCarried, nightOpen := p.grid(night, 0, 0, nil)

	// 日盘块的起始 carried 是【相位】，按标称模板算。
	//
	// 这里不是「把夜盘的余数接着用」，而是【显式设成相位】——两者在夜盘
	// 足长时数值相同，但停夜盘那天只有后者是对的：那天 night 为空、
	// 自然累计给 0，而实测日盘网格仍是带 30 分钟余数的那一套。
	// 沿实际累计的写法会在每个长假前后错一整天。
	phase := int64(p.Phase(tmpl) / time.Millisecond)

	// 【开盘时刻】则要沿用夜盘残段——相位管「网格切在哪」，
	// 残段的 Open 管「那段时间归谁」。两者分开：
	// 沪银的 02:00–02:30 必须落在某根 K 线的 [Open, Close) 里，
	// 否则那半小时不属于任何一根，聚合时被静默丢掉——
	// 收盘标签序列完全正常，只是少了 30 分钟的成交。
	//
	// 但「沿用」有个上界：残段只有【不超过相位】时才塞得进第一根日盘。
	// 第一根日盘装的是 nightCarried + (周期 − 相位)，
	// 所以 nightCarried > phase 时它会【超过一个周期】——
	// 一根装了 1.5 个周期的「完整」格子，而标签序列与覆盖检查都看不出来。
	//
	// nightCarried > phase 意味着当日实际夜盘比标称长，即模板过期。
	// 这一侧【未验】：实测只覆盖「实际 ≤ 标称」（停夜盘日，实际为 0）。
	// 未验就不替它选一套口径——把残段单独冲刷成一根短的并标 Anomalous，
	// 让矛盾显式冒到上层，而不是安静地摊进网格。
	dayOpen := int64(0)
	switch {
	case nightCarried == 0:
		// 夜盘正好切齐，或当天根本没有夜盘：日盘块从日盘首段开盘
	case nightCarried <= phase:
		dayOpen = nightOpen
	default:
		// 这一根不在这里打 Anomalous——日级那一遍会把【整天】都标上。
		// 只标这一根的话，标志就骑在了周期上：同一个矛盾在 15m 报、60m 不报。
		out = append(out, BarBound{
			Open:  nightOpen,
			Close: night[len(night)-1].End,
			Full:  false,
		})
	}
	dayBars, carried, open := p.grid(day, phase, dayOpen, nil)
	out = append(out, dayBars...)

	// 交易日结束，冲刷残段（最后一根可以是短的）
	if carried > 0 && len(day) > 0 {
		out = append(out, BarBound{Open: open, Close: day[len(day)-1].End, Full: false})
	}

	// 「模板与实际矛盾」是【交易日一级】的事实，与周期无关——所以整天都标上。
	//
	// 原先它由 nightCarried > phase 决定，那是两个【取模后的余数】相比，
	// 于是同一个事实在 15m/30m 报、在 5m/60m/90m 不报（评审 K1）。
	// 上层按周期扫这一位就会按周期漏报，而漏掉的那些看起来完全正常。
	if _, _, bad := d.TemplateMismatch(tmpl); bad {
		for i := range out {
			out[i].Anomalous = true
		}
	}
	return out
}

// grid 沿 sessions 累计交易时间切分，返回切出的完整根、剩余 carried、
// 以及下一根的开盘时刻。【不冲刷残段】——是否冲刷由调用方决定。
//
// open 是第一根的开盘时刻；传 0 表示取 sessions[0].Start。
// 日盘块必须显式传【夜盘残段的开盘时刻】，否则那段时间会掉在所有 K 线之外。
func (p IntradayPeriod) grid(sessions []Session, carried, open int64, out []BarBound) ([]BarBound, int64, int64) {
	step := int64(p.min) * 60000
	if len(sessions) == 0 {
		return out, carried, 0
	}
	if open == 0 {
		open = sessions[0].Start
	}
	fresh := false // 上一根正好在时段末尾收口，下一根要落到下一段起点
	for _, s := range sessions {
		if fresh {
			open, fresh = s.Start, false
		}
		cur := s.Start
		for cur < s.End {
			need := step - carried
			if left := s.End - cur; left < need {
				carried += left
				break
			}
			cur += need
			out = append(out, BarBound{Open: open, Close: cur, Full: true})
			carried, open = 0, cur
			if cur >= s.End {
				fresh = true
			}
		}
	}
	return out, carried, open
}

// splitNightDay 把一个交易日的时段拆成夜盘块与日盘块。
//
// 判据是开盘时刻：>= 20:00 或 < 04:00 算夜盘。中国期货的夜盘最晚到次日 02:30，
// 最早 21:00 开，这条界限没有歧义。
func splitNightDay(ss []Session) (night, day []Session) {
	for _, s := range ss {
		h := time.UnixMilli(s.Start).In(CST).Hour()
		if h >= 20 || h < 4 {
			night = append(night, s)
		} else {
			day = append(day, s)
		}
	}
	return night, day
}

// CST 是中国期货市场的时区（东八区）。
//
// 固定偏移而非 Asia/Shanghai：中国大陆自 1991 年起不再有夏令时，
// 而依赖 tzdata 会让结果取决于运行环境是否装了时区库。
var CST = time.FixedZone("CST", 8*3600)

// CalendarPeriod 是日线及以上：按【交易日】成组。
type CalendarPeriod int

const (
	Daily CalendarPeriod = iota
	Weekly
	Monthly
)

func (p CalendarPeriod) String() string {
	switch p {
	case Daily:
		return "1d"
	case Weekly:
		return "1w"
	case Monthly:
		return "1M"
	}
	return "?"
}

// Group 把一串交易日按周 / 月成组。日线就是一天一组。
//
// 一组的边界是【首个交易日的开盘】到【末个交易日的收盘】——
// 注意那是墙钟时刻，中间跨了若干个自然日与休市段。
//
// days 必须按 Num 升序。
func (p CalendarPeriod) Group(days []Day) []BarBound {
	var out []BarBound
	var cur []Day
	flush := func() {
		if len(cur) == 0 {
			return
		}
		// 取【首个有时段的日】与【末个有时段的日】，不是首日与末日。
		//
		// 原来直接取 cur[0] / cur[len-1]，只要这一组的【边界日】没有时段，
		// 整组就被 return 掉——**丢的是一整周或一整月，而且不出声**。
		// 组里其余的日全都有时段也照丢。
		//
		// 全组都没有时段时才不出这一根：那时这一组确实没有任何交易时间，
		// 「没有」是对的答案，不是丢失。
		var open, close int64
		found := false
		for _, d := range cur {
			if len(d.Sessions) == 0 {
				continue
			}
			if !found {
				open, found = d.Sessions[0].Start, true
			}
			close = d.Sessions[len(d.Sessions)-1].End
		}
		cur = nil
		if !found {
			return
		}
		out = append(out, BarBound{Open: open, Close: close, Full: true})
	}
	for i, d := range days {
		if i > 0 && p.newGroup(days[i-1].Num, d.Num) {
			flush()
		}
		cur = append(cur, d)
		if p == Daily {
			flush()
		}
	}
	flush()
	return out
}

// newGroup 报告从交易日 prev 到 next 是否要开一组新的。
func (p CalendarPeriod) newGroup(prev, next TradingDay) bool {
	switch p {
	case Daily:
		return true
	case Monthly:
		py, pm, _ := prev.Split()
		ny, nm, _ := next.Split()
		return py != ny || pm != nm
	case Weekly:
		return isoWeek(prev) != isoWeek(next)
	}
	return true
}

func isoWeek(d TradingDay) [2]int {
	y, m, day := d.Split()
	t := time.Date(y, time.Month(m), day, 0, 0, 0, 0, time.UTC)
	wy, ww := t.ISOWeek()
	return [2]int{wy, ww}
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
