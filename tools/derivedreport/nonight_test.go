package main

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/derived"
)

// v0.11 Q-f：-nonight 停夜盘名单（design.md v0.11 庚）。

// c630Sorted 是 6.30 那 6 个 c 日（节后首日），升序。
func c630Sorted() []tickflow.TradingDay {
	var out []tickflow.TradingDay
	for d := range c630 {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// neighbour 交出 days 里 d 的前一个（step −1）或后一个（step +1）交易日。
func neighbour(t *testing.T, days []tickflow.TradingDay, d tickflow.TradingDay, step int) tickflow.TradingDay {
	t.Helper()
	for i, x := range days {
		if x == d {
			if j := i + step; j >= 0 && j < len(days) {
				return days[j]
			}
		}
	}
	t.Fatalf("交易日表里找不到 %s 的邻居（step %d）", d, step)
	return 0
}

func runNonight(t *testing.T, root, daysPath, spansPath string, extra ...string) (int, string) {
	t.Helper()
	args := append([]string{"-product", "SHFE.rb", "-from", "20250915", "-to", "20260911", "-store", root, "-days", daysPath, "-spans", spansPath}, extra...)
	return runBin(t, args...)
}

// 判据一 ＋ 四 ＋ 五 —— 6.30 同形库：名单写那 6 个 c 日各自的前一个交易日（公告日期）⇒ 差异 0 条、退出 0，报文头逐个印「X → D」；
// 同一个库不给名单 ⇒ 仍是那 6 条、退出 2，头上印「未给」（零点不动）；跑前跑后库与三份输入文件（含名单）md5 不变。
func TestNoNightListClearsTable630Diffs(t *testing.T) {
	root, daysPath, spansPath := build630Library(t)
	days := table630Days(t)
	var body strings.Builder
	var xs []tickflow.TradingDay
	for _, d := range c630Sorted() {
		x := neighbour(t, days, d, -1)
		xs = append(xs, x)
		fmt.Fprintf(&body, "%d\n", int(x))
	}
	nnPath := writeFile(t, filepath.Join(filepath.Dir(daysPath), "nonight.txt"), "# 公告日期\n"+body.String())

	code, out := runNonight(t, root, daysPath, spansPath)
	if code != exitDiffs || !strings.Contains(out, "== 差异（6 条） ==") || !strings.Contains(out, "停夜盘名单：未给") {
		t.Fatalf("不给名单（零点）：退出码 %d，要 %d、6 条差异、头上「未给」\n%s", code, exitDiffs, out)
	}
	if !strings.Contains(out, "安排写着那一晚停 ⇒ 把公告日期（节前最后一个交易日）写进 -nonight 名单重跑") {
		t.Errorf("「base 有夜盘 · 观测 c」的提示没指向 -nonight：\n%s", out)
	}

	before := treeMD5(t, root, daysPath, spansPath, nnPath)
	code, out = runNonight(t, root, daysPath, spansPath, "-nonight", nnPath)
	if after := treeMD5(t, root, daysPath, spansPath, nnPath); before != after {
		t.Errorf("只读被破坏：跑前 md5 %s，跑后 %s", before, after)
	}
	if code != exitOK {
		t.Fatalf("给了名单：退出码 %d，要 %d\n%s", code, exitOK, out)
	}
	if !strings.Contains(out, "== 差异（0 条） ==") || !strings.Contains(out, fmt.Sprintf("停夜盘名单 %s（6 天 · md5 ", nnPath)) {
		t.Errorf("报文不对：\n%s", out)
	}
	for i, d := range c630Sorted() {
		if want := fmt.Sprintf("  %s → 去掉的是 %s 那一天的夜盘", xs[i], d); !strings.Contains(out, want) {
			t.Errorf("报文头缺「%s」：\n%s", want, out)
		}
	}
}

// 判据二 —— 名单抄成节后首日（M2 那种错）⇒ 那 6 条 c 仍在，另多出 6 条「base 无夜盘 · 观测夜盘有量」落在各自的下一个交易日，
// 且这 6 条的提示是「多半把节后首日当成了公告日期」。
func TestNoNightListMisCopiedAsPostHolidayDay(t *testing.T) {
	root, daysPath, spansPath := build630Library(t)
	days := table630Days(t)
	var body strings.Builder
	for _, d := range c630Sorted() {
		fmt.Fprintf(&body, "%d\n", int(d))
	}
	nnPath := writeFile(t, filepath.Join(filepath.Dir(daysPath), "nonight_wrong.txt"), body.String())
	code, out := runNonight(t, root, daysPath, spansPath, "-nonight", nnPath)
	if code != exitDiffs || !strings.Contains(out, "== 差异（12 条） ==") {
		t.Fatalf("退出码 %d，要 %d 且 12 条差异\n%s", code, exitDiffs, out)
	}
	for _, d := range c630Sorted() {
		if !strings.Contains(out, d.String()+" · base 有夜盘 · 观测没有夜盘根") {
			t.Errorf("c 日 %s 那条应仍在：\n%s", d, out)
		}
		next := neighbour(t, days, d, +1)
		want := fmt.Sprintf("%s · base 无夜盘 · 观测夜盘有量 · 观测 a（夜盘有量） · 先查：名单说 %s 晚上停，而观测在这一天有夜盘根 ⇒ 先核名单那一行：多半把节后首日当成了公告日期", next, d)
		if !strings.Contains(out, want) {
			t.Errorf("缺「%s」：\n%s", want, out)
		}
	}
}

// 判据二的对照 —— 提示只在【名单去掉了夜盘的那一天】上换；别的天上的「base 无夜盘」两种、以及别的种类，仍是处置表原文。
func TestNoNightHintOnlyOnSuppressedDays(t *testing.T) {
	suppressed := map[tickflow.TradingDay]tickflow.TradingDay{20261008: 20260930}
	for _, c := range []struct {
		d    derived.Diff
		want string
	}{
		{derived.Diff{Day: 20261008, Kind: derived.BaseNoNightObservedTraded}, fmt.Sprintf(nonightHint, tickflow.TradingDay(20260930))},
		{derived.Diff{Day: 20261008, Kind: derived.BaseNoNightObservedZeroVolume}, fmt.Sprintf(nonightHint, tickflow.TradingDay(20260930))},
		{derived.Diff{Day: 20261009, Kind: derived.BaseNoNightObservedTraded}, disposalHint[derived.BaseNoNightObservedTraded]},
		{derived.Diff{Day: 20261009, Kind: derived.BaseNoNightObservedZeroVolume}, disposalHint[derived.BaseNoNightObservedZeroVolume]},
		{derived.Diff{Day: 20261008, Kind: derived.BaseTradingDayNoObservation}, disposalHint[derived.BaseTradingDayNoObservation]},
	} {
		if got := hintFor(c.d, suppressed); got != c.want {
			t.Errorf("%s %s：提示「%s」，应为「%s」", c.d.Day, c.d.Kind, got, c.want)
		}
	}
	if disposalHint[derived.BaseNoNightObservedTraded] == fmt.Sprintf(nonightHint, tickflow.TradingDay(20260930)) {
		t.Fatal("标定：两句提示相同，上面几格分不出对错")
	}
}

// 判据三 —— 名单坏了 ⇒ 退出 1，在结构前提之前就停，并点名原因；名单写表里最后一天 ⇒ 不报错，头上印「影响的那一天不在交易日表里」。
func TestNoNightListRejects(t *testing.T) {
	root, daysPath, spansPath := build630Library(t)
	dir := filepath.Dir(daysPath)
	for _, c := range []struct{ name, body, want string }{
		{"不在交易日表（国庆当天）", "20251001\n", "停夜盘名单里的 2025-10-01 不在交易日表里"},
		{"乱序", "20251231\n20250930\n", "没有严格晚于上一行"},
		{"重复", "20250930\n20250930\n", "没有严格晚于上一行"},
		{"不是 YYYYMMDD", "2025-09-30\n", "不是 YYYYMMDD"},
		{"一行都没有", "# 只有注释\n", "一个交易日都没读到"},
	} {
		p := writeFile(t, filepath.Join(dir, "nn_bad.txt"), c.body)
		code, out := runNonight(t, root, daysPath, spansPath, "-nonight", p)
		if code != exitFailed || !strings.Contains(out, c.want) {
			t.Errorf("%s：退出码 %d（要 %d），报文要含「%s」\n%s", c.name, code, exitFailed, c.want, out)
		}
		if strings.Contains(out, "== 结构前提") {
			t.Errorf("%s：名单坏了却往下跑到了结构前提：\n%s", c.name, out)
		}
	}
	p := writeFile(t, filepath.Join(dir, "nn_last.txt"), "20260911\n")
	code, out := runNonight(t, root, daysPath, spansPath, "-nonight", p)
	if code != exitDiffs || !strings.Contains(out, "== 差异（6 条） ==") || !strings.Contains(out, "  2026-09-11 → 影响的那一天不在交易日表里（2026-09-11 是表里最后一天）") {
		t.Errorf("名单写表里最后一天：退出码 %d（要 %d、6 条、头上点明）\n%s", code, exitDiffs, out)
	}
}
