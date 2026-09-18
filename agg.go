package tickflow

import (
	"errors"
	"fmt"
	"math"
)

// AggRule 是日内高周期由低周期聚合时用的口径。两套内容不同（probe.md 6.x「天勤 = 纯时钟网格」那一节）：
//
//	AggTradingAxis  交易时间轴 ＋ 相位，按【收盘时刻】标注（= 新浪）：60m 标 09:30 10:45 …，
//	                10:45 那根装 10:00–10:15 ＋ 10:30–10:45；边界就是 IntradayPeriod.Bars
//	AggClockGrid    时钟网格，按【开盘时刻】标注（= 天勤）：60m 标 09:00 10:00 …，空格子省略、残格子保留
//
// ⛔ **零值不合法**（用户 2026-09-18 裁 U1「不设默认，调用方必须选」）：
// 两套的默认取哪套要看国内主流软件用哪套，本库没测 ⇒ 不替调用方选。
// 以后加默认是兼容的，已发出去的默认再改是破坏性的。零值 ⇒ ErrAggRuleUnset。
type AggRule int

const (
	aggRuleUnset   AggRule = iota // 零值：没选。不导出 —— 它不是一个可选项
	AggTradingAxis                // 交易时间轴 ＋ 相位（= 新浪）
	AggClockGrid                  // 时钟网格（= 天勤）
)

// ErrAggRuleUnset：AggRule 是零值（没选）。聚合口径本库不设默认，调用方必须显式给。
var ErrAggRuleUnset = errors.New("tickflow: AggRule 没有选（零值不合法）：本库不设默认聚合口径，请显式给 AggTradingAxis 或 AggClockGrid")

func (r AggRule) String() string {
	switch r {
	case aggRuleUnset:
		return "AggRule(未选)"
	case AggTradingAxis:
		return "AggTradingAxis"
	case AggClockGrid:
		return "AggClockGrid"
	}
	return fmt.Sprintf("AggRule(%d)", int(r))
}

// check 零值 ⇒ ErrAggRuleUnset；未知值 ⇒ 另一个错误（不 Is 它：那是调用方造了个不存在的值，不是没选）。
func (r AggRule) check() error {
	switch r {
	case AggTradingAxis, AggClockGrid:
		return nil
	case aggRuleUnset:
		return ErrAggRuleUnset
	}
	return fmt.Errorf("tickflow: 未知的 AggRule %d", int(r))
}

// clockAlign 是时钟网格对齐的周期上界：周期必须整除 480 分钟。
//
// 为什么是 480 不是 1440：网格「对齐到哪个零点」本库没验 —— 按北京时间零点，还是按 UTC 零点
// （天勤的时间戳是 UTC 纳秒）。两者差 8 小时 ＝ 480 分钟；周期整除 480 时两种对齐切出同一套格子，
// 不整除时（例如 90m、180m）会差出相位。⇒ **只收两种对齐答案相同的周期**，不替未验的那一问选答案。
// 实测过的只有 15m / 30m / 60m 的标签（probe.md「天勤 = 纯时钟网格」），格子内容用的是那一节的文字读数。
const clockAlign = 480

// cstOffsetMs 北京时间相对 UTC 的偏移（毫秒）。中国期货只有这一个时区，不随夏令时变。
const cstOffsetMs = 8 * 3600 * 1000

// Bounds 给出交易日 d 在规则 r、周期 p 下的全部格子边界，升序、左闭右开。
//
//	AggTradingAxis  ＝ p.Bars(tmpl, d)，原样
//	AggClockGrid    与 d.Sessions 相交的每个时钟格子一根：[格子起点, 格子终点)，**不按时段裁剪**
//	                （10:00 那根 60m 的 Close 是 11:00，而它装的是 10:00–10:15 ＋ 10:30–11:00；装了多少用 Minutes）
//
// 模板与实际矛盾（Day.TemplateMismatch）时两套都整天标 Anomalous —— 那是交易日一级的事实，与规则无关。
func (r AggRule) Bounds(p IntradayPeriod, tmpl SessionTemplate, d Day) ([]BarBound, error) {
	if err := r.check(); err != nil {
		return nil, err
	}
	if p.min <= 0 {
		return nil, fmt.Errorf("tickflow: 周期 %s 不合法", p)
	}
	if r == AggTradingAxis {
		return p.Bars(tmpl, d), nil
	}
	if clockAlign%p.min != 0 {
		return nil, fmt.Errorf("tickflow: AggClockGrid 只收整除 %d 分钟的周期，收到 %s（对齐零点未验，见 clockAlign）", clockAlign, p)
	}
	step := int64(p.min) * 60000
	var out []BarBound
	for _, s := range d.Sessions {
		for c := floorTo(s.Start+cstOffsetMs, step) - cstOffsetMs; c < s.End; c += step {
			if n := len(out); n > 0 && out[n-1].Open == c {
				continue // 同一个格子里的第二段（10:00 格子里的 10:30–11:00）
			}
			out = append(out, BarBound{Open: c, Close: c + step, Full: true})
		}
	}
	if _, _, bad := d.TemplateMismatch(tmpl); bad {
		for i := range out {
			out[i].Anomalous = true
		}
	}
	return out, nil
}

