package tickflow

import (
	"errors"
	"fmt"
)

// TradingDay 是交易日编号，形如 20260907。
//
// 【具名类型，不是 int32 的别名】——防的正是它与自然日混用。
// 中国期货里两者不是一回事：夜盘 21:00 之后的交易日是【下一个】交易日，
// 周五夜盘属于下周一；一个交易日可能横跨三个自然日
// （周五夜盘 → 周六凌晨 → 周一日盘）。把它写成 int32，
// 「今天是几号」和「这是哪个交易日」就会在某处悄悄相等。
//
// 下游记账内核 futsim 用的是同一个表示（yyyymmdd 的具名 int32），
// 见 docs/design.md 第十二节——两边不必在边界上来回转。
type TradingDay int32

// Valid 报告 d 是否像一个 yyyymmdd。只做粗筛，不查是不是真实存在的日期。
func (d TradingDay) Valid() bool {
	y, m, day := d.Split()
	return y >= 1990 && y <= 2999 && m >= 1 && m <= 12 && day >= 1 && day <= 31
}

// Split 拆成年、月、日。
func (d TradingDay) Split() (year, month, day int) {
	n := int(d)
	return n / 10000, n / 100 % 100, n % 100
}

func (d TradingDay) String() string {
	y, m, day := d.Split()
	return fmt.Sprintf("%04d-%02d-%02d", y, m, day)
}

// Session 是一段连续交易时间，左闭右开 [Start, End)，毫秒墙钟。
type Session struct {
	Start, End int64
}

// Minutes 返回这一段的长度（分钟）。
func (s Session) Minutes() int { return int((s.End - s.Start) / 60000) }

// Contains 报告 ts 是否落在 [Start, End) 内。
func (s Session) Contains(ts int64) bool { return ts >= s.Start && ts < s.End }

// Day 是一个交易日：编号，加上它【实际】开了哪些时段。
//
// 「实际」是关键。长假前交易所停夜盘，那一天的 Sessions 里就是没有夜盘段——
// 这是逐日事实，区间模式表达不了。相位不看这里，看 SessionTemplate（标称），
// 两层的分工见 IntradayPeriod.Phase 的注释。
type Day struct {
	Num      TradingDay
	Sessions []Session // 升序；有夜盘的品种，第一段落在前一个自然日
}

// Minutes 返回这一天实际交易了多少分钟。
func (d Day) Minutes() int {
	n := 0
	for _, s := range d.Sessions {
		n += s.Minutes()
	}
	return n
}

// SessionTemplate 是一个品种的【标称】时段模板，带生效区间。
//
// 与 Day.Sessions（实际）分成两层，是被实测逼出来的：
// 网格【相位】按标称模板算，是品种常量，**与当天是否真的开了夜盘无关**——
// 停夜盘那天沪银的日盘网格仍然是带 30 分钟余数的那一套。
// 详见 docs/probe.md 的「坑三之三」。
//
// 带生效区间是因为夜盘时间历史上调整过多次：实测 rb1605（2016 上半年）
// 每交易日约 445 分钟，而 rb1801 约 345 分钟——夜盘长度差了近一倍。
type SessionTemplate struct {
	From, To TradingDay // 生效的交易日区间，闭区间；To 为 0 表示至今
	Day      []Session  // 相对时刻：当日 00:00 起的毫秒偏移
	Night    []Session  // 可能为空。跨日用 >24h 的偏移表示（天勤的 25:00 / 26:30）
}

// NightMinutes 返回标称夜盘总长（分钟）。没有夜盘时为 0。
func (t SessionTemplate) NightMinutes() int {
	n := 0
	for _, s := range t.Night {
		n += s.Minutes()
	}
	return n
}

// DayMinutes 返回标称日盘总长（分钟）。
func (t SessionTemplate) DayMinutes() int {
	n := 0
	for _, s := range t.Day {
		n += s.Minutes()
	}
	return n
}

// Covers 报告该模板是否在交易日 d 上生效。
func (t SessionTemplate) Covers(d TradingDay) bool {
	return d >= t.From && (t.To == 0 || d <= t.To)
}

// ProductKey 是时段表的键：交易所 + 品种。
//
// 【不能只用 product】——交易所本身就决定了时段大类
// （中金所股指 09:30 开、国债 09:15 开、商品 09:00 开），
// 而不同交易所可能有同名品种。
//
// 【也不含年月】——时段是按品种给的，不按合约。这一点与 refdata
// （合约规格，按合约，键里必须含四位年月否则跨十年会撞）相反：
// 同一个「跨十年会撞」的警告，对 calendar 不成立、对 refdata 成立，
// 差别就在键里有没有年月。
type ProductKey struct {
	Exchange string // SHFE DCE CZCE CFFEX INE GFEX
	Product  string // rb / TA / IF —— 大小写按交易所原生，见 Symbol
}

func (k ProductKey) String() string { return k.Exchange + "." + k.Product }

