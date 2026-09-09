package tickflow

import (
	"context"
	"errors"
	"fmt"
)

// 本文件对应 docs/design.md §五 的 `source.go`：Source 接口与它的契约层。
//
// ⚠️ v0.3 这一版【只落契约】：接口、请求/能力类型、以及把「实现必须保证」
// 那六条变成可执行的 CheckBars。**具体的源（sinasource / cffexsource）在后面几件里。**
//
// 分开不是为了少写，理由和 store.go 那次一样：
// **契约的不变量要先能被单独测红。** 混在第一个实现里，一条红了分不清
// 是「新浪的响应长得不一样」还是「契约本身写错了」——
// 而这两者的处置完全相反（前者改解析，后者改所有源）。

// —— Period：一个密封接口 ——

// Period 是「周期」这个概念的统一名字，**密封**：只有本包里的
// IntradayPeriod 与 CalendarPeriod 实现它，靠 isPeriod 这个不可导出方法封口。
//
// 为什么要密封（design.md §五）：Capabilities.Since 拿它做 map 键，
// 而一个只要求 String() 的开放接口，**任何带 String() 的类型都能塞进来**——
// 包括 time.Duration。密封之后「周期」在类型层面就是穷举的两种，
// 新增一种必须改本包，而改本包会撞上 TestPeriodIsSealed。
//
// ⛔ **实现者必须【可比较】** —— 这是一条硬要求，不是建议：
// `Capabilities.Since` 拿 Period 当 map 键，`Supports` 用 `==`。
// 一个带切片/映射/函数字段的实现者**编译期一声不响**，
// 到运行期才 `panic: hash of unhashable type`。
//
//	⚠️ **密封挡不住这一条。** 密封挡的是包外，
//	而不可比较的类型可以从包【内】加进来。
//	今天没炸，是因为恰好只有 `struct{min int}` 与 `int` 两种，两种都可比较 ——
//	**一个碰巧成立的性质，替一句声明背了书。**
//
// ⇒ 由 TestPeriodImplementorsAreComparable 查，而「有没有新增实现者」
// 由 TestPeriodImplementorsAreEnumerated 扫源码查（不是靠一张手写名单）。
//
// ⛔ **不要靠【嵌入】已有实现来获得 isPeriod。** 那样两条守卫都看不见它：
//
//	type X struct { IntradayPeriod; legs []string }   // 满足 Period，编译通过
//	periodImplementors 扫到的  [CalendarPeriod IntradayPeriod]   ⇒ **没有 X**
//	当 Since 的键               panic: hash of unhashable type
//
//	⚠️ 成因值得记：**AST 扫的是「谁【声明】了 isPeriod」，而接口要的是「谁【满足】它」。**
//	嵌入让这两个集合分开 —— **守卫扫的是【语法】，而契约管的是【类型】，低了一层。**
//
// （评审方 2026-09-09 构造出来的，登记⑧；无到期日，目前只写文档不设机械拦截。）
// 这一条是评审方 2026-09-09 用对照组实测出来的：一个带 []string 字段的
// 实现者放进 Since ⇒ 当场 panic。
//
// ⚠️ 它只承诺「能报出自己的名字」，**不承诺两种周期能互换使用**：
// IntradayPeriod.Bars(tmpl, day) 与 CalendarPeriod.Group(days) 签名不同，
// 那是真实的差异，不是没抽象好。把它们硬凑成一个方法，只会让调用方
// 拿到一个「有一半参数在这一种下没意义」的接口。
type Period interface {
	String() string

	// isPeriod 封口。不可导出 ⇒ 包外无法实现这个接口。
	isPeriod()
}

func (p IntradayPeriod) isPeriod() {}
func (p CalendarPeriod) isPeriod() {}

// 编译期断言：两种周期都真的实现了 Period。
// 放在这里而不是测试里，是因为**它该在编译时就红**——
// 一个「接口没人实现」的库，测试跑起来之前就已经错了。
var (
	_ Period = IntradayPeriod{}
	_ Period = CalendarPeriod(0)
)

// —— BarRequest ——