func floorTo(x, step int64) int64 {
	q := x / step
	if x%step != 0 && x < 0 {
		q--
	}
	return q * step
}

// Aggregate 把交易日 d 的低周期根 bars 按规则 r 聚合成周期 p。
//
// 输入要求（不满足就报错，不猜）：
//
//	bars 全部属于 d（TradingDay == d.Num）、按 Ts 升序、互不重叠（Ts ≥ 上一根的 TsEnd）
//	每一根整个落在某一个格子里（Open ≤ Ts 且 TsEnd ≤ Close）—— 跨格子的根说明低周期与这套格子不相容
//
// 输出每根：Ts / TsEnd ＝ 格子边界 · O 首根开 · H 最高 · L 最低 · C 末根收 · Volume 与 Turnover 求和（任一 NaN ⇒ NaN）·
// OpenInterest 取末根 · Settle NaN（分钟线没有结算价）· Flags 带 FlagAggregated，并保留输入的来源位。
//
// 完整性（design.md §十五「v0.9 起手」丙，评审方 2026-09-18 定的三条规则）：
//
//	规则一  格子里一根输入都没有 ⇒ **不产出根**（不造 Volume 0 的空根）
//	规则二  格子里有输入，但输入覆盖的交易分钟 < 格子的交易分钟（BarBound.Minutes）⇒ 产出根，带 FlagPartial
//	规则三  覆盖满 ⇒ 不带 FlagPartial（除非某根输入自己就带 FlagPartial —— 不完整向上传）
//
// ⚠️ 规则二**只对「在零成交的分钟也给占位根」的源成立**：
//
//	天勤  给（probe.md 6.20 E2：rb2701 上市首周 2000 根里 1542 根零成交，根照给）
//	新浪  很可能不给（probe.md 6.35 五之二：RB2601 到期前九天 5m 每天只有 20–66 根，应为 69；推论，没有第二来源核）
//	⇒ 底层是新浪分钟线时，规则二会把「没成交」当成「缺数据」标 FlagPartial
//
// ⚠️ 规则一的代价：「停夜盘」与「源侧整段丢数」在这一层是同一形状，分不开 —— 分它们的是 Sync 的 coverage / Gaps。
// 交易时间轴跨时段分组时，停夜盘那天横跨夜盘尾与日盘头的那一根按规则二带 FlagPartial：
// 那是日历缺陷（embedded 看不见停夜盘，v0.7「还没定」丙）传到了标记上，记下不绕。
func Aggregate(r AggRule, p IntradayPeriod, tmpl SessionTemplate, d Day, bars []Bar) ([]Bar, error) {
	bounds, err := r.Bounds(p, tmpl, d)
	if err != nil {
		return nil, err
	}
	for i, b := range bars {
		if b.TradingDay != d.Num {
			return nil, fmt.Errorf("tickflow: Aggregate 第 %d 根属于交易日 %s，不是 %s", i, b.TradingDay, d.Num)
		}
		if b.TsEnd <= b.Ts {
			return nil, fmt.Errorf("tickflow: Aggregate 第 %d 根 TsEnd ≤ Ts", i)
		}
		if i > 0 && b.Ts < bars[i-1].TsEnd {
			return nil, fmt.Errorf("tickflow: Aggregate 第 %d 根与上一根重叠或乱序", i)
		}
	}
	var out []Bar
	j := 0
	for _, bd := range bounds {
		var agg Bar
		n, covered := 0, 0
		for ; j < len(bars) && bars[j].Ts < bd.Close; j++ {
			b := bars[j]
			if b.Ts < bd.Open || b.TsEnd > bd.Close {
				return nil, fmt.Errorf("tickflow: Aggregate 有一根 [%d, %d) 不整个落在 %s 的任何一个格子里（低周期与这套格子不相容）", b.Ts, b.TsEnd, r)
			}
			if n == 0 {
				agg = Bar{Ts: bd.Open, TsEnd: bd.Close, TradingDay: d.Num, Open: b.Open, High: b.High, Low: b.Low, Settle: math.NaN()}
			}
			agg.High = math.Max(agg.High, b.High)
			agg.Low = math.Min(agg.Low, b.Low)
			agg.Close = b.Close
			agg.Volume += b.Volume
			agg.Turnover += b.Turnover
			agg.OpenInterest = b.OpenInterest
			agg.Flags |= b.Flags &^ FlagAggregated
			covered += BarBound{Open: b.Ts, Close: b.TsEnd}.Minutes(d)
			n++
		}
		if n == 0 {
			continue // 规则一
		}
		agg.Flags |= FlagAggregated
		if covered < bd.Minutes(d) {
			agg.Flags |= FlagPartial // 规则二
		}
		out = append(out, agg)
	}
	if j < len(bars) {
		b := bars[j]
		return nil, fmt.Errorf("tickflow: Aggregate 有一根 [%d, %d) 不落在 %s 的任何一个格子里", b.Ts, b.TsEnd, r)
	}
	return out, nil
}
