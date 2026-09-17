package derived

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/continuous"
)

// —— 判据本体：按品种级主连逐天判「那一夜有没有夜盘」 ——
//
// 对应 design.md §十五「derived 的读数计划」与 probe.md 6.21 / 6.30。
//
// ⛔ 本文件只交出【每一天的结论】与一份「让蚕食看得见」的报数；**不与 base 日历比、不出差异清单** ——
// 差异清单要一份摊平的 base 快照做输入，那是下一颗。
//
// 每一天按下面的**先后次序**判，先命中的先定：
//
//	一  库侧有洞（Hole）          ⇒ 无结论，原因 ＝ 洞的种类（同一天两种洞都有 ⇒ 记【永久洞】：它需要人处置，尾部未登记再同步一次就行）
//	二  规则没给出主力（NoPick）   ⇒ 无结论，原因 ＝ NoPick
//	三  否则取那一天的主力合约，看它的夜盘观测：
//	      有夜盘根且量 > 0 ⇒ a · 有夜盘根而量 0 ⇒ b · 没有夜盘根 ⇒ c
//	    ⛔ 主力那一天**没有观测** ⇒ 报错，不许判成 c ——「没拉到」与「没开」在这里必须分得开（勘误三那一族）
//
// 洞排在 NoPick 前面：洞说的是「库侧没拉到」，那一天连候选集都不完整，主连选谁本身就不可信，
// 谈不上「规则说不清」。

// NightObs 是某个合约在某个交易日的夜盘观测。
type NightObs struct {
	Day         tickflow.TradingDay
	Symbol      tickflow.Symbol
	NightBars   int     // 开盘时刻落在 20:00 之后或次日 04:00 之前的根数
	NightVolume float64 // 那些根的成交量合计
}

// Hole 是库侧点名的一个「没有读数」的交易日。Reason 只许是 ReasonTailUnregistered 或 ReasonNeverFetched。
//
// ⚠️ 调用方负责把「每份合约的洞」摊平成「哪几天有洞」：哪些合约算数（例如到期之后那一截不算，probe.md 6.31）
// 是调用方的判断，本包不收库、也看不见合约的存续期。
type Hole struct {
	Day    tickflow.TradingDay
	Reason NoVerdictReason
}

// Input 是判据本体的全部输入 —— 三样都是**数据**，不是提供者。
type Input struct {
	// From / To 是调用方**问的那一段**（闭区间，交易日）—— 它取数、同步、拼主连用的就是这一段。
	//
	// ⛔ 必填，而且是 Report 的一部分：Report 若只带 Days，下游分不开「这一段里那天真没根」与「根本没问到那一天」——
	// 评审方 2026-09-17 的探针 W：报告只有 03-02、base 快照给 03-02…03-04 ⇒ 差异清单把 03-03/03-04 说成「那天没交易」，
	// 而那两天只是没被问到（base 取到今天、库只同步到昨天，就是这个形状）⇒ 本仓那条老病：报错指向数据，而真因在调用方。
	From, To tickflow.TradingDay

	Main   continuous.Continuous // 品种级主连（未复权，design.md 读数计划第②样）
	Nights []NightObs            // 各合约逐天的夜盘观测（至少要覆盖主力合约有根的每一天）
	Holes  []Hole                // 库侧的洞
}

// Report 是判据本体的产物：逐天结论 ＋ 「让蚕食看得见」的报数。
type Report struct {
	From, To tickflow.TradingDay // 被问的那一段（抄自 Input）—— Compare 据它判 base 快照有没有越界
	Days     []DayVerdict        // 升序，每个被判的交易日一条

	Traded, ZeroVolume, Absent     int // a · b · c
	NoVerdict                      int // 无结论合计
	TailUnregistered, NeverFetched int // 其中尾部未登记 · 永久洞
	NoPick                         int // 其中规则没给出主力

	// OthersHadNight 是【主连那天没有夜盘量，而同一天别的合约有】的那些天 —— 判据假阳的形状。
	// ⛔ 只记录、不改结论（6.21 证伪的是「按具体合约判」；品种级这条只是还没被证伪，而这是它可能垮的方向）。
	OthersHadNight []tickflow.TradingDay
}