// BarRequest 是一次拉取请求。
//
// ⚠️ 区间按【交易日】给，而且是【闭区间】——两条都是决定，理由写在 design.md §五：
//
//	按交易日   夜盘属于下一个交易日 ⇒ 一个交易日的 K 线从前一个自然日的 21:00 就开始了。
//	           用毫秒区间表达「我要 09-04 这一天」，调用方得自己先算一遍日历，
//	           **而算错不报错，只是少一段夜盘。**
//	闭区间     Span（store.go）的 To 就是闭的。Syncer 的活是把 Span 翻成 BarRequest，
//	           两边语义不同的话那一步每次都要 ±1，**而 ±1 错了不报错**。
type BarRequest struct {
	Symbol Symbol     // 要哪个合约或主连
	Period Period     // 要什么周期
	From   TradingDay // 起（含）
	To     TradingDay // 止（含）
}

// Validate 检查请求本身立不立得住。
//
// 分开成一个方法而不是塞进各源的开头，是因为**每个源都要做同样的检查**，
// 而「每个实现各写一遍」正是它们会各自写错一遍的地方。
func (r BarRequest) Validate() error {
	var errs []error
	if r.Symbol.Exchange == "" || r.Symbol.Product == "" {
		errs = append(errs, fmt.Errorf("Symbol 不完整（Exchange=%q Product=%q）——"+
			"零值 Symbol 会被下游当成一个真实合约", r.Symbol.Exchange, r.Symbol.Product))
	}
	if r.Period == nil {
		errs = append(errs, errors.New("Period 是 nil——不给周期就没法判一根算不算完结"))
	}
	if !r.From.Valid() {
		errs = append(errs, fmt.Errorf("From=%d 不是一个合法交易日", int32(r.From)))
	}
	if !r.To.Valid() {
		errs = append(errs, fmt.Errorf("To=%d 不是一个合法交易日", int32(r.To)))
	}
	if r.From.Valid() && r.To.Valid() && r.From > r.To {
		errs = append(errs, fmt.Errorf("From=%s 晚于 To=%s——闭区间下这表示空请求，"+
			"而空请求和「拉过、确认没有」在 coverage 里长得一样", r.From, r.To))
	}
	return errors.Join(errs...)
}

// —— Capabilities ——

// Capabilities 报告一个源【在某个品种上】的能力与边界。
//
// ⚠️ 深度必须按品种问，不能只按周期。新浪的 1023 是【根数】上限，
// 而一个交易日出几根取决于品种的时段总长 ⇒ 同为 60m：
// AG0 只有约 4.8 个月、CU0 约 6.1 个月、RB0 约 8.5 个月（probe.md 坑一）。
// 按周期问的话，Syncer 会拿 RB0 的深度去向 AG0 要数据，
// 拿回 4.8 个月，然后把差额记成「拉过，确认没有」——**而且静默**。
type Capabilities struct {
	Periods []Period // 支持哪些周期
	MaxBars int      // 单次最多给多少根（新浪 1023，且【无法翻页】）

	// Since 是【绝对起点】：该源在这个品种的这个周期上，最早给得出哪个交易日。
	//
	// ⛔ 这里原本是 `Depth map[Period]time.Duration`（「能回溯多久」），
	// **而那个形状是错的，第一次实现就撞上了**（登记⑪）：
	//
	//	真实约束是一个【日期】—— 新浪合约级日线约从 2018-05 起有数据；
	//	而「能回溯多久」是一个**随时间增长的量** ⇒ 写成常量它明天就偏一天。
	//
	// ⚠️ 而换成 TradingDay 还多买到一样东西：**零值可辨。**
	//
	//	time.Duration(0)  「不知道」与「一天都给不了」**长得一模一样**
	//	TradingDay(0)     `Valid()` 为假 ⇒ **忘了填这件事本身可以被查出来**
	//
	// ⇒ 换形状的理由不是「新的更好听」，是**旧的把「没填」藏了起来**。
	Since map[Period]TradingDay

	HasSettle bool // 是否给结算价
	HasOI     bool // 是否给持仓量
	Realtime  bool
}

