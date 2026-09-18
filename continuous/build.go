package continuous

import (
	"errors"
	"fmt"
	"math"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
)

// —— 拼接：把「每天哪些合约在市」变成一条主连序列 ＋ 接缝 ＋ 天轴 ——
//
// ⛔ **本包只收【数据】，不收【提供者】**（design.md §十五「自指环」那一节的落地形态）：
// `Build` 收的是一个按天排好的切片，不是一个能去取数的东西（Store / Source / Calendar 一概不收）。
// ⇒ 谁去取、取哪一段、缺不缺，全在调用方那边；本包只对**递到手里的数据**做判定。
// 📎 这条比「包里不出现 Calendar」更接近本质 —— 那一条按名字判，而这一条按**能力**判（见 noprovider_test.go）。

var (
	// ErrDaysNotAscending 递进来的天不是严格升序（或有重复）。
	//
	// ⛔ 不自己排序而是拒：排序会把「调用方给错了顺序」变成一次静默的修复，
	// 而顺序错多半意味着**上游拼装数据时就错了**，那时后面每一个数都可疑。
	ErrDaysNotAscending = errors.New("continuous: 递进来的交易日不是严格升序")

	// ErrExpiryUnknown 规则要按到期日判，而候选里有合约的 Expiry 是 ExpiryUnknown。
	//
	// 🔴 **返回错误，不是跳过这个候选** —— 跳过会把「不知道它什么时候到期」
	// 悄悄变成「这个合约不参与换月」，而后者是一个**肯定的答案**。最危险的方向。
	ErrExpiryUnknown = errors.New("continuous: 候选合约的到期日是 ExpiryUnknown（参考数据没给），按到期日换月的规则判不了")

	// ErrNoBarForPick 规则选出了某个合约，而这一天没有它的那一根。
	ErrNoBarForPick = errors.New("continuous: 规则选出的合约在这一天没有根")

	// ErrRollPriceUnusable 换月这一刻，两边的价格里有一个用不了（0 或非有限）。
	//
	// 🔴 **判在源头，不给四种复权各自兜底**（评审方 2026-09-16 造输入量出来的，我认）：
	// 同一份「旧合约收盘 ＝ 0」的输入，四种方式**各错各的** ——
	//
	//	RatioBack  静默变成「没复权」（那是我原来写的兜底）
	//	RatioFwd   早段价格**全被乘成 0**（这一支根本没有兜底）
	//	DiffBack   最新那一根变成 0（Basis ＝ 1100 − 0）
	//	DiffFwd    早段价格被抬高 1100
	//
	// ⇒ 「给 Factor 兜底」根本不够：`Basis` 同样被毒到。而 **0 是一个看起来正常的价格**（结算价那一格记过同族），
	// 它会变成一整条被清零或被抬高的序列，**全程不报错**。⇒ 在算这次换月时就拒。
	ErrRollPriceUnusable = errors.New("continuous: 换月这一刻的价格用不了（0 或非有限），拒绝据此算基差与复权因子")
)

// DayBars 是**某一个交易日**递给本包的全部东西：那天在市的候选，以及它们各自的那一根。
//
// ⚠️ 「那天在市」由调用方按【库里有没有根】决定 —— 本包不问日历（§十五「自指环」）。
type DayBars struct {
	Day   tickflow.TradingDay
	Cands []ContractDay
	// Bars 是这一天每个合约的那一根，按合约取。
	Bars map[tickflow.Symbol]tickflow.Bar
}

// CountingRule 是**会自述「我数到了哪一段」**的换月规则。
//
// ⚠️ 它是可选的：`RollRule` 只负责选，而按天数的规则（`FixedBarDaysBeforeExpiry`）多一层义务 ——
// 把它数到的那一段交出来，让下游查得出「约定被违反了」（`CountedSpan` 的注释里写了为什么）。
type CountingRule interface {
	RollRule
	// Counted 交出**最近一次 Pick** 数到的那一段。
	Counted() CountedSpan
}

// AxisAware 是**要整条天轴**的换月规则（按天数判的那一类）。
//
// ⚠️ Build 在开始时把本次的天轴递给它。这**不违反**「只收数据不收提供者」：递过去的是一条已经在手的切片，
// 不是一个能去取数的东西。
type AxisAware interface {
	SetAxis(days []tickflow.TradingDay)
}

