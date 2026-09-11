package tickflow

import (
	"errors"
	"fmt"
	"time"
)

// 本文件是 v0.3 同步层的【甲】：缺口分类器。对应 docs/design.md §七之九。
//
// ⛔ **它是纯函数**：日历、coverage、以及「这一天有没有根」全部由参数给，
// 内部不取时钟、不碰网络、不开文件。
// ⇒ 这是它能排在 `Store` 接口【之前】的全部理由 —— 它的五个输入
// （`Span` / 两个存储哨兵 / 两个日历哨兵）都住在本包，`Store` 接口定型推不翻它。

// GapKind 是缺口的七类。**零值不合法** —— 忘了填的调用方会当场被拒，
// 而不是拿到六类里的某一个（同 segfile.Outcome 那条理由）。
//
// ⚠️ 而零值在【本包内部】另有一个用处：`classifyTradingDay` 用它表示
// 「这一天有数据，不是缺口」。**那是一个不出包的哨兵**，不是第七类 ——
// 它出不了 `PlanGaps`，因为那一支根本不生成 `Gap`。
type GapKind int

const (
	// GapNeverFetched 没拉过。真值来自 coverage（这一天不在任何 Span 里）。
	//
	// **最危险的错认**：当成「拉过确认没有」⇒ 静默漏数据，且不会自愈。
	GapNeverFetched GapKind = iota + 1

	// GapConfirmedEmpty 拉过，确认没有。真值来自 coverage【且那一段走查过】。
	//
	// 最危险的错认：当成「没拉过」⇒ 每次都重拉一段确实没有的区间（吵，但不丢数据）。
	GapConfirmedEmpty

	// GapNotTrading 不是交易日。真值来自日历。
	//
	// ⚠️ 它是【被 Walk 跳过的那些天】，不是 Walk 报出来的东西 ——
	// 所以本层拿【自然日区间】与【Walk 走过的天】相减才得到它。
	// 而这正是⑨ 那一格的形状：**「日历说有、数据没给」与「日历本来就没说有」，
	// 在一个只看回调的循环里长得一样。**
	GapNotTrading

	// GapCalendarUnknown 日历答不了（品种没收录，或日期在 Covers 之外）。
	//
	// **最危险的错认**：当成「不是交易日」⇒ 静默跳过整段历史。
	//
	// ⚠️ **只有这一类的端点是【自然日】，不是交易日** —— 日历答不了那一段，
	// 「哪天是交易日」在那里没有答案。这条非对称是那一类的定义带来的，别去「统一」它。
	GapCalendarUnknown

	// GapStoreUnverified 存储答不了：这一段**本次没有被走查**（ErrSpanUnverified）。
	//
	// ⛔ **它的产生者在 (iv)（2026-09-11）整个换了一批，旧的那些全部消失了** ——
	// 而这一段的处置文字曾经是写给旧的那些的。写清楚，别让它静默变假：
	//
	//	(iv) 之前  「这一段刚写进来，还没轮到走查」·「孤儿记录」·
	//	           「多块同步的键分岔」·「一段都没走查（touched 空）」
	//	(iv) 之后  走查绑在【库】上（`Store.VerifyCoverage` 一遍核算每一段）
	//	           ⇒ 上面那些**一个都产不出它了**
	//
	// —— ✅ 今天只剩**一个**产生者 ——
	//
	//	**这一遍走查跑不起来**（`Store.VerifyCoverage` 的第二个返回值非 nil，例如读盘失败）
	//	⇒ 那时 `rep.TruncatedTails` 里有一条「这一遍走查没能跑起来：…」
	//	⇒ 处置：**看那条真因**（盘 / 权限 / 文件被占）。**重跑是对的** —— 它可能是瞬时的。
	//
	// ⛔ **而「Store 漏掉某一段」不在这一类里** —— 那是**违约**，而违约 ＝ **坏了**，不是答不了
	// ⇒ `verifyAll` 当场**中止**，不折进任何一类缺口（与 `classifyTradingDay` 那条兜底同一个处置）。
	// 🔴 折成缺口看起来更温和，而那是有害的：调用方会拿到一个**看起来可以照着处置的答案**，
	// 而那个处置不存在 ——「再走一遍」对一个不给结论的实现没有用。
	//
	// ⚠️ 而「走一遍就行，不必问人」这句话**已经从这里删掉了**。
	// 它在 (iv) 之前对好几种来历都是**循环**的（判别符与被判别的状态是同一个东西），
	// 而今天它对唯一剩下的那个产生者恰好成立 —— **恰好成立不是留着它的理由**：
	// 一句处置要说的是**去看什么**，不是**再做一次刚才那件事**。
	// 📎 本仓那条：**一句处置有三种坏法 —— 为假 · 为真而有害 · 循环。**
	//
	// —— ⛔⛔ 而它牵着一份【改不了的】注解，写在这儿 ——
	//
	// `v0.4.1` 的 tag 注解里，自查方法二逐字要人
	// **「跑一次 `Sync`，看 `rep.Gaps` 里有没有 `GapStoreUnverified`」**。
	// 🔴 (iv) 之后**坏库不再报这一类**（它们报 `GapStoreVerifyFailed`）
	// ⇒ 照那句话做的人**找不到它**，于是得出「这个库没问题」——**而库是坏的**。
	// ⇒ tag 改不了 ⇒ 勘误写在 `docs/release/v0.5.0.md`（本版的发布说明），
	// 而 `tag_selfcheck_meta_test.go` 钉着「勘误在，且点名了改看哪一类」。
	// 📎 ⇒ **在 `rep.Gaps` 里找不到这一类，不再是「库没问题」的证据。**
	//
	// —— ⛔ 两条早退【产不出】它，写在这儿免得有人去那儿找 ——
	//
	// 2026-09-11 实测（两格都断言过 `Halt`，因为第一版没走到那一支）：
	//
	//	HaltOutsideCoverage  缺口只有「日历答不了」     ⇒ 不产出
	//	HaltNoTradingDays    缺口只有「不是交易日」     ⇒ 不产出
	//
	// 成因：`classifyTradingDay` 是按**请求区间里的每一个交易日**调的，
	// 而那两条早退各自消灭了那个前提（一条没有交易日，一条那些天在日历之外）
	// ⇒ **没有任何一天落进 coverage 段。**
	//
	// —— 📌 历史：那句「走一遍就行」曾经【在默认配置下必然有害】——
	//
	// `cffexsource` 的出厂值是 `BatchDays: 1` ⇒ (o) 之前，一次**完全成功**的多块同步
	// 就会产出这一类（`touched` 里是分块的 span，而 `Coverage()` 给的是并段之后的值，
	// 两者做 map 键时不相等）。照那句话做 ⇒ 第二次 `Sync` 重拉 ⇒ 落盘失败。
	// ⇒ 它不是偶尔有害，是**必然**有害。（`false_unverified_test.go` 钉着修完之后的样子。）
	//
	// 最危险的错认：当成「拉过确认没有」⇒ 把「没验过」升级成一个肯定的答案。
	GapStoreUnverified

	// GapStoreLegacy 存储答不了：.meta 版本未知且源不可重放（ErrLegacyMeta）。
	// **需要一个显式决定** —— 必须问人，机器不许替他答。
	//
	// 最危险的错认：与上一类合并 ⇒「走一遍就好」被用在一个需要人拍板的格子上。
	GapStoreLegacy

	// —— 下面这一条是后加的，**加在末尾** ——
	//
	// ⛔ 理由与 `HaltReason` 那次同：**可见且有界的代价，优先于静默且无界的代价。**
	// 插在中间读起来更像一族（`GapStore*` 三条挨着），**而既有取值会静静地 +1** ——
	// 而那种依赖不会在编译期出声。⇒ 分组这件事写在注释里，一分钱数值代价都不用付。
	// （落地时让编译器印过改前改后：1..6 全部不变，新的这条是 7。）

	// GapStoreVerifyFailed 存储答不了：这一段**走查过了，而它没通过**（ErrSpanVerifyFailed）。
	//
	// ⚠️ 它与 `GapStoreUnverified` 分开，判据是**处置分不分岔**，不是「看起来像不像」：
	//
	//	GapStoreUnverified   本次没走查过这一段    ⇒ 它**不表示这一段有问题**
	//	GapStoreVerifyFailed 走查过了而没通过      ⇒ **别再重跑**，去读包在里面的真因
	//
	// 🔴 而名字说的是**现状**，不是「应该」：它说「走查过了而没通过」，不说「这一段坏了」——
	// **坏没坏要由那个真因回答，而真因就包在 `Err` 里**（`errors.Is` 取得到）。
	//
	// 最危险的错认：与上一类合并 ⇒ 「走一遍就好」被用在一个**重跑毫无意义**的格子上。
	GapStoreVerifyFailed
)

