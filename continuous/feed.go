package continuous

import (
	"fmt"
	"math"
	"sort"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
)

// 本文件把拼好的主连交给 tickflow.Feed（v0.9 F-c）：
//
//	c.Walker()  拼好的序列当成一个 BarWalker（内存里、一段 coverage）—— Feed 的源
//	c           本身实现 tickflow.MainSource —— View 的四个主连方法（RawClose · Contract · IsRollDay · Basis）读它
//
// ⚠️ 射程：只有**日线**主连（Build 按天拼；日内主连 v0.9 不做，用户 2026-09-18 裁 U5）。

var _ tickflow.MainSource = Continuous{}

// MainAt 交出交易日 d 那一根的主连事实；序列里没有 d（NoPick 那几天、或天轴之外）⇒ false。
//
//	Contract  那一根属于哪个合约（与 ContractAt 同一推法）
//	RawClose  未复权收盘价（RawClose 字段）
//	RollDay   d 是不是某个换月点的那一天
//	Basis     换月那天的基差（Roll.Basis）；**不是换月日给 NaN**（不给 0：0 是一个看起来正常的基差）
func (c Continuous) MainAt(d tickflow.TradingDay) (tickflow.MainDay, bool) {
	i := sort.Search(len(c.Bars), func(i int) bool { return c.Bars[i].TradingDay >= d })
	if i >= len(c.Bars) || c.Bars[i].TradingDay != d {
		return tickflow.MainDay{}, false
	}
	sym, _ := c.ContractAt(i)
	out := tickflow.MainDay{Contract: sym, Basis: math.NaN(), RawClose: math.NaN()}
	if i < len(c.RawClose) {
		out.RawClose = c.RawClose[i]
	}
	for _, r := range c.Rolls {
		if r.Day == d {
			out.RollDay, out.Basis = true, r.Basis
		}
	}
	return out, true
}

// Walker 把拼好的序列（复权后的 Bars）交成一个 BarWalker，给 tickflow.NewFeed 当源。
//
// coverage 是一段：[Bars 首根的交易日, 末根的交易日]。NoPick 那几天不在 Bars 里 ⇒ 那几步 Feed 不步进（缺的是根，不是没拉过）。
// 数据在内存里、Build 已经核过 ⇒ Walk 没有「先核」可做，结论只可能是越段。
func (c Continuous) Walker() tickflow.BarWalker { return walker{c.Bars} }

type walker struct{ bars []tickflow.Bar }

func (w walker) Coverage() []tickflow.Span {
	if len(w.bars) == 0 {
		return nil
	}
	return []tickflow.Span{{From: w.bars[0].TradingDay, To: w.bars[len(w.bars)-1].TradingDay, Bars: len(w.bars), Days: len(w.bars)}}
}

func (w walker) Walk(from, to tickflow.TradingDay, fn func(tickflow.Bar) bool) error {
	if len(w.bars) == 0 || from < w.bars[0].TradingDay || to > w.bars[len(w.bars)-1].TradingDay {
		return fmt.Errorf("continuous: Walk [%s, %s] 不在主连的天轴里: %w", from, to, tickflow.ErrWalkOutsideCoverage)
	}
	for _, b := range w.bars {
		if b.TradingDay >= from && b.TradingDay <= to && !fn(b) {
			break // 契约「停」：只停回调；内存里没有剩下要核的，结论照给（nil）
		}
	}
	return nil
}
