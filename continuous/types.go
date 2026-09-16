// Package continuous 是主力连续（换月拼接 ＋ 复权）的类型层。对应 docs/design.md §八。
//
// ⛔ **这一片【只落类型，不落拼接】**，而那不是拆得细：起手那一节把「判据先于实现」钉成了条件
// （design.md §十五「v0.7 起手」）——**守卫先于实现落地才有意义**：
// 那条「本包不碰交易日历」的约束，要在**有代码可碰之前**就立住，否则第一处依赖会先于守卫进来。
//
// —— ⛔ 本包的硬约束：**不依赖交易日历** ——
//
// derived（v0.7 的另一半）拿本包的输出去判「那一夜开没开」，结论回头修正日历。
// ⇒ 本包若吃到【derived 修正过的那份日历】，判别符与被判别的状态就是同一个东西：
// **「证伪」这个动作在环里做不到** —— 改一次判据，输入跟着变。
//
//	换月点   Pick 是按天调的，而那条【天轴】取自「库里实际有根的交易日」，不取自 Calendar
//	缺口     不补：只在有根的日子上拼接；补缺口是 Syncer/Gap 那一层的事
//	接缝     换月日 ＝ Pick 的输出发生变化的那一天，由规则的输出定义
//
// ⇒ 承载体是一条守卫（`nocalendar_test.go`），不是这段注释：**注释挡不住「后来的人加一个 cal 参数」**。
// ⚠️ 而守卫守不住两格，写在这儿免得有人以为它覆盖了：
// ① 调用方在外面用日历筛过 bars 再喂进来 ⇒ 由 `Continuous.Days`（把天轴交出去）接住；
// ② 收进来的接口恰好带 `Calendar()` 方法而我们不调用 —— **不调用就不构成依赖**。
package continuous

import tickflow "github.com/dream-until-dawn/futures-tickflow-go"

// ContinuousSpec 是一条主连序列的**全部参数**。
//
// ⚠️ 同一个品种按不同换月规则、不同复权方式拼出来的是**不同的序列**（§八）——
// 所以落库时键里要带全部参数，不能只带品种。
type ContinuousSpec struct {
	// Product 是品种，形如 "SHFE.rb"。
	Product string

	// Roll 是换月规则；Adjust 是复权方式。
	Roll   RollRule
	Adjust AdjustMethod
}

// ContractDay 是**某一个具体合约在某一个交易日**的那几个数 —— 换月规则要看的全部输入。
//
// ⛔ 它**不含日历**：规则要的是「这一天这些合约各自多少量、多少持仓」，
// 而「这一天算不算交易日」在本包里由【库里有没有根】回答，不由日历回答。
type ContractDay struct {
	Symbol tickflow.Symbol
	Day    tickflow.TradingDay

	// Volume / OpenInterest 是那一天的成交量与持仓量。
	Volume       float64
	OpenInterest float64

	// Expiry 是这个合约的到期交易日；**`ExpiryUnknown`（＝0）表示不知道**（参考数据没给）。
	Expiry tickflow.TradingDay
}

// ExpiryUnknown 是 `ContractDay.Expiry` 的「不知道」。
//
// ⛔ **它为什么可以是零值，而本仓别处「零值不合法」** —— 两者的区别在【忘了填与真值分不分得开】：
// 别处的 0 会被误读成一个答案（「没有硬顶」「确认没有」）；这里 0 的来源是**参考数据没给**，
// 它本身就是一个合法的状态。⇒ 留 0 可以，**条件是每一个读它的人都被迫处理它**（评审方 2026-09-16 定的三样）：
//
//	一  具名常量（就是这一个）—— `if d.Expiry == 0` 那种写法**读不出意图**
//	二  规则侧**硬拒**：按到期日换月的规则拿到 ExpiryUnknown ⇒ **返回错误**，不是「跳过这个候选」——
//	    跳过会把「不知道到期日」悄悄变成「这个合约不参与换月」，那是最危险的方向
//	    ⚠️ 二与三要等规则落地（`FixedBarDaysBeforeExpiry` 在下一颗），**本颗只有一**
//	三  一格测试钉住二
//
// 🔴 **而排序是这一格最容易踩的**：0 在数值上比任何真实交易日都小 ⇒ **任何按 Expiry 排序的代码都会默默把「不知道」排到最前**，
// 而那不是任何人想要的语义。⇒ 排序/比较之前先把 ExpiryUnknown 摘出去（或当场拒），别让它参与比大小。
const ExpiryUnknown tickflow.TradingDay = 0

