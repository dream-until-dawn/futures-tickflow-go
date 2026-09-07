// Package embedded 提供内置的交易时段模板，以及一个由调用方注入交易日的 Calendar。
//
// # 它有什么、没有什么
//
// 【有】各品种的**标称时段模板**——来自天勤 openmd 的 trading_time，
// 外加从 1m 数据反推的广期所。相位按它算（见 tickflow.IntradayPeriod.Phase）。
//
// 【没有】交易日列表。本包**不维护节假日表**：那是每年国务院发文才定的，
// 调休规则复杂，维护一张表意味着每年要改一次代码，且改晚了就静默出错。
// 交易日由调用方注入——v0.3 的 calendar/derived 会从日线序列反推
// （有日线的那天就是交易日，这是从数据本身得到的真值）。
//
// 所以 New 要求显式给出交易日。不给就报错，**不猜**。
//
// # ⚠️ 内置模板的两条限定
//
//  1. **它是【当前】模板，不覆盖历史变更。** 夜盘时间历史上调整过多次——
//     实测 rb1605（2016 上半年）每交易日约 445 分钟，而 rb1801 约 345 分钟，
//     差了近一倍。所以 From 一律取一个保守的近期日期，更早的区间**没有依据**，
//     Template 会如实返回 ok=false，而不是拿当前模板去顶替。
//
//  2. **品种覆盖不全。** 模板来自 openmd 目录的前 5%（那份文件有 334 MiB），
//     其中**根本没有广期所**——GFEX 三个品种是另外从 1m 数据反推的。
//     没收录的品种同样返回 ok=false。
//
// 两条都是「宁可说不知道，也不给一个看起来合理的默认」——
// 一个错的时段模板不会报错，它只会让那些天的每一根 K 线都错开。
package embedded

import (
	"fmt"
	"sort"
	"time"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
)

const (
	hour = int64(3600000)
	min  = int64(60000)
)

// 三套日盘。相对时刻，当日 00:00 起的毫秒偏移。
var (
	dayCommodity = []tickflow.Session{ // 商品：09:00-10:15 / 10:30-11:30 / 13:30-15:00
		{Start: 9 * hour, End: 10*hour + 15*min},
		{Start: 10*hour + 30*min, End: 11*hour + 30*min},
		{Start: 13*hour + 30*min, End: 15 * hour},
	}
	dayEquityIndex = []tickflow.Session{ // 中金所股指：09:30-11:30 / 13:00-15:00
		{Start: 9*hour + 30*min, End: 11*hour + 30*min},
		{Start: 13 * hour, End: 15 * hour},
	}
	dayBond = []tickflow.Session{ // 中金所国债：09:15-11:30 / 13:00-15:15
		{Start: 9*hour + 15*min, End: 11*hour + 30*min},
		{Start: 13 * hour, End: 15*hour + 15*min},
	}
)

// night 造一段从 21:00 起、长 n 分钟的夜盘。跨日用 >24h 的偏移表示
// （天勤的 25:00 / 26:30 就是这个意思）。
func night(n int) []tickflow.Session {
	if n <= 0 {
		return nil
	}
	return []tickflow.Session{{Start: 21 * hour, End: 21*hour + int64(n)*min}}
}

// baseFrom 是内置模板的生效起点。
//
// 取 2020-05-06 是有理由的：那是疫情期间夜盘暂停后【恢复并调整】的日子，
// 大商所 / 郑商所的多个品种从 21:00–23:30 改成了 21:00–23:00。
// 更早的区间本包没有依据，Template 会返回 ok=false。
const baseFrom = tickflow.TradingDay(20200506)

