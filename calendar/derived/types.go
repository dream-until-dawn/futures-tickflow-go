// Package derived 是「从行情观测里反推日历差异」的类型层。对应 docs/design.md §十五
// 「derived 的读数计划」与「开工条件二的回答」。
//
// ⛔ **这一片只落类型与两道守卫，不落判据**：与 continuous 第一颗同一个形状 ——
// 约束要在**有代码可违反之前**立住，否则第一处违反会先于守卫进来。
//
// —— ⛔ 本包的硬约束：**只读不写**（开工条件二的回答） ——
//
//	产物   只交出每个交易日的结论（DayVerdict）与差异清单，**不自己给一份日历**
//	输入   只收**数据**（摊平的快照、切片、结构体），不收 Store / Calendar / Source 这类提供者
//	写     **从不往库里写**：「洞是永久的」是库的缺陷，另立登记，不借本包去补
//	回灌   本包的产物**不许自动**写回日历或库 —— 要吃回去，是另一个决定、另一个入口、要人拍板
//
// ⇒ 承载体是两道守卫（`readonly_test.go`），不是这段注释：
//
//	签名层  导出函数的参数里不许出现提供者（tickflow.Store / Calendar / Source / SourceFactory ·
//	        store/ 与 source/ 下任何包的类型 · 本包声明的接口 · 字面量接口与 any）
//	调用层  非测试源码里不许出现写库的方法名（AppendBars · CommitSpan · DiscardCoverage）
//
// ⚠️ 两道加起来仍然**守不住**两格，写在这儿免得有人以为「只读」被机械地保证了：
// ① 调用方在外面写好库、再把结果递进来 —— 那不是本包写的，本包也看不见；
// ② 经一个名字不同的接口、或一个藏着提供者字段的具体类型转手去写。
package derived

import (
	"errors"
	"fmt"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
)

// Verdict 是某一个交易日在「那一夜有没有夜盘」这一维上的结论。
//
// 三档照 probe.md 6.21（a/b/c），外加第四种「无结论」。
//
// ⛔ 零值 VerdictUnset **不合法**：没填与「判过了」必须分得开 —— 一个默认成 NightTraded 的零值，
// 会把「忘了判」静默读成「那一夜开着」。
type Verdict int

const (
	// VerdictUnset 是零值：没填。任何交出去的 DayVerdict 都不许是它。
	VerdictUnset Verdict = iota
	// NightTraded 是 a 档：有夜盘根且夜盘量合计 > 0。
	NightTraded
	// NightZeroVolume 是 b 档：有夜盘根而夜盘量合计为 0。
	NightZeroVolume
	// NightAbsent 是 c 档：没有夜盘根。
	NightAbsent
	// NoVerdict 是「无结论」：这一天判不了，**必须**带原因（NoVerdictReason）。
	//
	// ⛔ 它不许被跳过不提：跳过不提 ＝ 让读的人以为那一天判过了（开工条件二的回答，四格之一）。
	NoVerdict
)

// String 交出档位的名字，给报文用。
func (v Verdict) String() string {
	switch v {
	case VerdictUnset:
		return "未填"
	case NightTraded:
		return "a（夜盘有量）"
	case NightZeroVolume:
		return "b（有夜盘根而量 0）"
	case NightAbsent:
		return "c（没有夜盘根）"
	case NoVerdict:
		return "无结论"
	}
	return fmt.Sprintf("Verdict(%d)", int(v))
}

// NoVerdictReason 是「无结论」的原因。
//
// ⛔ 三种原因**处置不同**，所以不许合成一个：
//
//	ReasonHeldBack      暂时的：下次同步会重拉那几天，来了就落盘，过了年龄上限仍没有就登记成「拉过确认没有」⇒ 不用处置
//	ReasonNeverFetched  永久的：库不会自己补（洞是永久的）⇒ 要么接受，要么删文件从早到晚重拉
//	ReasonNoPick        规则那一天说不清主力（持仓最大与成交最大不是同一个）⇒ 与库无关，重拉也没用
//
// 判两个东西该不该共用一个名字，看处置分不分岔 —— 这三种在「这一天没有读数」上长得一模一样，而处置三分。
type NoVerdictReason int

const (
	// ReasonNone 是零值：只允许出现在「判过了」的那几档上。
	ReasonNone NoVerdictReason = iota
	// ReasonHeldBack 见类型注释。
	ReasonHeldBack
	// ReasonNeverFetched 见类型注释。
	ReasonNeverFetched
	// ReasonNoPick 见类型注释。
	ReasonNoPick
)

// String 交出原因的名字，给报文用。
func (r NoVerdictReason) String() string {
	switch r {
	case ReasonNone:
		return "无"
	case ReasonHeldBack:
		return "库侧挂起（暂时）"
	case ReasonNeverFetched:
		return "库侧没拉过（永久洞）"
	case ReasonNoPick:
		return "规则没给出主力"
	}
	return fmt.Sprintf("NoVerdictReason(%d)", int(r))
}

// DayVerdict 是某一个交易日的结论。只能经 NewDayVerdict 造出合法值。
type DayVerdict struct {
	Day     tickflow.TradingDay
	Verdict Verdict
	// Reason 只在 Verdict ＝ NoVerdict 时非零；其余档位必须是 ReasonNone。
	Reason NoVerdictReason
}

var (
	// ErrVerdictUnset 是交出了零值档位。
	ErrVerdictUnset = errors.New("derived: 结论没填（VerdictUnset）")
	// ErrReasonMismatch 是档位与原因对不上：无结论却没带原因，或判过了却带着原因。
	ErrReasonMismatch = errors.New("derived: 结论与原因对不上")
	// ErrDayInvalid 是交易日不合法。
	ErrDayInvalid = errors.New("derived: 交易日不合法")
)

// NewDayVerdict 造一个结论，并当场核三条：
//
//	一  交易日合法
//	二  档位不是零值
//	三  「无结论」⇔「带原因」：无结论必须带原因，判过了的档位不许带原因
//
// ⚠️ 第三条的两个方向都要拒：只拒「无结论没原因」的话，一个带着 ReasonNeverFetched 的 NightTraded
// 会让读的人不知道该信档位还是信原因。
func NewDayVerdict(day tickflow.TradingDay, v Verdict, r NoVerdictReason) (DayVerdict, error) {
	if !day.Valid() {
		return DayVerdict{}, fmt.Errorf("%w：%d", ErrDayInvalid, int32(day))
	}
	switch v {
	case VerdictUnset:
		return DayVerdict{}, fmt.Errorf("%w：%s", ErrVerdictUnset, day)
	case NightTraded, NightZeroVolume, NightAbsent:
		if r != ReasonNone {
			return DayVerdict{}, fmt.Errorf("%w：%s 判成 %s，却带着原因「%s」", ErrReasonMismatch, day, v, r)
		}
	case NoVerdict:
		if r == ReasonNone {
			return DayVerdict{}, fmt.Errorf("%w：%s 无结论却没带原因 —— 三种原因处置不同，不许省略", ErrReasonMismatch, day)
		}
		if r != ReasonHeldBack && r != ReasonNeverFetched && r != ReasonNoPick {
			return DayVerdict{}, fmt.Errorf("%w：%s 的原因 %s 不在已知的三种里", ErrReasonMismatch, day, r)
		}
	default:
		return DayVerdict{}, fmt.Errorf("%w：%s 的档位 %s 不在已知的五种里", ErrReasonMismatch, day, v)
	}
	return DayVerdict{Day: day, Verdict: v, Reason: r}, nil
}