var (
	// ErrNightObsMissing 是主力合约那一天没有夜盘观测。
	ErrNightObsMissing = errors.New("derived: 主力合约那一天没有夜盘观测")
	// ErrNightObsDuplicate 是同一合约同一天给了两条观测。
	ErrNightObsDuplicate = errors.New("derived: 同一合约同一天有两条夜盘观测")
	// ErrHoleReason 是洞的原因不是「尾部未登记」或「永久洞」。
	ErrHoleReason = errors.New("derived: 洞的原因只许是尾部未登记或永久洞")
	// ErrWindowInvalid 是被问的那一段没填或不合法（From/To 不合法，或 From 晚于 To）。
	ErrWindowInvalid = errors.New("derived: 被问的那一段（From/To）没填或不合法")
	// ErrOutsideWindow 是主连天轴或洞落在被问的那一段之外 —— 输入与调用方声明的窗口对不上。
	ErrOutsideWindow = errors.New("derived: 输入里有日子落在被问的那一段之外")
)

// NightObsOf 把一个合约的 1m 根按交易日聚成夜盘观测。
//
// 判据（probe.md 6.21，一字不改）：开盘时刻（`Bar.Ts`，CST 墙钟）落在 20:00 之后或次日 04:00 之前的根 ＝ 夜盘根。
// ⚠️ bars 里出现过的每一个交易日都交出一条 —— 那一天一根夜盘根都没有也交（NightBars ＝ 0），
// 因为「有日盘而无夜盘根」正是 c 档，它必须有一条观测才能被判成 c。
func NightObsOf(sym tickflow.Symbol, bars []tickflow.Bar) []NightObs {
	by := map[tickflow.TradingDay]*NightObs{}
	var order []tickflow.TradingDay
	for _, b := range bars {
		o := by[b.TradingDay]
		if o == nil {
			o = &NightObs{Day: b.TradingDay, Symbol: sym}
			by[b.TradingDay] = o
			order = append(order, b.TradingDay)
		}
		if h := time.UnixMilli(b.Ts).In(tickflow.CST).Hour(); h >= 20 || h < 4 {
			o.NightBars++
			o.NightVolume += b.Volume
		}
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })
	out := make([]NightObs, 0, len(order))
	for _, d := range order {
		out = append(out, *by[d])
	}
	return out
}

type obsKey struct {
	day tickflow.TradingDay
	sym tickflow.Symbol
}

