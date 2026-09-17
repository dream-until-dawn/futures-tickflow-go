package main

import (
	"fmt"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/derived"
)

// —— 覆盖层：段内没覆盖的天按【相对 coverage 的位置】转成 Hole ——
//
// 评审方 2026-09-17 判（「乙的回答」的两层读法）：
//
//	结构层  段文件/库/窗口本身坏了 ⇒ 停，退出码 1（在 run 里）
//	覆盖层  段内没覆盖的天 **不算前提不过**，按位置转成 Hole 交给 Judge：
//	          最后一段 coverage 的 To 之后（或整份合约一段 coverage 都没有）⇒ 尾部未登记（挂起或还没同步到）⇒ 再同步一次
//	          两段 coverage 之间、或早于第一段的 From                   ⇒ 永久洞（库只往后长，补不进去）
//	        ⛔ **不按年龄分**：HeldBack 只活在那一次同步的报文里、不落库；年龄判的是那次同步的「现在」，不是工具运行时的「现在」
//	        ⇒ 工具不读、不复刻 sync.go 的 emptyTailGraceDays（两份真值会分岔）
//	例外    按 6.31 的规则放行：合约已到期、没覆盖的那一截整个在【最后交易日】之后、库里最后一根 ＝ 最后交易日 ⇒ 不算洞
//	        最后交易日取自交易所规则（只认得 SHFE.rb；01/02 月一律不适用 —— 春节月份可另行调整）

// covHole 是某份合约的一个没覆盖的天及其分类。
type covHole struct {
	sym    tickflow.Symbol
	day    tickflow.TradingDay
	reason derived.NoVerdictReason
}

// covLine 是报文里「覆盖层」那一段每份合约一行。
type covLine struct {
	sym          tickflow.Symbol
	sp           span
	covEnd       tickflow.TradingDay // 最后一段 coverage 的 To（0 ＝ 一段都没有）
	tail, holes  int                 // 尾部未登记几天 · 永久洞几天
	exempt       int                 // 按到期规则放行了几天
	lastTrading  tickflow.TradingDay // 规则算出的最后交易日（0 ＝ 规则不适用或算不出）
	lastBar      tickflow.TradingDay
	exemptReason string
}

// lastTradingDay 是交易所规则给出的【最后交易日】；0 ＝ 不知道（调用方不许放行）。
//
//	SHFE.rb  合约月份 15 日，遇非交易日顺延（顺延用注入表；原文只说「法定节假日」—— 周末顺延是推论，6.31 第四节）
//	         ⛔ 01/02 月一律返回 0：原文「春节月份等最后交易日交易所可另行调整并通知」
//	其余品种 返回 0（没有成文规则的读数，不猜）
func lastTradingDay(sym tickflow.Symbol, days []tickflow.TradingDay) tickflow.TradingDay {
	if sym.Exchange != tickflow.SHFE || sym.Product != "rb" {
		return 0
	}
	m := sym.YearMon % 100
	if m == 1 || m == 2 {
		return 0
	}
	d15 := tickflow.TradingDay((2000+sym.YearMon/100)*10000 + m*100 + 15)
	for _, d := range days {
		if d >= d15 {
			return d
		}
	}
	return 0
}

// classifyCoverage 对每份合约：段 ∩ 窗口里、注入表认的每个交易日，没落进任何一段 coverage 的 ⇒ 按位置分类。
func classifyCoverage(spans []span, lr libraryRead, days []tickflow.TradingDay, from, to tickflow.TradingDay) ([]covHole, []covLine) {
	var holes []covHole
	var lines []covLine
	for _, sp := range spans {
		cov := lr.coverage[sp.sym]
		ln := covLine{sym: sp.sym, sp: sp, lastBar: lr.lastBar[sp.sym], lastTrading: lastTradingDay(sp.sym, days)}
		var first tickflow.TradingDay
		if len(cov) > 0 {
			first, ln.covEnd = cov[0].From, cov[len(cov)-1].To
		}
		covered := func(d tickflow.TradingDay) bool {
			for _, c := range cov {
				if d >= c.From && d <= c.To {
					return true
				}
			}
			return false
		}
		for _, d := range days {
			if d < sp.from || d > sp.to || d < from || d > to || covered(d) {
				continue
			}
			// 例外：已到期、这一天在最后交易日之后、库里最后一根 ＝ 最后交易日
			if ln.lastTrading != 0 && d > ln.lastTrading && ln.lastBar == ln.lastTrading {
				ln.exempt++
				continue
			}
			var r derived.NoVerdictReason
			switch {
			case len(cov) == 0 || d > ln.covEnd:
				r = derived.ReasonTailUnregistered
				ln.tail++
			case d < first:
				r = derived.ReasonNeverFetched // 头部：早于第一段，补不进去
				ln.holes++
			default:
				r = derived.ReasonNeverFetched // 两段之间
				ln.holes++
			}
			holes = append(holes, covHole{sym: sp.sym, day: d, reason: r})
		}
		if ln.exempt > 0 {
			ln.exemptReason = fmt.Sprintf("已到期：最后交易日 %s（规则）＝ 库里最后一根 %s", ln.lastTrading, ln.lastBar)
		}
		lines = append(lines, ln)
	}
	return holes, lines
}

// uncoveredWindowDays 交出窗口里、注入表认的、却一个合约的段都没落到的交易日（结构前提：非空 ⇒ 段文件不完整 ⇒ 停）。
func uncoveredWindowDays(spans []span, days []tickflow.TradingDay, from, to tickflow.TradingDay) []tickflow.TradingDay {
	var out []tickflow.TradingDay
	for _, d := range days {
		if d < from || d > to {
			continue
		}
		in := false
		for _, sp := range spans {
			if d >= sp.from && d <= sp.to {
				in = true
				break
			}
		}
		if !in {
			out = append(out, d)
		}
	}
	return out
}