func (k GapKind) String() string {
	switch k {
	case GapNeverFetched:
		return "没拉过"
	case GapConfirmedEmpty:
		return "拉过确认没有"
	case GapNotTrading:
		return "不是交易日"
	case GapCalendarUnknown:
		return "日历答不了"
	case GapStoreUnverified:
		return "存储答不了·未走查"
	case GapStoreLegacy:
		return "存储答不了·旧格式"
	case GapStoreVerifyFailed:
		return "存储答不了·走查没通过"
	}
	return fmt.Sprintf("GapKind(%d)", int(k))
}

// Gap 是一段闭区间加上它属于哪一类。
//
// ⚠️ 用区间而不是逐日列表：一段十一年的「日历答不了」列成逐日，
// 是约 2700 行噪声 —— **一份全是噪声的告警等于没有告警**。
type Gap struct {
	From, To TradingDay
	Kind     GapKind
}

func (g Gap) String() string {
	if g.From == g.To {
		return fmt.Sprintf("%s %s", g.From, g.Kind)
	}
	return fmt.Sprintf("%s..%s %s", g.From, g.To, g.Kind)
}

// SpanStatus 是【存储对一段 coverage 的答复】：这一段在哪，以及它答不答得了。
//
// ⛔ 把 Span 与它的可答性放在一起，是因为**分开传就一定会错位**：
// 两个切片、两套下标，而错位之后每一天的类别都变了，且没有任何东西会响。
type SpanStatus struct {
	Span Span

	// Err 是这一段的可答性：nil / ErrSpanUnverified / ErrSpanVerifyFailed / ErrLegacyMeta。
	//
	// ⚠️ **这四个之外的一律走下面那条兜底** —— 而那一句是【白名单】，别把它改成黑名单。
	//
	// ⚠️ **别的错误值一律当成「坏了」** —— 中止，不折进缺口。见 PlanGaps 的兜底。
	Err error
}