// Build 按 spec 把每天的候选拼成一条主连序列。
//
// 返回的 `Continuous` 里：
//
//	Bars   每天选中那个合约的那一根，按 Adjust 复权之后的价格（Volume / OpenInterest 不动）
//	Rolls  每一个换月点（含基差、复权因子，以及规则数到的那一段）
//	Days   本次用的天轴 —— 就是 in 里的那些天，升序
//
// ⛔ 三条不做的，写在这儿而不是留给读者去发现：
//
//	不补缺口   in 里缺的那一天，序列里也缺；补缺口是 Syncer/Gap 那一层的事
//	不问日历   「那天算不算交易日」由 in 自己回答
//	不排序     顺序错就拒（ErrDaysNotAscending）
func Build(spec ContinuousSpec, in []DayBars) (Continuous, error) {
	var out Continuous
	if spec.Roll == nil {
		return out, errors.New("continuous: ContinuousSpec.Roll 是 nil —— 没有换月规则就没有主连")
	}
	switch spec.Adjust {
	case RatioBack, DiffBack, NoAdjust:
	case RatioFwd, DiffFwd:
		out.RewritesHistory = "前复权以最新价为基准：下一次换月之后，这条序列的全部历史价格都会变 ⇒ 同一段历史今天跑与换月后跑结果不同；要可复现请用后复权"
	default:
		// 不拒的话，它在 adjust 里一支都不命中 ⇒ 静默变成不复权
		return out, fmt.Errorf("continuous: ContinuousSpec.Adjust ＝ %d 不是已知的复权方式", int(spec.Adjust))
	}
	if err := checkAscending(in); err != nil {
		return out, err
	}

	// 天轴先算出来递给规则 —— 「离到期还有几个有根的日子」问的是**未来**，
	// 而一个只看过去的规则回答不了它（rules.go 里那段红字记着这次撞车）。
	days := make([]tickflow.TradingDay, 0, len(in))
	for _, d := range in {
		days = append(days, d.Day)
	}
	if ar, ok := spec.Roll.(AxisAware); ok {
		ar.SetAxis(days)
	}

	var prev tickflow.Symbol
	var rawPrev float64 // 上一天那一根的收盘价（未复权），算基差用
	for _, d := range in {
		out.Days = append(out.Days, d.Day)
		sym, ok := spec.Roll.Pick(d.Day, d.Cands)
		if !ok {
			// 规则说这一天没有主力（候选为空，或 ByOIAndVolume 那种「说不清就不猜」）：
			// 这一天不产出根，也不算换月，**而它要留声** —— 否则规则造成的空洞与「那天真没数据」同形，
			// 下游（derived）会把前者读成后者（v0.6 勘误三那一族）。
			// ⚠️ 这一天**仍在天轴上** —— 天轴记的是「库里有这一天」，不是「这一天有主力」。
			out.NoPick = append(out.NoPick, d.Day)
			continue
		}
		bar, has := d.Bars[sym]
		if !has {
			return out, fmt.Errorf("%w：%s 的 %s", ErrNoBarForPick, d.Day, sym)
		}
		if prev != (tickflow.Symbol{}) && sym != prev {
			roll := Roll{Day: d.Day, From: prev, To: sym}
			// 基差 ＝ 新合约这一天的收盘 − 旧合约这一天的收盘；
			// ⚠️ 旧合约这一天**未必还有根**（它可能已经停了）⇒ 那时退回「与前一天的收盘比」。
			// 这两种取法**不是同一件事**，所以 Basis 的含义要按哪一种取到写下来（见下面那一行注释）。
			oldClose := rawPrev
			if ob, ok := d.Bars[prev]; ok {
				oldClose = ob.Close
			}
			if !usablePrice(oldClose) || !usablePrice(bar.Close) {
				return out, fmt.Errorf("%w：%s 从 %s 换到 %s，旧收盘 %v 新收盘 %v",
					ErrRollPriceUnusable, d.Day, prev, sym, oldClose, bar.Close)
			}
			roll.Basis = bar.Close - oldClose
			roll.Factor = bar.Close / oldClose
			if cr, ok := spec.Roll.(CountingRule); ok {
				roll.Counted = cr.Counted()
			}
			out.Rolls = append(out.Rolls, roll)
		}
		if len(out.Bars) == 0 {
			out.First = sym // 第一段的合约：没有它，一条不换月的序列说不出自己拼的是谁
		}
		out.Bars = append(out.Bars, bar)
		out.RawClose = append(out.RawClose, bar.Close) // 复权之前记下（adjust 只改 Bars）
		prev, rawPrev = sym, bar.Close
	}
	adjust(&out, spec.Adjust)
	return out, nil
}