// productNight 是各品种的标称夜盘长度（分钟）。0 表示无夜盘。
//
// 来源：天勤 openmd 的 trading_time（实测 9 种模式），
// 广期所来自 1m 数据反推（openmd 的前 5% 里没有 GFEX）。
var productNight = map[tickflow.ProductKey]int{
	// 21:00–02:30（330 分）：贵金属与原油
	{Exchange: tickflow.SHFE, Product: "au"}: 330,
	{Exchange: tickflow.SHFE, Product: "ag"}: 330,
	{Exchange: tickflow.INE, Product: "sc"}:  330,

	// 21:00–01:00（240 分）：有色
	{Exchange: tickflow.SHFE, Product: "cu"}: 240,
	{Exchange: tickflow.SHFE, Product: "al"}: 240,
	{Exchange: tickflow.SHFE, Product: "zn"}: 240,
	{Exchange: tickflow.SHFE, Product: "pb"}: 240,
	{Exchange: tickflow.SHFE, Product: "ni"}: 240,
	{Exchange: tickflow.SHFE, Product: "sn"}: 240,
	{Exchange: tickflow.SHFE, Product: "ss"}: 240,

	// 21:00–23:00（120 分）：黑色 / 化工 / 农产品
	{Exchange: tickflow.SHFE, Product: "rb"}: 120,
	{Exchange: tickflow.SHFE, Product: "hc"}: 120,
	{Exchange: tickflow.SHFE, Product: "bu"}: 120,
	{Exchange: tickflow.SHFE, Product: "fu"}: 120,
	{Exchange: tickflow.SHFE, Product: "ru"}: 120,
	{Exchange: tickflow.SHFE, Product: "sp"}: 120,
	{Exchange: tickflow.INE, Product: "nr"}:  120,
	{Exchange: tickflow.INE, Product: "bc"}:  120,
	{Exchange: tickflow.DCE, Product: "i"}:   120,
	{Exchange: tickflow.DCE, Product: "j"}:   120,
	{Exchange: tickflow.DCE, Product: "jm"}:  120,
	{Exchange: tickflow.DCE, Product: "m"}:   120,
	{Exchange: tickflow.DCE, Product: "y"}:   120,
	{Exchange: tickflow.DCE, Product: "p"}:   120,
	{Exchange: tickflow.DCE, Product: "a"}:   120,
	{Exchange: tickflow.DCE, Product: "b"}:   120,
	{Exchange: tickflow.DCE, Product: "c"}:   120,
	{Exchange: tickflow.DCE, Product: "cs"}:  120,
	{Exchange: tickflow.DCE, Product: "l"}:   120,
	{Exchange: tickflow.DCE, Product: "pp"}:  120,
	{Exchange: tickflow.DCE, Product: "v"}:   120,
	{Exchange: tickflow.DCE, Product: "eg"}:  120,
	{Exchange: tickflow.DCE, Product: "eb"}:  120,
	{Exchange: tickflow.DCE, Product: "pg"}:  120,
	{Exchange: tickflow.CZCE, Product: "TA"}: 120,
	{Exchange: tickflow.CZCE, Product: "MA"}: 120,
	{Exchange: tickflow.CZCE, Product: "FG"}: 120,
	{Exchange: tickflow.CZCE, Product: "SR"}: 120,
	{Exchange: tickflow.CZCE, Product: "CF"}: 120,
	{Exchange: tickflow.CZCE, Product: "OI"}: 120,
	{Exchange: tickflow.CZCE, Product: "RM"}: 120,
	{Exchange: tickflow.CZCE, Product: "ZC"}: 120,
	{Exchange: tickflow.CZCE, Product: "SA"}: 120,
	{Exchange: tickflow.CZCE, Product: "UR"}: 120,

	// 无夜盘：郑商所部分农产品
	{Exchange: tickflow.CZCE, Product: "AP"}: 0,
	{Exchange: tickflow.CZCE, Product: "CJ"}: 0,
	{Exchange: tickflow.CZCE, Product: "SF"}: 0,
	{Exchange: tickflow.CZCE, Product: "SM"}: 0,
	{Exchange: tickflow.CZCE, Product: "JR"}: 0,
	{Exchange: tickflow.CZCE, Product: "LR"}: 0,
	{Exchange: tickflow.CZCE, Product: "PM"}: 0,
	{Exchange: tickflow.CZCE, Product: "RI"}: 0,
	{Exchange: tickflow.CZCE, Product: "RS"}: 0,
	{Exchange: tickflow.CZCE, Product: "WH"}: 0,
	{Exchange: tickflow.DCE, Product: "jd"}:  0,
	{Exchange: tickflow.DCE, Product: "bb"}:  0,
	{Exchange: tickflow.DCE, Product: "fb"}:  0,
	{Exchange: tickflow.DCE, Product: "lh"}:  0,

	// 广期所：无夜盘（从 1m 数据反推，含 SHFE.rb 对照组）
	{Exchange: tickflow.GFEX, Product: "si"}: 0,
	{Exchange: tickflow.GFEX, Product: "lc"}: 0,
	{Exchange: tickflow.GFEX, Product: "ps"}: 0,
}

// 中金所：股指与国债时段不同，且都无夜盘。
var cffexDay = map[string][]tickflow.Session{
	"IF": dayEquityIndex, "IH": dayEquityIndex, "IC": dayEquityIndex, "IM": dayEquityIndex,
	"T": dayBond, "TF": dayBond, "TS": dayBond, "TL": dayBond,
}

// Template 返回该品种的内置标称模板。未收录、或交易日早于生效起点时 ok=false。
func Template(k tickflow.ProductKey, num tickflow.TradingDay) (tickflow.SessionTemplate, bool) {
	t, ok := template(k)
	if !ok || !t.Covers(num) {
		return tickflow.SessionTemplate{}, false
	}
	return t, true
}

func template(k tickflow.ProductKey) (tickflow.SessionTemplate, bool) {
	if k.Exchange == tickflow.CFFEX {
		d, ok := cffexDay[k.Product]
		if !ok {
			return tickflow.SessionTemplate{}, false
		}
		return tickflow.SessionTemplate{From: baseFrom, Day: d}, true
	}
	n, ok := productNight[k]
	if !ok {
		return tickflow.SessionTemplate{}, false
	}
	return tickflow.SessionTemplate{From: baseFrom, Day: dayCommodity, Night: night(n)}, true
}