// PlanGaps 把请求区间按【七类缺口】分好。签名与用法见 docs/design.md §七之九。
//
// 返回的是请求区间在【自然日】上的分段：**不重叠、有序、相邻且同类必已合并**。
// 有数据的那些天不出现在结果里（它们不是缺口），所以结果是一个
// **「除去有数据的天」之后的划分**，不是一个覆盖全区间的划分。
//
// ⛔ daysWithBars 或 coverage 报出一个本层认不出的错误 ⇒ **整段中止并返回它**。
// 「坏了」不是「答不了」：一个要中止，一个要报成缺口然后继续，
// 而两者在 `(bool, error)` 上长得一模一样（登记：冲突八）。
func PlanGaps(cal Calendar, k ProductKey, from, to TradingDay, cov []SpanStatus, daysWithBars func(Span) (map[TradingDay]bool, error)) ([]Gap, error) {
	if cal == nil {
		return nil, errors.New("tickflow: PlanGaps 需要一个日历——第三、第四类的真值只有它给得出")
	}
	if daysWithBars == nil {
		return nil, errors.New("tickflow: PlanGaps 需要 daysWithBars——" +
			"没有它就分不出「拉过，确认没有」和「有数据」，而那两者的处置相反")
	}
	// ⛔ **按段记忆：每一段最多读一次。** 这才是换读法的全部收益 ——
	// 逐日问 `O(天数 × 根数)` 变成整段问 `O(根数)`。
	//
	// ⚠️ 而它**不预取**：`daysOf` 只在 `classifyTradingDay` 真的走到那一行时才被调
	// （见那一行上面的注释）。⇒ `cov` 为空 ⇒ 这个 map 一次都不填 ⇒ **零次读存储**。
	// 🔴 「记忆」和「预取」在代码里长得很像，而它们在 `cov` 为空那一族上差一个整文件扫描。
	//
	// ⚠️ 错误**不进缓存**：一次失败不该把后面每一天都变成同一个错误的复读；
	// 而本层遇错即中止，所以重试的机会本来也只有一次。
	memo := make(map[Span]map[TradingDay]bool)
	daysOf := func(sp Span) (map[TradingDay]bool, error) {
		if m, ok := memo[sp]; ok {
			return m, nil
		}
		m, err := daysWithBars(sp)
		if err != nil {
			return nil, err
		}
		memo[sp] = m
		return m, nil
	}
	if !from.Valid() || !to.Valid() {
		return nil, fmt.Errorf("tickflow: PlanGaps 的区间不合法：from=%d to=%d", int32(from), int32(to))
	}
	if from > to {
		return nil, fmt.Errorf("tickflow: PlanGaps 的区间反了：from=%s 晚于 to=%s", from, to)
	}
	// ⛔ 端点必须是一个**真实存在的日子**，而 Valid() 只做粗筛（它自己写明了这一点）。
	//
	// 为什么这一层非查不可：**本层按自然日铺开**，而 natNext 会把不存在的日子
	// 归一化掉 —— 实测 `natNext(20200230) = 2020-03-02` ⇒ **真实的 2020-03-01
	// 一次都没被分类，而输出里还带着一个「2020-02-30」流进报告**。
	// ⇒ 那是「静默漏掉一天」，本仓最怕的那一族。
	// （评审方 2026-09-09 登记为「不拦」，我判它该拦：垃圾进可以，**静默跳过不行**。）
	for _, e := range [2]TradingDay{from, to} {
		if !isRealDate(e) {
			return nil, fmt.Errorf("tickflow: PlanGaps 的端点 %s 不是一个真实存在的日子"+
				"——Valid() 只做粗筛，而本层按自然日铺开：一个不存在的端点会让"+
				"natNext 归一化时【跳过】一个真实的日子，且没有任何东西会响", e)
		}
	}

	// 一、先问日历能回答哪一段 —— **求交必须在最前**。
	//
	// 放在后面的话，越界那几天会先被 coverage 判成「没拉过」——
	// 而「没拉过」会让调用方去重拉一段**日历根本答不了**的区间。
	cf, ct, covered := cal.Covers(k)
	lo, hi := from, to
	if covered {
		if lo < cf {
			lo = cf
		}
		if hi > ct {
			hi = ct
		}
	}
	inter := covered && lo <= hi

	// 二、交集内逐个【交易日】分类。Walk 只把交易日交给回调 ——
	// 所以「不是交易日」在它的输出里是一个【缺席】，要靠下面第三步相减才得到。
	seen := make(map[TradingDay]GapKind)
	if inter {
		var werr error
		if err := cal.Walk(k, lo, hi, func(d Day) bool {
			kd, e := classifyTradingDay(d.Num, cov, daysOf)
			if e != nil {
				werr = e
				return false
			}
			seen[d.Num] = kd // kd == 0 表示「有数据，不是缺口」
			return true
		}); err != nil {
			return nil, fmt.Errorf("tickflow: PlanGaps 遍历交易日失败：%w", err)
		}
		if werr != nil {
			return nil, werr
		}
	}

	// 三、按自然日铺开，逐日定类，相邻同类合成一段。
	//
	// ⚠️ 相邻性按【自然日】算，不按交易日 —— 按交易日的话，周末两侧的
	// 「没拉过」会合成一段，而那一段会**盖住**中间那个「不是交易日」⇒ 两类重叠。
	// **噪声可以靠筛，重叠不能靠筛。**
	var out []Gap
	for d := from; d <= to; d = natNext(d) {
		var kd GapKind
		switch {
		case !inter || d < lo || d > hi:
			kd = GapCalendarUnknown
		default:
			var ok bool
			kd, ok = seen[d]
			if !ok {
				kd = GapNotTrading // Walk 没走到 ⇒ 那天不交易
			}
		}
		if kd == 0 {
			continue // 有数据：不是缺口，而且它【断开】前后的段
		}
		if n := len(out); n > 0 && out[n-1].Kind == kd && out[n-1].To == natPrev(d) {
			out[n-1].To = d
			continue
		}
		out = append(out, Gap{From: d, To: d, Kind: kd})
	}
	return out, nil
}

