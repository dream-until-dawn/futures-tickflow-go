// Package embedded 提供内置的交易时段模板，以及一个由调用方注入交易日的 Calendar。
//
// # 它有什么、没有什么
//
// 【有】各品种的**标称时段模板**——来自天勤 openmd 的 trading_time，
// 外加从 1m 数据反推的广期所。相位按它算（见 tickflow.IntradayPeriod.Phase）。
//
// 【没有】交易日列表。本包**不维护节假日表**：那是每年国务院发文才定的，
// 调休规则复杂，维护一张表意味着每年要改一次代码，且改晚了就静默出错。
// 交易日由调用方注入——v0.4 的 calendar/derived 会从日线序列反推
// （有日线的那天就是交易日，这是从数据本身得到的真值）。
//
// 所以 New 要求显式给出交易日。不给就报错，**不猜**。
//
// # ⚠️ 内置模板的两条限定
//
//  1. **它是【当前】模板，不覆盖历史变更。** 夜盘时间历史上调整过多次——
//     ✅ 2026-09-09 起这句话有了日期，不再是一句掌故（`probe.md` 6.10）：
//     **SHFE `2016-05-03` ／ DCE `2019-03-29` ／ CZCE `2019-12-11`**，
//     一个交易所一个时点，同所内部一天不差。
//     ⚠️ 而这三个日子**全都落在本库的目标深度里**（天勤 1m 回溯到 2016-01-04）
//     ⇒ 它不是历史掌故，**是会被读到的数据**。
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
	"errors"
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
	// 中金所国债：09:30-11:30 / 13:00-15:15
	//
	// ⚠️ 起点曾写成 09:15，随 v0.1.0 发出去过。实测（天勤 1m，2026-09-07）：
	//   CFFEX.T / CFFEX.TF   09:30 → 11:30 ｜ 13:00 → 15:15
	//   CFFEX.IF / CFFEX.IC  09:30 → 11:30 ｜ 13:00 → 15:00   （对照，收盘早 15 分钟）
	//   SHFE.rb              09:00 → …                        （对照，证明读法能分辨）
	//
	// 根因：内置表抄自 openmd 目录，而那份目录的 trading_time 是【按合约】给的，
	// 被截断的快照里抄到的是【老合约】那一行——中金所后来把国债起点从 09:15
	// 挪到 09:30 与股指对齐。**一张来自过期快照的表，可以同时「缺行」和「行是旧的」**，
	// H1 那轮只查了前一种（GFEX 整个不在前缀里），没问在表里的行是不是也旧了。
	dayBond = []tickflow.Session{
		{Start: 9*hour + 30*min, End: 11*hour + 30*min},
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
// 2020-05-06 是**疫情期间夜盘暂停后恢复的第一天**（暂停了 64 个交易日，
// 2020-02-03 … 2020-04-30 每个交易日日盘照常、夜盘一根没有；`probe.md` 6.9 实测）。
// 更早的区间本包没有依据，Template 会返回 ok=false。
//
// ⛔ **这一段原来写的理由是错的，2026-09-09 实测更正**：
//
//	原文  「那是…恢复【并调整】的日子，大商所 / 郑商所的多个品种
//	       从 21:00–23:30 改成了 21:00–23:00」
//	实测   DCE.m / DCE.i    改在 **2019-03-29**
//	       CZCE.TA / CZCE.MA 改在 **2019-12-11**
//	       —— 都比 2020-05-06 早半年到一年多（`probe.md` 6.10，定到日）
//
// ⇒ **2020-05-06 那天发生的是【恢复】，不是【调整】。**
// 两件事被并进了同一句话，而它们隔着一年。
//
//	**一个日期同时被赋予两个理由，而只有一个是真的 ——
//	那比没有理由更难纠正：它读起来像已经查过了。**
//
// ⚠️ 而这条更正有个**可以拿的好处**，一并写在这儿，别丢了：
// 既然时段变更最晚发生在 2019-12-11，那么 **2019-12-11 … 2020-02-02 这一段的时段
// 和今天是一样的** ⇒ `baseFrom` 原则上能往前挪到 2019-12-11，**多盖约两个月**。
//
//	没有现在就挪，两个理由：
//	① 挪之前要先验「那一段每个品种的时段都和今天一致」——**已测 17 个品种**：
//	   11 个变过的最晚一次在 2019-12-11，6 个从来没变过 ⇒ 它们在
//	   2019-12-11…2020-02-02 的时段都等于今天（`probe.md` 6.10）。
//	   ⚠️ 而每所仍有十几个品种没测 ⇒ **这是强证据，不是全称。**
//	② 挪过去之后，2020-02-03…2020-05-06 那 64 个交易日会落在生效区间【之内】，
//	   而它们**没有夜盘** ⇒ 模板会对那一段全体报 `实际 < 标称`。
//	   要先能表达「这一段没有夜盘」。
//
// ⚠️ 而 ② 比它第一眼看上去小得多，**我最初把它写大了，这里更正**：
//
//	能表达吗   能。`SessionTemplate` 的 `Night` **本来就允许为空**，
//	           而 `From/To` 也早就在类型里 ⇒ 一段「Night 为空」的区间就是暂停区间。
//	接口够吗   够。`Template(k, num)` **已经收交易日了**。
//	缺的是什么 只缺 `template()` 的**函数体**：它现在无视 `num`，
//	           每个品种只揣着一份模板。
//
//	⇒ **所以那不是「本包没有这个概念」，是「本包每个品种只存了一段」。**
//	  前者听起来要改模型，后者只要把 map 的值从一份换成一个按 From 排序的切片。
//
// **一句「需要一个本包没有的概念」，会把一件小事说成一件大事 ——
// 而说大了的事，没人会去做。**
//
// ⇒ **在①②之前，2020-05-06 这个起点是保守但正确的**：它把两个麻烦一起挡在外面。
//
// ⚠️ **而「保守」的代价是量出来的，别只当它是一句形容词**（2026-09-09 实测，
// `shinny-night-gap` 顺带算的；分母是数据源给得出的目标深度 2016-01-05…2026-09-08，
// 共 2595 个交易日）：
//
//	生效起点              日历答得了的交易日      占目标深度
//	2020-05-06（今天）    1542                   **59%**
//	2019-12-11            1636                   63%
//	2016-05-03            2517                   **97%**
//
//	⇒ **今天有 41% 的目标深度，`Walk` 一律给 `ErrUncovered`。**
//
// **把「原则上能往前挪」变成三个数之后，这一格才第一次能被排优先级。**
//
// ⛔ **而这三行读起来像同一类的三个选项，那是错的**（评审方 2026-09-09，四处，我逐条核过）：
//
// **一、`2016-05-03` 那一行的前提，被我自己两行之上的那张表否掉。**
//
//	起点 2020-05-06 ⇒ 其后无变更 ⇒ 单段模板成立 ✅
//	起点 2019-12-11 ⇒ 其后无变更 ⇒ 单段模板成立 ✅
//	起点 2016-05-03 ⇒ **DCE 2019-03-29、CZCE 2019-12-11 都在它【之后】**
//	                  ⇒ DCE 品种有 2.9 年、CZCE 品种有 3.6 年用的不是今天的模板
//	⇒ **不是「再往前才要第二段」，是 `2016-05-03` 本身就要第二段、第三段。**
//
//	**三行摆在同一张表里，读起来像同一类的三个选项 —— 而其中一行的前提，另外两行给不出。**
//	（和「差 1 与差 4 被写进同一句解释里」同形：**同表不等于同类。**）
//
// **二、+38 不是一整块，而两块的价格不一样：**
//
//	59% → 63%   +4 点   三次变更都在 2019-12-11 之前 ⇒ 今天的模板仍成立
//	                    ⚠️ 但它把 2020-02-03…2020-05-06 那 64 天圈进生效区间
//	                       ⇒ 需要一段「Night 为空」⇒ **已经要多段**（至少三段）
//	63% → 97%   +34 点  还要 DCE / CZCE 的**旧模板** ⇒ 每个品种再加一到两段
//
//	⇒ **多段模板是这两块的【共同】前提**，而它的价格上面已经定过：只缺 `template()` 的函数体。
//	  ⇒ **做完机制那一半，+4 立刻到手；+34 再补数据。**
//	  **排优先级排的不是总量，是「第一块能单独交付的量」，以及「哪几块共用同一次改动」。**
//
// **三、6.10 那张表给的是【边界日期】，不是【模板】。** 它测的是「最晚一根」
// ⇒ 能重建**夜盘尾端**（SHFE 旧 240 分 / DCE / CZCE 旧 150 分），
// **而它对【日盘分段】一言未发** —— 「日盘这十年没变过」曾是一个**没有被那次测量覆盖的前提**。
//
// ✅ **2026-09-09 补测了**（探针 `shinny-day-segments`，记在 docs/probe.md 6.12）：
//
//	SHFE.rb / DCE.i / CZCE.MA   各 2595 个自然日，2016-01-05..2026-09-08
//	                            **十一年一个形态，零处变更**
//
// ⇒ 这一条从【前提】变成了【读数】。**+34 那一块不再压着一个没测过的假设。**
//
// ⚠️ 而射程要连着读，别把它读成全称：
//
//	一、15m 网格 ⇒ 只看得见 15 分钟的边界（判据是 10:15 这个标签的缺席 = 上午休息）
//	二、每所只测了一个主连 ⇒ 「只有某一个品种改了日盘」这条它答不了
//
// ⇒ **「商品日盘没变过」现在是三条序列上的读数，不是全市场的全称命题。**
//
// **四、分母是 `KQ.m@SHFE.rb` 那条序列，不是「全市场」。**
// `2595` 是 `rb` 的可得深度。反例就在仓里：GFEX 三个品种很年轻
// （`si` 2022-12、`lc` 2023-07、`ps` 2024-12）⇒ **对它们「日历答不了」是 0%。**
//
//	41% =「**最深的那条序列**上答不了的比例」—— 是**上界**
//	    ≠「按品种加权的答不了比例」—— 那个数一定更小
//	⇒ 它**不能**被读成「41% 的行情答不了」。
//	（而「回溯到 2016」这个目标本身就是由最深那条序列定义的，所以作为
//	**目标深度**的缺口，41% 是对的 —— 限定别掉。）
//
//	⇒ **一个比例，要连它的分母是从【哪一条序列】上数出来的一起写。**
//
// （范围写窄了，而**这一次是知情地写窄，不是没想到** —— 两者的区别写在这儿。）
const baseFrom = tickflow.TradingDay(20200506)

// productNight 是各品种的标称夜盘长度（分钟）。0 表示无夜盘。
//
// 来源：天勤 openmd 的 trading_time（实测 9 种模式），
// 广期所原来只能从 1m 数据反推 —— openmd 的【前 5%】里没有 GFEX。
//
// ✅ 2026-09-09 补上了直接证据：用 Range 抠 GFEX 所在的窗口，拿到 133 条，
// 它们的 `trading_time` 是 `night[]`（无夜盘），**与 1m 反推的结论一致**
// （`probe.md` 6.11）。两条独立的路给出同一个答案。
//
// ⚠️ 而那 133 条**全是期权，一条期货都没有** ——
// 所以这里 GFEX 那几行的依据仍然是「1m 反推 ＋ 期权元数据佐证」，
// **不是「期货的元数据说的」**。这个区别别抹掉。
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

// Template 返回该品种的内置标称模板。
// 未收录、或交易日早于生效起点时返回 ErrUncovered——**那是「答不了」，不是「没有交易」**。
//
// ⚠️ 它的覆盖判据【只有】品种与 baseFrom，**不含任何一份日历的注入区间** ——
// 因为它是包级函数，手上没有 Calendar 实例。
//
// ⇒ 所以它比 `(*Calendar).Template` 【宽】，而那不是缺陷，是分工：
//
//	包级这个    答「**内置表**对这一天怎么说」   ← 探针拿标称模板去比交易所实际给的时段，用的就是它
//	方法那个    答「**这份日历**答得了吗」       ← 它会先比 Covers（见 coversDay）
//
// ⛔ 2026-09-08 之前，方法那个是【直接转给这里】的 ——
// **等于借用了「标称表怎么说」去回答「这份日历答得了吗」**，
// 于是任何日历的未来日期都被答成「那天不交易」。来历与实测见 docs/contract.md §5。
func Template(k tickflow.ProductKey, num tickflow.TradingDay) (tickflow.SessionTemplate, error) {
	t, ok := template(k)
	if !ok {
		return tickflow.SessionTemplate{}, fmt.Errorf(
			"embedded: 未收录品种 %s.%s: %w", k.Exchange, k.Product, tickflow.ErrUncovered)
	}
	if !t.Covers(num) {
		return tickflow.SessionTemplate{}, fmt.Errorf(
			"embedded: %s.%s 的内置模板自 %s 起生效，问的是 %s: %w",
			k.Exchange, k.Product, baseFrom, num, tickflow.ErrUncovered)
	}
	return t, nil
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

	// coverFrom / coverTo 是覆盖区间的两端，**在 New 里算一次**。
	//
	// ⚠️ 它们存在的理由是实测出来的：Covers 原来每次都线性扫一遍 days，
	// 而 2026-09-09 那次改动把 Covers 放到了 DayOf / DayAt 的每一条路径上
	// ⇒ 1500 个交易日的日历上，DayAt 从 310ns 变成 3322ns（约 11 倍），
	// 而且**随日历长度线性增长**——17 年的日历会更糟。
	//
	// ⚠️ 而它们能被缓存，靠的是一个前提：**Calendar 在 New 之后不可变**。
	// 那个前提现在没有任何东西守着（没有 setter，但也没有守卫）。
	// ⇒ 哪天加了「往日历里补一天」这种方法，**这两个字段必须跟着更新**，
	// 否则它们就成了一份会过期的抄件——正是本仓反复栽的那一类。
	coverFrom tickflow.TradingDay
	coverTo   tickflow.TradingDay
	coverOK   bool // 注入的交易日里有没有落在 baseFrom 之后的
}

// New 构造一个 Calendar。
//
// tradingDays 必须非空——本包**不维护节假日表**，也不从工作日近似
// （用工作日近似会在每个长假前后错一次，而那正是保证金上调的时候）。
// v0.4 的 calendar/derived 会从日线序列反推真值。
func New(tradingDays []tickflow.TradingDay) (*Calendar, error) {
	if len(tradingDays) == 0 {
		return nil, fmt.Errorf(
			"embedded: 必须显式给出交易日——本包不维护节假日表，也不从工作日近似。" +
				"v0.4 的 calendar/derived 会从日线序列反推")
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
	c := &Calendar{days: out, index: idx}
	// 覆盖区间只算一次：注入列表与 baseFrom 的交集，两者在 New 之后都不再变。
	for _, d := range out {
		if d < baseFrom {
			continue
		}
		if !c.coverOK {
			c.coverFrom, c.coverOK = d, true
		}
		c.coverTo = d
	}
	return c, nil
}

// Days 返回注入的交易日（升序、去重）。
func (c *Calendar) Days() []tickflow.TradingDay {
	return append([]tickflow.TradingDay(nil), c.days...)
}

// Template 见包级 Template。
func (c *Calendar) Template(k tickflow.ProductKey, num tickflow.TradingDay) (tickflow.SessionTemplate, error) {
	// ⛔ 不能直接转给包级 Template：它的覆盖判据比本日历的 Covers 宽（见 coversDay）。
	// 上一版就是直接转的，于是对一个覆盖不到的日期【成功返回一份模板】——
	// **那比返回一个错误值更坏：它给的是一个能被拿去算相位的结构体。**
	if err := c.coversDay(k, num); err != nil {
		return tickflow.SessionTemplate{}, err
	}
	return Template(k, num)
}

// coversDay 报告 num 在不在本日历对 k 的【覆盖区间】内。
//
// ⚠️ 这一步存在的理由，是它当初【不存在】造成的后果（2026-09-08 实测，见 contract.md §5）：
// DayOf 第一步问包级 Template「答得了吗」——**顺序是对的，而它问错了人**：
// Template 的覆盖判据只有品种与 baseFrom，**不含注入的交易日区间**，
// 而 Covers 才是两者的交集。⇒ **一个比 Covers 宽的判据，被当成了 Covers 用。**
// 后果：任何日历的「未来日期」都会被答成「那天不交易」，
// 而「明天开不开市」正是 calendar.go 明写着「答不了，去看交易所公告」的那一格。
func (c *Calendar) coversDay(k tickflow.ProductKey, num tickflow.TradingDay) error {
	cf, ct, ok := c.Covers(k)
	if !ok {
		return fmt.Errorf("embedded: 未收录品种 %s.%s: %w",
			k.Exchange, k.Product, tickflow.ErrUncovered)
	}
	if num < cf || num > ct {
		return fmt.Errorf(
			"embedded: 问的是 %s，而本日历只覆盖 [%s, %s]"+
				"——这是【答不了】，不是「那天不交易」: %w",
			num, cf, ct, tickflow.ErrUncovered)
	}
	return nil
}

// coverageWindow 是覆盖区间在【时间戳】上的样子：半开区间 [lo, hi)，与 Session.Contains 同口径。
//
// ⛔ 下界【不能】自己算 midnight(cf)：有夜盘的品种，cf 那天的第一段挂在
// **上一个交易日的自然日**上，于是 Sessions[0].Start 可能【早于】midnight(cf)。
// 按午夜算，一个真属于 cf 的夜盘时刻会被判成 ErrUncovered。
// **这正是「交易日 ≠ 自然日」在时间戳侧的同一个形状**（评审方 2026-09-08 指出）。
func (c *Calendar) coverageWindow(k tickflow.ProductKey) (lo, hi int64, err error) {
	cf, ct, ok := c.Covers(k)
	if !ok {
		return 0, 0, fmt.Errorf("embedded: 未收录品种 %s.%s: %w",
			k.Exchange, k.Product, tickflow.ErrUncovered)
	}
	first, err := c.DayOf(k, cf)
	if err != nil {
		return 0, 0, err
	}
	last, err := c.DayOf(k, ct)
	if err != nil {
		return 0, 0, err
	}
	if len(first.Sessions) == 0 || len(last.Sessions) == 0 {
		return 0, 0, fmt.Errorf("embedded: %s.%s 的覆盖端点没有任何时段（实现自相矛盾）: %w",
			k.Exchange, k.Product, tickflow.ErrUncovered)
	}
	return first.Sessions[0].Start, last.Sessions[len(last.Sessions)-1].End, nil
}

// Covers 报告本日历对该品种能回答的交易日闭区间。
//
// 两个约束取交集：注入的交易日列表，与内置模板的生效起点 baseFrom。
// 品种没收录 → ok=false（整个答不了）。
func (c *Calendar) Covers(k tickflow.ProductKey) (from, to tickflow.TradingDay, ok bool) {
	// ⚠️ 品种那一半必须每次都问：覆盖 = 注入区间 ∩ baseFrom ∩ 【本品种收录了没有】，
	// 而只有前两者能预算。日期那一半在 New 里算好了（见 coverFrom/coverTo 的注释）。
	if _, ok := template(k); !ok {
		return 0, 0, false
	}
	return c.coverFrom, c.coverTo, c.coverOK
}

// DayOf 组装某个交易日的【实际】时段。
//
// ⚠️ 内置实现假定「实际 = 标称」——它**看不见停夜盘**这类逐日事实。
// 那需要从分钟数据反推（v0.4 的 calendar/derived）。
// 所以本实现在长假前后会给出多余的夜盘段。
//
// ⛔ **而偏差有【两个方向】，上一版只声明了「多给」那一个**（评审方 2026-09-09 指出）：
//
//	多给   长假前后 —— 上面那句
//	少给   **日历左端的第一个交易日（i == 0）没有【上一个交易日】可挂夜盘**
//	       ⇒ 下面那个 `i > 0` 把它的夜盘段【静默丢掉】
//
// 实测（注入 5 天，CZCE.TA）：
//
//	[0] 20240701  段数=3  225 分钟   ← 每次都是它
//	[1] 20240702  段数=4  345 分钟   （其余各日同）
//
// ⇒ **日历自己造出来的那一天，和「交易所真的停了夜盘」的那一天，
//
//	在本包输出里长得一模一样。** 而 `TemplateMismatch` 有意排除 `actual == 0`
//	（那个排除是对的），所以**没有任何东西会报它**。
//
//	**一个只声明了一半射程的注释，比没有声明更容易被信。**
//
// 今天不出事，理由两条，也都写下来：
//
//	一  上层那条判据的门槛是「连续 ≥ 2 天无夜盘」，而左端的假日子永远只有 1 天
//	二  内置日历从不产生真的无夜盘日
//
// ⚠️ **而第二条明年就会变**（v0.4 `calendar/derived` 落地 ⇒ 真的无夜盘日出现）：
// 若某次注入的 `days[0]` 恰是长假前最后一个交易日、`days[1]` 是假后第一个，
// 两者相连 = 连续 2 天 ⇒ 报出的区间**起点早一天、长度多一天**。
// ⇒ 那条不变量记在 `design.md` §七之六 的 `SYN-9`。
// 相位不受影响（相位按标称算，是品种常量），受影响的是切分与判完结。
func (c *Calendar) DayOf(k tickflow.ProductKey, num tickflow.TradingDay) (tickflow.Day, error) {
	// 顺序要紧：先问「答得了吗」，再问「那天交易吗」。
	//
	// 反过来的话，一个覆盖不到的日期会因为不在 index 里而被报成
	// ErrNotTradingDay——**又把「答不了」说成了「没有交易」**，
	// 只是换了个更像模像样的说法。
	//
	// ⛔ 上一版这里【只】问了包级 Template，而它的判据比 Covers 宽 ⇒ 顺序对、人问错了。
	// 实测后果与来历见 coversDay 与 contract.md §5。
	if err := c.coversDay(k, num); err != nil {
		return tickflow.Day{}, err
	}
	t, err := Template(k, num)
	if err != nil {
		return tickflow.Day{}, err
	}
	i, ok := c.index[num]
	if !ok {
		return tickflow.Day{}, fmt.Errorf("embedded: %s: %w", num, tickflow.ErrNotTradingDay)
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
	return tickflow.Day{Num: num, Sessions: ss}, nil
}

// DayAt 返回包含 ts 的交易日。
// 休市 → ErrClosed；覆盖不到 → ErrUncovered。
func (c *Calendar) DayAt(k tickflow.ProductKey, ts int64) (tickflow.Day, error) {
	// ⚠️ 能力问题优先于内容问题：「我答得了吗」必须在「答案是什么」之前定。
	// ErrClosed 是一个【实质答案】（那天有市，只是那一刻没在交易）——
	// 在不知道的时候给出实质答案，和 Template 那一格是同一个错，只是说法更像样。
	//
	// ⛔ 上一版没有这一步：一个落在时段内、但属于覆盖之外某一天的时刻，
	// 会被答成 ErrClosed（实测：+1 天 / +365 天两次都是）。
	lo, hi, err := c.coverageWindow(k)
	if err != nil {
		return tickflow.Day{}, err
	}
	if ts < lo || ts >= hi { // 与 Session.Contains 同口径：半开区间
		return tickflow.Day{}, fmt.Errorf(
			"embedded: %s 落在本日历的覆盖之外——这是【答不了】，不是「那一刻没在交易」: %w",
			time.UnixMilli(ts).In(tickflow.CST).Format("2006-01-02 15:04"),
			tickflow.ErrUncovered)
	}

	// 夜盘最多往前挂一个交易日，所以只需看当天与下一个交易日。
	i := sort.Search(len(c.days), func(i int) bool {
		return midnight(c.days[i]) > ts
	})
	for j := i - 1; j <= i+1; j++ {
		if j < 0 || j >= len(c.days) {
			continue
		}
		d, err := c.DayOf(k, c.days[j])
		if err != nil {
			// ⚠️ 这里【跳过】而不是抛出，包括 ErrUncovered。
			//
			// 「答不答得了」已经由上面那个 coverageWindow 判完了；
			// 到了这一层，职责只剩「这个时刻归哪一天」，而邻日可能本来就在覆盖之外
			// —— 注入列表里排在 cf 之前的那些日子就是（它们只用来给 cf 的夜盘定基准）。
			//
			// ⚠️ 原来这里是【抛】的，而那句「覆盖不到就直说」当时是对的：
			// 那时 DayOf 从不对覆盖内的日子报 ErrUncovered，抛出来的必定是真的答不了。
			// **给 DayOf 加了覆盖判定之后，同一行代码的含义就变了** ——
			// cf 的夜盘时刻会因为邻日（cf 的前一天）在覆盖外而被判成答不了。
			// 这一格由 TestCalendarContract_FirstDayNightSession_Red 顶着，
			// 而它正是在这次改动里当场红给我看的。
			//
			// ⛔ 而【只跳过理由覆盖得到的那两种】，别的原样抛出去。
			//
			// ⚠️ 来历：这一处是**评审方对 fix/embedded-uncovered-impl 那一格的意见**，
			// 而它落在了 slice2 这一格（我在那条分支上改完没提交就切了分支）。
			// 写在这儿，免得下一个人从 embedded 那一格的历史里找不到它的出处。
			// 上一版这里是无差别 `continue`，**理由只覆盖一种错，代码跳过全部**。
			// 影响面今天是 0（评审方与我各数过一遍 DayOf 的返回口子：
			// coversDay ⇒ ErrUncovered 这一支才可达），
			// **而那不是留着它的理由**：一个被吞掉的未知错误会让 DayAt
			// 落到函数尾部返回 ErrClosed —— **不知道，却给出了一个实质答案**，
			// 和 Template 那一格同形，只是小一号。
			if errors.Is(err, tickflow.ErrUncovered) || errors.Is(err, tickflow.ErrNotTradingDay) {
				continue
			}
			return tickflow.Day{}, err
		}
		for _, s := range d.Sessions {
			if s.Contains(ts) {
				return d, nil
			}
		}
	}
	return tickflow.Day{}, fmt.Errorf("embedded: %s: %w",
		time.UnixMilli(ts).In(tickflow.CST).Format("2006-01-02 15:04"),
		tickflow.ErrClosed)
}

// Walk 按升序遍历 [from, to] 之间的交易日。
//
// ⚠️ **区间只要有一端落在 Covers 之外就报错。** 这是本组里唯一真正的护栏：
// 上层最自然的写法就是把整个请求区间交给 Walk，于是那个写法天生安全——
// 请 2009–2026 而日历只覆盖 2020–2026 会当场炸，
// 而不是安静地少遍历十一年、再让上层以为那十一年没有交易日。
func (c *Calendar) Walk(k tickflow.ProductKey, from, to tickflow.TradingDay,
	fn func(tickflow.Day) bool) error {
	if from > to {
		return fmt.Errorf("embedded: from(%s) 晚于 to(%s)", from, to)
	}
	cf, ct, ok := c.Covers(k)
	if !ok {
		return fmt.Errorf("embedded: 未收录品种 %s.%s: %w",
			k.Exchange, k.Product, tickflow.ErrUncovered)
	}
	if from < cf || to > ct {
		return fmt.Errorf(
			"embedded: 请求 [%s, %s]，而本日历只覆盖 [%s, %s]"+
				"——超出的那一段【答不了】，不是「没有交易日」: %w",
			from, to, cf, ct, tickflow.ErrUncovered)
	}
	for _, num := range c.days {
		if num < from {
			continue
		}
		if num > to {
			break
		}
		d, err := c.DayOf(k, num)
		if err != nil {
			// 走到这里只可能是 ErrNotTradingDay 之外的意外——
			// 区间已经在覆盖内了，所以任何 ErrUncovered 都是实现自相矛盾。
			if errors.Is(err, tickflow.ErrUncovered) {
				return fmt.Errorf("embedded: 区间在覆盖内却报答不了（实现 bug）: %w", err)
			}
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