// 日历说不出结果时的三种原因。**它们必须分得开。**
//
// v0.1.0 的接口全都返回 `(X, bool)`，而那个 bool 承载了两件完全不同的事：
//
//	DayOf(rb, 20260905) → false   ← 周六，真的不是交易日
//	DayOf(rb, 20160104) → false   ← 2016 年，日历【答不了】（早于生效起点）
//
// 后者被当成前者的后果很具体：内置模板的生效起点是 2020-05-06，
// 而新浪 `RB0` 日线从 2009-03-27 起——**中间十一年日历全答「不知道」**，
// 上层读成「不是交易日」就会静默跳过日线最值钱的那一段，且不会自愈。
//
// ⚠️ **这三个错误值本身【不构成】护栏。** `if err != nil { continue }`
// 一行就把三种一起吞掉，成本和 `if !ok { continue }` 完全一样——
// Go 里 `error` 反而是「泛化忽略」最顺手的通道。
// 它们负责把话【说清楚】；真正拦住那个失败的是 Walk（见下）。
var (
	// ErrNotTradingDay 日历知道，而答案是「那天不交易」。
	ErrNotTradingDay = errors.New("tickflow: 该日不是交易日")

	// ErrClosed 日历知道，而那一刻不在任何交易时段内（休市段、周末夜里）。
	ErrClosed = errors.New("tickflow: 该时刻不在任何交易时段内")

	// ErrUncovered 日历【答不了】：品种没收录，或日期在覆盖区间之外。
	//
	// **这不是「没有交易」。** 把它当成后者，会静默跳过一整段历史。
	ErrUncovered = errors.New("tickflow: 日历覆盖不到——这是「答不了」，不是「没有交易」")
)

// Calendar 是交易日历：哪些日子是交易日，以及每个交易日实际开了哪些时段。
//
// 【按 ProductKey 给】，因为时段随品种不同，也随时间变。
//
// 本库不猜未来：覆盖不到的一律 ErrUncovered，由调用方决定怎么办。
// 反推只能覆盖到「已有数据」的最后一天，实盘要判断明天是否开市，
// 得靠交易所公告。
type Calendar interface {
	// DayAt 返回包含 ts 的交易日。
	// 休市 → ErrClosed；覆盖不到 → ErrUncovered。
	DayAt(k ProductKey, ts int64) (Day, error)

	// DayOf 按交易日编号取。
	// 那天不交易 → ErrNotTradingDay；覆盖不到 → ErrUncovered。
	DayOf(k ProductKey, num TradingDay) (Day, error)

	// Template 返回该品种在该交易日生效的【标称】时段模板。算相位要用。
	// 覆盖不到 → ErrUncovered。
	Template(k ProductKey, num TradingDay) (SessionTemplate, error)

	// Covers 报告日历对该品种能回答的交易日闭区间。
	// ok=false 表示这个品种整个答不了。
	//
	// 它让边界在【一行日志】里出现，而不是散在十一年的逐日错误里。
	Covers(k ProductKey) (from, to TradingDay, ok bool)

	// Walk 按升序遍历 [from, to] 之间的交易日。fn 返回 false 即停止。
	//
	// ⚠️ **区间只要有一端落在 Covers 之外就报错，绝不静默少遍历。**
	//
	// 这是这一组里唯一真正的护栏，理由是【循环在这里】：
	// 上层最自然的写法就是把整个请求区间交给 Walk，而那个写法因此天生安全——
	// 请 2009–2026 而日历只覆盖 2020–2026 会【当场炸】，
	// 不会安静地少同步十一年。
	//
	// 绕过 Walk 去逐日调 DayOf 当然做得到，但那需要自己写循环——
	// **那是一个看得见的选择，不是一个默认。**
	Walk(k ProductKey, from, to TradingDay, fn func(Day) bool) error
}

// TemplateMismatch 报告当日【实际】夜盘与【标称】模板是否矛盾。
//
// 这是一条**交易日一级、与周期无关**的事实——它取决于交易所那天开了多久夜盘，
// 不取决于你打算切成 5m 还是 60m。
//
// 曾经不是这样：矛盾原先由 `nightCarried > phase` 检测，**两个取模后的余数相比**，
// 于是同一个「标称 330 / 实际 310」的事实在 15m/30m 报、在 5m/60m/90m 不报
// （60m 下是一根装 40 分钟、不带标记的完整格子）。
// **标志骑在周期上**，上层按周期扫就会按周期漏。
//
// 判据里 `actual == 0` **不算矛盾**，理由是语义的而不是操作的：
//
//	模板描述的是【标称】时段；交易所因已知原因停一天，
//	**不构成「标称错了」的证据**。
//
// （操作上它也确实会太吵——每个长假前后都亮，按「一位总亮着等于没有」就废了。
// 但那是结果，上面那句才是原因。写原因，后来的人才知道边界该往哪挪。）
//
// ⚠️ **这条边界留了一个按设计静默的洞，见 contract.md 风险表**：
// 单日 `actual == 0`（长假）与**交易所永久取消该品种夜盘**，在 Day 这一级
// **不可分辨**。后者是真的模板过期，而它会让本函数永远返回 false、
// Phase 继续按陈旧的标称算——沪银日盘网格一直给 09:30/10:45/13:45/14:45/15:00，
// 而实际已经是 10:00/11:15/14:15/15:00：**每一根都错 30 分钟，永远，静默。**
// 区分需要【序列】（连续 N 个交易日 actual == 0），那是 v0.3 Syncer 的层级。
// 由 TestKnownDefect_PermanentNightCancellationLooksLikeHoliday 钉住。
//
// 返回 nominal / actual 是让上层能把差异写进报告，而不是只知道「有问题」。
func (d Day) TemplateMismatch(tmpl SessionTemplate) (nominal, actual int, mismatch bool) {
	nominal = tmpl.NightMinutes()
	night, _ := splitNightDay(d.Sessions)
	for _, s := range night {
		actual += int((s.End - s.Start) / 60000)
	}
	return nominal, actual, actual > 0 && actual != nominal
}