// Products 返回内置模板覆盖的全部品种，供调用方检查覆盖面。
func Products() []tickflow.ProductKey {
	out := make([]tickflow.ProductKey, 0, len(productNight)+len(cffexDay))
	for k := range productNight {
		out = append(out, k)
	}
	for p := range cffexDay {
		out = append(out, tickflow.ProductKey{Exchange: tickflow.CFFEX, Product: p})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Exchange != out[j].Exchange {
			return out[i].Exchange < out[j].Exchange
		}
		return out[i].Product < out[j].Product
	})
	return out
}

// Calendar 是内置模板 + 注入的交易日。
type Calendar struct {
	days  []tickflow.TradingDay // 升序、去重
	index map[tickflow.TradingDay]int
}

// New 构造一个 Calendar。
//
// tradingDays 必须非空——本包**不维护节假日表**，也不从工作日近似
// （用工作日近似会在每个长假前后错一次，而那正是保证金上调的时候）。
// v0.3 的 calendar/derived 会从日线序列反推真值。
func New(tradingDays []tickflow.TradingDay) (*Calendar, error) {
	if len(tradingDays) == 0 {
		return nil, fmt.Errorf(
			"embedded: 必须显式给出交易日——本包不维护节假日表，也不从工作日近似。" +
				"v0.3 的 calendar/derived 会从日线序列反推")
	}
	ds := append([]tickflow.TradingDay(nil), tradingDays...)
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	out := ds[:0]
	for i, d := range ds {
		if !d.Valid() {
			return nil, fmt.Errorf("embedded: 交易日 %d 不像一个 yyyymmdd", int32(d))
		}
		if i == 0 || d != ds[i-1] {
			out = append(out, d)
		}
	}
	idx := make(map[tickflow.TradingDay]int, len(out))
	for i, d := range out {
		idx[d] = i
	}
	return &Calendar{days: out, index: idx}, nil
}

// Days 返回注入的交易日（升序、去重）。
func (c *Calendar) Days() []tickflow.TradingDay {
	return append([]tickflow.TradingDay(nil), c.days...)
}

// Template 见包级 Template。
func (c *Calendar) Template(k tickflow.ProductKey, num tickflow.TradingDay) (tickflow.SessionTemplate, bool) {
	return Template(k, num)
}

// DayOf 组装某个交易日的【实际】时段。
//
// ⚠️ 内置实现假定「实际 = 标称」——它**看不见停夜盘**这类逐日事实。
// 那需要从分钟数据反推（v0.3 的 calendar/derived）。
// 所以本实现在长假前后会给出多余的夜盘段。
// 相位不受影响（相位按标称算，是品种常量），受影响的是切分与判完结。
func (c *Calendar) DayOf(k tickflow.ProductKey, num tickflow.TradingDay) (tickflow.Day, bool) {
	i, ok := c.index[num]
	if !ok {
		return tickflow.Day{}, false
	}
	t, ok := Template(k, num)
	if !ok {
		return tickflow.Day{}, false
	}
	var ss []tickflow.Session
	// 夜盘挂在【上一个交易日】的自然日上——这正是「交易日 ≠ 自然日」。
	if len(t.Night) > 0 && i > 0 {
		base := midnight(c.days[i-1])
		for _, s := range t.Night {
			ss = append(ss, tickflow.Session{Start: base + s.Start, End: base + s.End})
		}
	}
	base := midnight(num)
	for _, s := range t.Day {
		ss = append(ss, tickflow.Session{Start: base + s.Start, End: base + s.End})
	}
	return tickflow.Day{Num: num, Sessions: ss}, true
}

// DayAt 返回包含 ts 的交易日。落在休市段时 ok=false。
func (c *Calendar) DayAt(k tickflow.ProductKey, ts int64) (tickflow.Day, bool) {
	// 夜盘最多往前挂一个交易日，所以只需看当天与下一个交易日。
	i := sort.Search(len(c.days), func(i int) bool {
		return midnight(c.days[i]) > ts
	})
	for j := i - 1; j <= i+1; j++ {
		if j < 0 || j >= len(c.days) {
			continue
		}
		d, ok := c.DayOf(k, c.days[j])
		if !ok {
			continue
		}
		for _, s := range d.Sessions {
			if s.Contains(ts) {
				return d, true
			}
		}
	}
	return tickflow.Day{}, false
}

// Walk 按升序遍历 [from, to] 之间的交易日。
func (c *Calendar) Walk(k tickflow.ProductKey, from, to tickflow.TradingDay, fn func(tickflow.Day) bool) error {
	if from > to {
		return fmt.Errorf("embedded: from(%d) 晚于 to(%d)", int32(from), int32(to))
	}
	for _, num := range c.days {
		if num < from {
			continue
		}
		if num > to {
			break
		}
		d, ok := c.DayOf(k, num)
		if !ok {
			continue
		}
		if !fn(d) {
			return nil
		}
	}
	return nil
}

// midnight 返回交易日编号对应自然日的 00:00（CST）的毫秒时间戳。
func midnight(d tickflow.TradingDay) int64 {
	y, m, day := d.Split()
	return timeDate(y, m, day)
}

func timeDate(y, m, d int) int64 {
	return time.Date(y, time.Month(m), d, 0, 0, 0, 0, tickflow.CST).UnixMilli()
}