// classifyTradingDay 回答「这一个交易日属于哪一类」。
// 返回 0 表示**有数据，不是缺口**（那是一个不出包的哨兵，见 GapKind 的注释）。
func classifyTradingDay(d TradingDay, cov []SpanStatus, daysOf func(Span) (map[TradingDay]bool, error)) (GapKind, error) {
	for _, s := range cov {
		if d < s.Span.From || d > s.Span.To {
			continue
		}
		switch {
		case errors.Is(s.Err, ErrSpanUnverified):
			return GapStoreUnverified, nil
		case errors.Is(s.Err, ErrLegacyMeta):
			return GapStoreLegacy, nil
		case errors.Is(s.Err, ErrSpanVerifyFailed):
			// ⚠️ 用 errors.Is 而不是 ==：真因是**包在里面**的
			// （`fmt.Errorf("%w: %w", ErrSpanVerifyFailed, 真因)`），
			// 而调用方还可能在外面再包一层。⇒ 这一支要穿透。
			return GapStoreVerifyFailed, nil
		case s.Err != nil:
			// ⛔ 兜底：这一维【不封闭】。「坏了」不是「答不了」——
			// 认不出的错误一律中止，不折进任何一类缺口。
			return 0, fmt.Errorf("tickflow: coverage 段 [%s, %s] 报了一个本层认不出的错误"+
				"——这是【坏了】，不是【答不了】，所以中止而不是报成缺口：%w",
				s.Span.From, s.Span.To, s.Err)
		}
		// ⛔ **这一行的【位置】就是那条性质**：`daysOf` 只在
		// 「这一天落进某个段 ＋ 该段没报错」之后才被调。
		// ⇒ `cov` 为空、或这一天不落在任何段里 ⇒ **一次都不读存储**
		// （下面那个 `return GapNeverFetched` 在 for 之外）。
		// 🔴 把它挪到循环外面无条件取整段，会让「全新品种/周期的首次同步」纯亏 ——
		// 代价与判据见 docs/design.md 二十·六「问五」甲。
		days, err := daysOf(s.Span)
		if err != nil {
			return 0, fmt.Errorf("tickflow: 问 [%s, %s] 哪些天有根时出错——这是【坏了】，"+
				"中止而不是报成「拉过，确认没有」：%w", s.Span.From, s.Span.To, err)
		}
		if days[d] {
			return 0, nil
		}
		return GapConfirmedEmpty, nil
	}
	return GapNeverFetched, nil
}

