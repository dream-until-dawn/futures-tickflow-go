package derived

import (
	"errors"
	"fmt"
	"sort"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
)

// —— 差异清单：判据的结论 对 base 日历摊平出来的快照 ——
//
// 对应 design.md §十五「derived 的读数计划」第零段：**只报与 base 的差异，不自己给一份日历**。
// ⛔ 产物不回灌（开工条件二的回答，四格之三）：差异清单交给人看，**不许**被自动写回日历或库。
//
// base 以**摊平的快照**进来（「承载体：摊平」那一节）：本包不收 Calendar 提供者，调用方替 base 答完这一段、递数据进来。

// BaseDay 是 base 日历对某一个交易日的回答（摊平后）。
//
// ⚠️ 调用方怎么摊平（写在这儿，因为本包看不见 Calendar）：对窗口里 base 认的每一个交易日 T 取 `DayOf(品种, T)`，
// Night ＝ 首段起点（Sessions[0].Start，CST 墙钟）落在 20:00 之后或 04:00 之前 —— 与 NightObsOf 的判据同一把尺子。
// 窗口里 base **不认**的日子不出现在快照里。
type BaseDay struct {
	Day   tickflow.TradingDay
	Night bool // base 说交易日 T 有夜盘
}

// DiffKind 是差异的种类。⛔ 零值 DiffUnset 不合法。
//
// 分两维（读数计划第零段）：「这一天有没有夜盘」与「这一天是不是交易日」。
type DiffKind int

const (
	// DiffUnset 是零值：没填。
	DiffUnset DiffKind = iota
	// BaseNightObservedAbsent：base 说有夜盘，观测没有夜盘根（c）。6.30 那 6 天就是这一种。
	BaseNightObservedAbsent
	// BaseNightObservedZeroVolume：base 说有夜盘，观测有夜盘根而量 0（b）。
	// ⚠️ 与上一种分开：b 可能是「开着而没人成交」，也可能是源侧占位根 —— 处置不同。
	BaseNightObservedZeroVolume
	// BaseNoNightObservedTraded：base 说没有夜盘，观测夜盘有量（a）。
	BaseNoNightObservedTraded
	// BaseTradingDayNoObservation：base 说是交易日，而那一天**一根都没观测到、库侧也没点名有洞** ——
	// 读数计划里「这一天没有根」的第三种来源（真没交易）只可能落在这一格；前两种（NoPick、库侧没拉到）已经在报告里成了无结论。
	BaseTradingDayNoObservation
	// ObservedNotBaseTradingDay：观测那一天有结论（有根），而 base 不认它是交易日。
	ObservedNotBaseTradingDay
)

// String 交出种类的名字，给报文用。
func (k DiffKind) String() string {
	switch k {
	case DiffUnset:
		return "未填"
	case BaseNightObservedAbsent:
		return "base 有夜盘 · 观测没有夜盘根"
	case BaseNightObservedZeroVolume:
		return "base 有夜盘 · 观测有夜盘根而量 0"
	case BaseNoNightObservedTraded:
		return "base 无夜盘 · 观测夜盘有量"
	case BaseTradingDayNoObservation:
		return "base 是交易日 · 观测一根都没有（库侧也没点名有洞）"
	case ObservedNotBaseTradingDay:
		return "观测有根 · base 不认是交易日"
	}
	return fmt.Sprintf("DiffKind(%d)", int(k))
}

// Diff 是差异清单里的一条。
type Diff struct {
	Day     tickflow.TradingDay
	Kind    DiffKind
	Verdict Verdict // 观测那一侧的结论；BaseTradingDayNoObservation 时为 VerdictUnset（那一天没有结论可言）
}

var (
	// ErrBaseDuplicate 是快照里同一天出现两次。
	ErrBaseDuplicate = errors.New("derived: base 快照里同一个交易日出现两次")
	// ErrBaseDayInvalid 是快照里有不合法的交易日。
	ErrBaseDayInvalid = errors.New("derived: base 快照里有不合法的交易日")
	// ErrBaseEmpty 是快照为空 —— 空快照下「差异为 0」没有意义。
	ErrBaseEmpty = errors.New("derived: base 快照为空")
)

// Compare 拿判据的结论去对 base 的快照，交出差异清单（按日期升序）。
//
// 规则：
//
//	一  只比 base 快照的窗口 [最早一天, 最晚一天]；窗口外的结论不参与（调用方的窗口是它自己选的）
//	二  无结论的日子**不是差异**：它们已经在 Report 里逐天列出（原因在那边），这里不重复、也不许当成「没有差异」读
//	三  a/b/c 对 base 的夜盘：c 或 b 而 base 有夜盘 ⇒ 差异；a 而 base 无夜盘 ⇒ 差异；其余一致
//	四  交易日这一维：base 认而报告里没有这一天 ⇒ BaseTradingDayNoObservation；报告里有结论而 base 不认 ⇒ ObservedNotBaseTradingDay
func Compare(rep Report, base []BaseDay) ([]Diff, error) {
	if len(base) == 0 {
		return nil, ErrBaseEmpty
	}
	bm := map[tickflow.TradingDay]BaseDay{}
	lo, hi := base[0].Day, base[0].Day
	for _, b := range base {
		if !b.Day.Valid() {
			return nil, fmt.Errorf("%w：%d", ErrBaseDayInvalid, int32(b.Day))
		}
		if _, dup := bm[b.Day]; dup {
			return nil, fmt.Errorf("%w：%s", ErrBaseDuplicate, b.Day)
		}
		bm[b.Day] = b
		if b.Day < lo {
			lo = b.Day
		}
		if b.Day > hi {
			hi = b.Day
		}
	}

	var out []Diff
	seen := map[tickflow.TradingDay]bool{}
	for _, dv := range rep.Days {
		if dv.Day < lo || dv.Day > hi {
			continue
		}
		seen[dv.Day] = true
		if dv.Verdict == NoVerdict {
			continue
		}
		b, isBase := bm[dv.Day]
		switch {
		case !isBase:
			out = append(out, Diff{Day: dv.Day, Kind: ObservedNotBaseTradingDay, Verdict: dv.Verdict})
		case b.Night && dv.Verdict == NightAbsent:
			out = append(out, Diff{Day: dv.Day, Kind: BaseNightObservedAbsent, Verdict: dv.Verdict})
		case b.Night && dv.Verdict == NightZeroVolume:
			out = append(out, Diff{Day: dv.Day, Kind: BaseNightObservedZeroVolume, Verdict: dv.Verdict})
		case !b.Night && dv.Verdict == NightTraded:
			out = append(out, Diff{Day: dv.Day, Kind: BaseNoNightObservedTraded, Verdict: dv.Verdict})
		}
	}
	for d := range bm {
		if !seen[d] {
			out = append(out, Diff{Day: d, Kind: BaseTradingDayNoObservation})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Day < out[j].Day })
	return out, nil
}
