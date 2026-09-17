package main

import (
	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/derived"
)

// 退出码（design.md §十五「『还没定』乙的回答」第四节）。
//
// ⛔ 本工具**必须编译后跑**：go run 会把 2/3 压成 1（评审方 E1，复现过）。
const (
	exitOK       = 0 // 跑完 · 前提过 · 比对窗口里至少一天判出 a/b/c · 差异 0 条
	exitFailed   = 1 // 没跑完：前提不过 · 窗口错位 · 读库/解析失败
	exitDiffs    = 2 // 跑完 · 前提过 · 差异 ≥ 1 条 —— 要人看，不是错误
	exitNoJudged = 3 // 跑完 · 前提过 · 比对窗口里一天都没判出 a/b/c —— 「差异 0 条」是空的
)

// decideExit 从判据报告与差异清单定退出码（只在「跑完、前提过」之后调用）。
//
// ⛔ **2 优先于 3**，这是写死的决定，不是 if 的顺序碰出来的（评审方 2026-09-17 指出 2 与 3 会重叠）：
// 「差异 ≥ 1 而比对窗口里 a/b/c ＝ 0」能走到 —— 窗口里全是无结论、而 base 认的某一天报告里一根都没有 ⇒
// Compare 交出一条 BaseTradingDayNoObservation，它不需要任何 a/b/c。
// 理由：有差异就有东西要人看；而码 3 想防的是「空的 0」，这里本来就不是 0。
//
// baseLo / baseHi 是 base 快照的 [最早, 最晚]：「判出结论的天」只数比对窗口里的（design.md 那一节 E2）。
func decideExit(rep derived.Report, diffs []derived.Diff, baseLo, baseHi tickflow.TradingDay) int {
	if len(diffs) > 0 {
		return exitDiffs // ⛔ 先于「判出结论的天为 0」：见上
	}
	judged := 0
	for _, dv := range rep.Days {
		if dv.Day < baseLo || dv.Day > baseHi {
			continue
		}
		switch dv.Verdict {
		case derived.NightTraded, derived.NightZeroVolume, derived.NightAbsent:
			judged++
		}
	}
	if judged == 0 {
		return exitNoJudged
	}
	return exitOK
}
