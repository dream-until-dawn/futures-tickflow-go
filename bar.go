package tickflow

import "math"

// BarFlags 记录一根 K 线的完整性与来源。
type BarFlags uint32

const (
	// FlagPartial 已知不完整：时段中途熔断 / 停牌，或聚合时底层缺根。
	FlagPartial BarFlags = 1 << iota
	// FlagAggregated 由更低周期聚合而来，不是上游直接给的。
	FlagAggregated

	// 来源。多源共存时「这根是哪来的」是对账的第一个问题，
	// 事后从别处推不出来，所以记进每一根。
	FlagSrcSina
	FlagSrcShinny
	FlagSrcExchange
)

func (f BarFlags) Has(x BarFlags) bool { return f&x != 0 }

// Bar 是一根【已完结】的 K 线。
//
// 未完结的绝不进入本库任何一层。上游没有标志位可用——
// 天勤的 kline 对象字段是 close/close_oi/datetime/high/low/open/open_oi/volume，
// 最后一根与倒数第二根【字段集完全相同】，所以那不是「标志位为 false」，
// 是根本没有这个字段；新浪则更糟，它把还在累积的那根一并返回，
// 并打上「它将来会收盘的那个时刻」作为标签。
// 于是判完结只能靠交易日历——这正是本库把日历做成一等公民的理由。
type Bar struct {
	// Ts 是开盘墙钟时刻（毫秒）。
	Ts int64
	// TsEnd 是收盘墙钟时刻（毫秒）。
	//
	// 【与 TradingDay 一样，是存下来的，不是读取时现算的。】
	// 两者都能由 Ts 加交易日历推出来，存下来多花 12 字节，仍然选择存——
	// 因为它们依赖的那张表会变（夜盘时间历史上调整过多次），
	// 而已落库的数据不该跟着变。否则一个跑过的回测，升级一次库就换了结果，
	// 而且不报错。
	TsEnd int64
	// TradingDay 是所属交易日。夜盘属于【下一个】交易日。
	TradingDay TradingDay

	Open, High, Low, Close float64

	Volume       float64 // 成交量（手）
	Turnover     float64 // 成交额（元）
	OpenInterest float64 // 持仓量（手）—— 中国期货特有，主力判定要用
	// Settle 是当日结算价；仅日线有，分钟线为 NaN。
	//
	// ⚠️ 新浪的 s 字段【永远存在，但值按品种有 0.2%–98.6% 是 0】，
	// 中金所品种（IF/IH/IC/T/TF/TS）系统性缺失且持续至今。
	// 解析层必须把 0 映射成 NaN——0 是个看起来正常的价格，
	// 拿去做逐日盯市会算出一整天的灾难性盈亏而全程不报错。
	// 中金所的结算价走 CFFEX 官网 XML。
	Settle float64

	Flags BarFlags
}

// Lots 把手数字段取整。
//
// Volume 与 OpenInterest 本该是整数，用 float64 存是为了记录定长对齐。
// 读出来当整数用时【必须显式取整】——直接转换在接近整数的浮点值上会错 1。
func Lots(v float64) int64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return int64(math.Round(v))
}

// HasSettle 报告这根有没有可用的结算价。
//
// 【0 不算有】——没有任何品种的结算价会是零，0 只可能是缺失的伪装。
func (b Bar) HasSettle() bool {
	return !math.IsNaN(b.Settle) && b.Settle != 0
}

// Duration 返回开收盘之间的墙钟跨度（毫秒）。
// 注意它【不等于】周期长度：中间可能跨了休市段甚至隔夜缺口。
func (b Bar) Duration() int64 { return b.TsEnd - b.Ts }