// Supports 报告这个源支不支持某个周期。
func (c Capabilities) Supports(p Period) bool {
	for _, q := range c.Periods {
		if q == p {
			return true
		}
	}
	return false
}

// Validate 检查这份能力声明自己自洽不自洽。
//
// ⛔ 两条，都挡具体的静默失败：
//
//	一、Periods 里有、Since 里没有 ⇒ 读出来是零值，而**没填这件事必须能被查出来**
//	二、Since 里的值不是一个合法交易日 ⇒ 同上，只是错得更明显
//
// ⚠️ 这两条在旧形状（`Depth map[Period]time.Duration`）下**第二条根本写不出来**：
// 任何 Duration 都是「合法」的，包括 0。**换成 TradingDay 之后它才有话可说。**
//
// ⛔ 而 MaxBars 那一格【仍然】不可分辨（登记⑫）：
// `0` 同时是「没有观察到硬顶」和「一根都给不了」。
// 本次不一并改，理由是它们**不是同一个毛病**：
// Since 那一格是「没填被藏起来」，而 MaxBars 那一格是「两个真实语义共用一个值」——
// 后者要么加一个 bool，要么换成指针，**两种都会让每个源多写一行样板**，
// 而它今天挡不住任何已知的错（本源不给分钟线，日线上没有硬顶）。
// **写下来，不顺手带过。**
func (c Capabilities) Validate() error {
	var errs []error
	for _, p := range c.Periods {
		d, ok := c.Since[p]
		if !ok {
			errs = append(errs, fmt.Errorf("周期 %s 在 Periods 里但不在 Since 里——"+
				"读出来会是零值，而「忘了填」和「真的从那天起」必须分得开", p))
			continue
		}
		if !d.Valid() {
			errs = append(errs, fmt.Errorf("周期 %s 的 Since=%d 不是一个合法交易日", p, int32(d)))
		}
	}
	if c.MaxBars < 0 {
		errs = append(errs, fmt.Errorf("MaxBars=%d 是负数", c.MaxBars))
	}
	return errors.Join(errs...)
}

// —— Source ——

// Source 是一个行情来源。
//
// 实现必须保证（design.md §五，其中前五条由 CheckBars 机械查）：
//
//	一、返回按 Ts 升序、无重复
//	二、只含【已完结】的 K 线 —— 上游没有标志位，实现要自己按交易日历判
//	三、只含落在 [From, To] 内的 —— 按 TradingDay 判，不按 Ts
//	四、TsEnd 与 TradingDay 必须填，不能留零值让上层猜
//	五、Ts 必须是【开盘】时刻 —— 新浪给的是收盘时刻标签，要换算回去
//	六、真的没数据时返回空切片而非错误（停牌、新合约上市前都会缺）
type Source interface {
	// Bars 返回 [From, To] 内【已完结】的 K 线，按 Ts 升序。
	Bars(ctx context.Context, req BarRequest) ([]Bar, error)

	// Caps 报告这个源【在某个品种上】的能力与边界。
	Caps(k ProductKey) Capabilities
}

// —— CheckBars：把那六条约定变成一次可执行的检查 ——