// natNext / natPrev 是【自然日】的后一天 / 前一天。
//
// ⛔ 不能直接对 TradingDay 加减 1：它是 YYYYMMDD，`20200101-1` 会得到 `20200100`。
// ⚠️ 而这两个函数**只用来铺开区间与判相邻**，不用来判「哪天交易」——
// 后者只有日历答得了，本包一个字都不猜。
func natNext(d TradingDay) TradingDay { return shiftDays(d, 1) }
func natPrev(d TradingDay) TradingDay { return shiftDays(d, -1) }

// isRealDate 判「这个 YYYYMMDD 是不是一个真实存在的日子」。
//
// 判据是**往返**：time.Date 会把 2020-02-30 归一化成 2020-03-01，
// 于是回读的 Day() 不再是 30 ⇒ 认得出来。
func isRealDate(d TradingDay) bool {
	y, m, day := d.Split()
	t := time.Date(y, time.Month(m), day, 0, 0, 0, 0, time.UTC)
	return t.Year() == y && int(t.Month()) == m && t.Day() == day
}

func shiftDays(d TradingDay, n int) TradingDay {
	y, m, day := d.Split()
	t := time.Date(y, time.Month(m), day, 0, 0, 0, 0, time.UTC).AddDate(0, 0, n)
	return TradingDay(t.Year()*10000 + int(t.Month())*100 + t.Day())
}