// RollRule 在给定交易日，从候选合约里选出主力。
//
// ⚠️ 返回 `tickflow.Symbol` 而不是 `string`（§八 原稿写的是 string）：本仓的 `Symbol` 是具名类型，
// 与 `Bar.Symbol`、`BarRequest.Symbol` 对齐；裸串会让 "rb2610" / "SHFE.rb2610" / "RB2610" 三种写法都编译得过。
type RollRule interface {
	Pick(day tickflow.TradingDay, cands []ContractDay) (tickflow.Symbol, bool)
}

// AdjustMethod 是复权方式。
//
// ⛔ **默认（零值）是不复权**，而「推荐后复权」是一句文档，不是一个默认值：
// 前复权以最新价为基准 ⇒ **每换一次月，全部历史价格都会变** ⇒ 同一段历史同一个策略，今天跑与下月跑结果不同，
// 而且不报错。这对「回测可复现」是致命的（§八「三条容易踩的」之一）。
type AdjustMethod int

const (
	// NoAdjust 不复权（＝ 新浪 RB0 的口径）。
	NoAdjust AdjustMethod = iota
	// RatioBack 后复权·比例。
	RatioBack
	// RatioFwd 前复权·比例。
	RatioFwd
	// DiffBack 后复权·价差。
	DiffBack
	// DiffFwd 前复权·价差。
	DiffFwd
)

// CountedSpan 是**换月规则实际数到的那一段** —— 数了几天、从哪天数到哪天。
//
// ⛔ 它存在的理由是一条**约定的可查性**（评审方 2026-09-16 立）：
// 本包的天轴是「库里有根的交易日」⇒ 库里缺一天，`FixedBarDaysBeforeExpiry(n)` 就会**数错**，
// 而那是调用方违反约定（那一段里有「没拉过」的日子）造成的。
//
//	不交出去  违反在上游、报错在下游、**中间没有痕迹** ⇒ 下游拿到一个默默算错的换月日
//	交出去    下游当场看得出「它数的 20 天里只有 18 天」
//
// 📎 本仓那条「保证绑在事件上就要问谁触发」的另一半：触发者是调用方 ⇒ **那就让结果带着它依赖的事实**。
type CountedSpan struct {
	// From / To 是实际数到的那一段的两端（闭区间，交易日）。
	From, To tickflow.TradingDay
	// Days 是这一段里**实际数到的天数**；规则要求 n 天而 Days < n 时，换月日是在不足的样本上定的。
	Days int
}

// Roll 是一个换月点。**接缝必须交出去**（§八）：
// 回测引擎要知道哪几天的收益来自换月而不是行情，以及换月本身要付成本（平旧开新，两笔手续费加两次冲击成本）。
type Roll struct {
	// Day 是换月发生在哪个交易日。
	Day tickflow.TradingDay
	// From / To 是旧合约 → 新合约。
	From, To tickflow.Symbol
	// Basis 是换月当日两个合约的价差（基差）；Factor 是复权因子。
	Basis  float64
	Factor float64
	// Counted 是定这个换月点时**实际数到的那一段**（见 CountedSpan）。
	Counted CountedSpan
}

// Continuous 是拼好的序列 ＋ 接缝 ＋ **它用的那条天轴**。
type Continuous struct {
	// Bars 是拼好的序列；Rolls 是每一个接缝。
	Bars  []tickflow.Bar
	Rolls []Roll

	// Days 是本次拼接用的【天轴】：库里实际有根的交易日，升序。
	//
	// ⛔ **必须交出去**，理由有两条：
	//
	//	一  derived 要用这条序列判「哪一天该有夜盘」—— 拿到一条「不知道按什么天拼的」序列，它判不了
	//	二  天轴的**两端取决于库的覆盖范围**（库里只拉了近月 ⇒ 起点晚于真实上市日）
	//	    ⇒ 起点附近按天数的规则会**数不满 n 天，而它不会报错** ⇒ 起止要看得见：Days[0] 与 Days[len-1]
	Days []tickflow.TradingDay
}