// usablePrice 判「这个价格能不能拿来算基差与复权因子」：非 0、且有限。
func usablePrice(v float64) bool {
	return v != 0 && !math.IsNaN(v) && !math.IsInf(v, 0)
}

func checkAscending(in []DayBars) error {
	for i := 1; i < len(in); i++ {
		if in[i].Day <= in[i-1].Day {
			return fmt.Errorf("%w：第 %d 天是 %s，而上一天是 %s", ErrDaysNotAscending, i, in[i].Day, in[i-1].Day)
		}
	}
	return nil
}

// adjust 按复权方式改价格。
//
// ⛔ **只动价格，不动 Volume 与 OpenInterest**（§八「三条容易踩的」之二）：量是手数，没有复权的含义。
// ⛔ **复权后的价格不是可成交价格**（§八 之三）：拿它算手续费、保证金、涨跌停一律是错的。
//
// 两族四种：
//
//	后复权  以**最早**为基准：换月之后的那一段被调整 ⇒ 历史一经生成就不再变（可复现）
//	前复权  以**最新**为基准：换月之前的那一段被调整 ⇒ **每换一次月，全部历史都会变**
func adjust(c *Continuous, m AdjustMethod) {
	if m == NoAdjust || len(c.Rolls) == 0 || len(c.Bars) == 0 {
		return
	}
	// 每一根属于哪一段：换月日起算新的一段。
	segOf := make([]int, len(c.Bars))
	seg, ri := 0, 0
	for i, b := range c.Bars {
		for ri < len(c.Rolls) && c.Rolls[ri].Day <= b.TradingDay {
			seg++
			ri++
		}
		segOf[i] = seg
	}
	n := len(c.Rolls)
	// 每一段相对基准段要乘/加的量。
	mul := make([]float64, n+1)
	add := make([]float64, n+1)
	for i := range mul {
		mul[i] = 1
	}
	switch m {
	case RatioBack, DiffBack:
		// 基准是第 0 段（最早）⇒ 第 k 段要抵掉它之前每一次换月带来的跳。
		//
		// ⛔ 这里**没有**「Factor 为 0 就不乘」那种兜底了：源头（Build）已经拒掉用不了的价格
		// （`ErrRollPriceUnusable`）⇒ 留着兜底反而让人以为这一格被处理过，而它只在四支里的一支上存在。
		for k := 1; k <= n; k++ {
			r := c.Rolls[k-1]
			if m == RatioBack {
				mul[k] = mul[k-1] / r.Factor
			} else {
				add[k] = add[k-1] - r.Basis
			}
		}
	case RatioFwd, DiffFwd:
		// 基准是最后一段（最新）⇒ 第 k 段要加上它之后每一次换月带来的跳。
		for k := n - 1; k >= 0; k-- {
			r := c.Rolls[k]
			if m == RatioFwd {
				mul[k] = mul[k+1] * r.Factor
			} else {
				add[k] = add[k+1] + r.Basis
			}
		}
	}
	for i := range c.Bars {
		k := segOf[i]
		b := &c.Bars[i]
		b.Open = b.Open*mul[k] + add[k]
		b.High = b.High*mul[k] + add[k]
		b.Low = b.Low*mul[k] + add[k]
		b.Close = b.Close*mul[k] + add[k]
		// ⚠️ Volume / OpenInterest / Turnover 一律不动 —— 有测试钉着。
	}
}

// ContractAt 交出**第 i 根属于哪个合约**。
//
// ⛔ 它存在的理由：`tickflow.Bar` 里没有 Symbol（按合约分文件存）⇒ 拼好的序列自己说不出这件事。
// 上一版把这句话写在 `First` 的注释里（「由 First 与 Rolls 推出来」）—— 而**一句能推的注释不会自己变红**：
// 下一个人改了 `Rolls` 的语义，那句话静静变假（评审方 2026-09-16 指出，我认）。
// ⇒ 把它变成一个**会被测试钉住的方法**：推法只有这一处，谁改了 Rolls 就会在这里当场对不上。
//
// ⚠️ 射程：i 越界或序列为空 ⇒ 返回零值与 false；
// 它按**交易日**找那一段（`Rolls[k].Day` 是换月发生的那一天，那一天起属于 `Rolls[k].To`）。
func (c Continuous) ContractAt(i int) (tickflow.Symbol, bool) {
	if i < 0 || i >= len(c.Bars) {
		return tickflow.Symbol{}, false
	}
	sym := c.First
	for _, r := range c.Rolls {
		if r.Day <= c.Bars[i].TradingDay {
			sym = r.To
			continue
		}
		break
	}
	return sym, sym != tickflow.Symbol{}
}