// Judge 按品种级主连逐天判夜盘，交出逐天结论与报数。
func Judge(in Input) (Report, error) {
	if !in.From.Valid() || !in.To.Valid() || in.From > in.To {
		return Report{}, fmt.Errorf("%w：[%d, %d]", ErrWindowInvalid, int32(in.From), int32(in.To))
	}
	rep := Report{From: in.From, To: in.To}
	outside := func(d tickflow.TradingDay) bool { return d < in.From || d > in.To }

	obs := map[obsKey]NightObs{}
	nightByDay := map[tickflow.TradingDay][]NightObs{}
	for _, o := range in.Nights {
		k := obsKey{o.Day, o.Symbol}
		if _, dup := obs[k]; dup {
			return Report{}, fmt.Errorf("%w：%s %s", ErrNightObsDuplicate, o.Day, o.Symbol)
		}
		obs[k] = o
		nightByDay[o.Day] = append(nightByDay[o.Day], o)
	}

	holes := map[tickflow.TradingDay]NoVerdictReason{}
	for _, h := range in.Holes {
		if h.Reason != ReasonTailUnregistered && h.Reason != ReasonNeverFetched {
			return Report{}, fmt.Errorf("%w：%s 的原因是「%s」", ErrHoleReason, h.Day, h.Reason)
		}
		if holes[h.Day] != ReasonNeverFetched { // 两种都有 ⇒ 记永久洞
			holes[h.Day] = h.Reason
		}
	}

	noPick := map[tickflow.TradingDay]bool{}
	for _, d := range in.Main.NoPick {
		noPick[d] = true
	}
	// 主连每一根属于哪一天、哪个合约
	picked := map[tickflow.TradingDay]tickflow.Symbol{}
	for i, b := range in.Main.Bars {
		sym, ok := in.Main.ContractAt(i)
		if !ok {
			return Report{}, fmt.Errorf("derived: 主连第 %d 根说不出属于哪个合约 —— 序列是它自己交出来的，这是输入坏了", i)
		}
		picked[b.TradingDay] = sym
	}

	// 被判的天 ＝ 主连天轴 ∪ 洞所在的天（洞可能让那一天根本进不了天轴，而它仍要被逐天印出来）
	daySet := map[tickflow.TradingDay]bool{}
	for _, d := range in.Main.Days {
		if outside(d) {
			return Report{}, fmt.Errorf("%w：主连天轴上的 %s 不在 [%s, %s] 里", ErrOutsideWindow, d, in.From, in.To)
		}
		daySet[d] = true
	}
	for d := range holes {
		if outside(d) {
			return Report{}, fmt.Errorf("%w：洞 %s 不在 [%s, %s] 里", ErrOutsideWindow, d, in.From, in.To)
		}
		daySet[d] = true
	}
	days := make([]tickflow.TradingDay, 0, len(daySet))
	for d := range daySet {
		days = append(days, d)
	}
	sort.Slice(days, func(i, j int) bool { return days[i] < days[j] })

	for _, d := range days {
		var v Verdict
		var r NoVerdictReason
		switch hr, hole := holes[d]; {
		case hole:
			v, r = NoVerdict, hr
		case noPick[d]:
			v, r = NoVerdict, ReasonNoPick
		default:
			sym, ok := picked[d]
			if !ok {
				return Report{}, fmt.Errorf("derived: %s 在天轴上、不是 NoPick、也没有洞，却没有主连的根 —— 输入自相矛盾", d)
			}
			o, ok := obs[obsKey{d, sym}]
			if !ok {
				return Report{}, fmt.Errorf("%w：%s %s —— 不许判成 c：「没拉到」与「没开」要分得开", ErrNightObsMissing, d, sym)
			}
			switch {
			case o.NightBars > 0 && o.NightVolume > 0:
				v = NightTraded
			case o.NightBars > 0:
				v = NightZeroVolume
			default:
				v = NightAbsent
			}
			if v != NightTraded {
				// ⚠️ 下面的 `other.Symbol != sym` 在今天这条路径上是**死的**：走到这里主力自己的 NightVolume 必为 0，
				// 满足不了 `> 0`。实测（2026-09-17，本文件）：删掉这个子句，本包测试全绿 —— 与根包夹具 derived_night_tiers_test.go 里那个子句同形。
				// 它将来会活的那一刻：分档改成只看根数、不看量（上面 `&& o.NightVolume > 0` 那半句去掉）——那时要补一格会红的输入。
				for _, other := range nightByDay[d] {
					if other.Symbol != sym && other.NightVolume > 0 {
						rep.OthersHadNight = append(rep.OthersHadNight, d)
						break
					}
				}
			}
		}
		dv, err := NewDayVerdict(d, v, r)
		if err != nil {
			return Report{}, err
		}
		rep.Days = append(rep.Days, dv)
		switch v {
		case NightTraded:
			rep.Traded++
		case NightZeroVolume:
			rep.ZeroVolume++
		case NightAbsent:
			rep.Absent++
		case NoVerdict:
			rep.NoVerdict++
			switch r {
			case ReasonTailUnregistered:
				rep.TailUnregistered++
			case ReasonNeverFetched:
				rep.NeverFetched++
			case ReasonNoPick:
				rep.NoPick++
			}
		}
	}
	return rep, nil
}

// Summary 交出「让蚕食看得见」的那几行（design.md §十五「开工条件二的回答」：逐次印无结论天数与占比、其中各原因几天），
// 并**逐天**列出无结论的日子与原因 —— 不许跳过不提。
func (r Report) Summary() string {
	var b strings.Builder
	n := len(r.Days)
	fmt.Fprintf(&b, "判了 %d 天 · a（夜盘有量）%d · b（有根无量）%d · c（没有夜盘根）%d\n", n, r.Traded, r.ZeroVolume, r.Absent)
	pct := 0.0
	if n > 0 {
		pct = 100 * float64(r.NoVerdict) / float64(n)
	}
	fmt.Fprintf(&b, "无结论 %d 天（%.1f%%）：尾部未登记 %d · 永久洞 %d · 规则没给出主力 %d\n",
		r.NoVerdict, pct, r.TailUnregistered, r.NeverFetched, r.NoPick)
	fmt.Fprintf(&b, "主连无量而别的合约有量 %d 天 %v\n", len(r.OthersHadNight), r.OthersHadNight)
	for _, d := range r.Days {
		if d.Verdict == NoVerdict {
			fmt.Fprintf(&b, "  无结论 %s：%s\n", d.Day, d.Reason)
		}
	}
	return b.String()
}