// CheckBars 拿一次请求和它的返回，核对 Source 的实现约定。
//
// now 是「现在」的墙钟毫秒，用来判「已完结」。**由调用方给，不在内部取 time.Now()**：
// 一个内部取时钟的检查器没法被测试固定住，于是它自己变成一个
// 「今天绿明天红」的东西 —— 而那种东西迟早会被人加个跳过。
//
// 它一次报出**全部**违反项（errors.Join），不是第一条就返回：
// 写实现的人要的是一张清单，不是一次修一个然后重跑六遍。
//
// ⚠️ 它比不了的【四】格，写在这里而不是留给下一个人去发现。
// 前两格是落这个文件时就知道的，后两格是评审方 2026-09-09 构造出来的：
//
//	一｜约定六「没数据时返回空切片而非错误」
//	  ⇒ 那是返回【路径】的形状，不在 bars 里，这里看不见。
//
//	二｜约定五「不得把收盘时刻标签当成 Ts」
//	  ⇒ 整体偏移一个周期的序列**自身完全自洽**：仍然升序、仍然落在区间内、
//	    TsEnd-Ts 仍然等于周期长。只有拿它和日历算出来的 BarBound 对齐才看得出来。
//	    ⇒ 这一格由各源自己的解析测试盯，新浪那一侧尤其要盯。
//
//	三｜**周期不符** —— 实测：req.Period=1m 而给一根 60 分钟的 K 线 ⇒ 返回 nil。
//	  ⇒ 而它**不是随手能补的**：跨休市段的日内 K 线，墙钟 TsEnd-Ts ≠ 周期长
//	    （60m 跨 10:15–10:30 那一段是 75 分钟）⇒ **和第二格同源：要日历。**
//
//	四｜**合约身份** —— 实测：`Bar` 的 12 个字段里，带合约身份字样的 **0 个**。
//	  ⇒ 一个源拿 `cu` 的数据回答 `rb` 的请求，CheckBars 全绿。
//	  ⚠️ 这一格和前三格**不同类**：前三格是「这里缺日历」，
//	    **这一格是数据模型里根本没有那个字段** ⇒ 它不可能在本函数里补，
//	    只能在【组装层】按请求核，或者不设防而写明。**目前是后者。**
//
// **写下来是为了让「没查」和「查过没问题」分得开。**
//
// ✅ **对照组（2026-09-09，落这个文件时跑的）：把实现里的判据【逐条关掉】，
// 每次只关一条，看是不是【正好那一条】用例转红、而对照用例仍绿 ⇒ 8/8 命中。**
//
// ⚠️ 而第一版对照组是【删掉】那两个 case，结果是 `declared and not used: prev`
// ⇒ **编译不过**。那种红证明不了任何断言 —— 它是本仓记过的那一类
// 「结构上不可能响的对照组」。改成把条件短路成永假（`false && ...`），
// 变量仍被读、编译得过、只有行为变了，这才是【只动一个变量】。
func CheckBars(req BarRequest, bars []Bar, now int64) error {
	var errs []error
	if err := req.Validate(); err != nil {
		errs = append(errs, fmt.Errorf("请求本身不合法：%w", err))
		// 请求都立不住时，下面按 From/To 做的判断没有意义，但升序、
		// 零值这几条仍然查得动 —— 所以不提前返回，只是不再信区间那一条。
	}
	rangeOK := req.From.Valid() && req.To.Valid() && req.From <= req.To

	var prev int64 = -1
	for i, b := range bars {
		switch {
		case b.Ts <= 0:
			errs = append(errs, fmt.Errorf("第 %d 根：Ts=%d 非正——零值 Ts 会被当成 1970 年", i, b.Ts))
		case b.Ts == prev:
			errs = append(errs, fmt.Errorf("第 %d 根：Ts=%d 与上一根重复", i, b.Ts))
		case b.Ts < prev:
			errs = append(errs, fmt.Errorf("第 %d 根：Ts=%d 小于上一根 %d——没有按升序", i, b.Ts, prev))
		}
		prev = b.Ts

		if b.TsEnd <= b.Ts {
			errs = append(errs, fmt.Errorf("第 %d 根：TsEnd=%d 不大于 Ts=%d——"+
				"TsEnd 是存下来的，不是读时现算的，留零值上层只能猜", i, b.TsEnd, b.Ts))
		}
		if !b.TradingDay.Valid() {
			errs = append(errs, fmt.Errorf("第 %d 根：TradingDay=%d 不合法——"+
				"夜盘属于下一个交易日，这个值上层推不出来", i, int32(b.TradingDay)))
		} else if rangeOK && (b.TradingDay < req.From || b.TradingDay > req.To) {
			errs = append(errs, fmt.Errorf("第 %d 根：TradingDay=%s 落在请求区间 [%s, %s] 之外",
				i, b.TradingDay, req.From, req.To))
		}
		if b.TsEnd > now {
			errs = append(errs, fmt.Errorf("第 %d 根：TsEnd=%d 在 now=%d 之后——"+
				"这是一根还没走完的 K 线，未完结的绝不进入本库任何一层", i, b.TsEnd, now))
		}
	}
	return errors.Join(errs...)
}
