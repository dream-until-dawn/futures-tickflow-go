// derivedreport 是 design.md §十五「『还没定』乙的回答」里那个【显式调用】的工具：
// 读库 → 结构前提 → 覆盖层（没覆盖的天按位置转成洞）→ 拼主连 → Judge → Compare → 印报告。
//
// ⛔ 必须编译后跑（go build）：go run 会把退出码 2/3 压成 1（评审方 E1，复现过）。
// ⛔ 只读：不写库、不写日历、不重拉、不回灌；跑前跑后库文件与两份输入文件的 md5 不变（有一格测试断言）。
//
// 用法：
//
//	go build -o derivedreport ./tools/derivedreport
//	./derivedreport -product SHFE.rb -from 20250915 -to 20260911 -store <库根> -days <交易日表> -spans <段文件>
//
// 库根下每份合约一个目录，名字是合约全名（例如 SHFE.rb2601），里面是 segfile 的 1m 库。
package main

import (
	"crypto/md5"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/derived"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, time.Now())) }

// disposalHint 是处置表里「先查什么」那一栏（design.md 那一节第三段）。⛔ 每个动作都不走 derived。
var disposalHint = map[derived.DiffKind]string{
	derived.BaseNightObservedAbsent:       "查交易所年度休市安排；安排没说 ⇒ 疑源侧缺数 ⇒ 删该合约文件从早到晚重拉；重拉后仍同 ⇒ 记源侧确认缺，不再重拉",
	derived.BaseNightObservedZeroVolume:   "看同一天别的合约有没有夜盘量；没有 ⇒ 疑占位根，目前没有能单独核夜盘量的第二来源 ⇒ 记为未决",
	derived.BaseNoNightObservedTraded:     "查 embedded 品种时段表与交易所夜盘品种公告；新开了夜盘 ⇒ 改时段表（代码改动，走评审）",
	derived.BaseNoNightObservedZeroVolume: "疑占位根 ⇒ 没有第二来源，记为未决；时段表确实过时 ⇒ 改时段表",
	derived.BaseTradingDayNoObservation:   "查注入表来源与新浪 RB0 日线那天有没有根（按交易日对）；休市 ⇒ 改注入表；有交易 ⇒ 重拉；重拉后仍一根都没有 ⇒ 记源侧确认缺，不再重拉",
	derived.ObservedNotBaseTradingDay:     "查注入表：漏了一天 ⇒ 改注入表",
}

func run(args []string, out io.Writer, now time.Time) int {
	fs := flag.NewFlagSet("derivedreport", flag.ContinueOnError)
	fs.SetOutput(out)
	product := fs.String("product", "", "品种，例如 SHFE.rb（必填）")
	fromS := fs.String("from", "", "被问的那一段起（YYYYMMDD，必填）")
	toS := fs.String("to", "", "被问的那一段止（YYYYMMDD，必填）")
	storeRoot := fs.String("store", "", "库根目录（必填）")
	daysPath := fs.String("days", "", "注入的交易日表（必填）")
	spansPath := fs.String("spans", "", "段文件：合约 段起 段止（必填）")
	if err := fs.Parse(args); err != nil {
		return exitFailed
	}
	fail := func(format string, a ...any) int {
		fmt.Fprintf(out, "⛔ 没跑完："+format+"\n", a...)
		return exitFailed
	}
	if *product == "" || *fromS == "" || *toS == "" || *storeRoot == "" || *daysPath == "" || *spansPath == "" {
		return fail("六个参数都必填（-product -from -to -store -days -spans）")
	}
	pk, ok := parseProduct(*product)
	if !ok {
		return fail("-product 要写成「交易所.品种」，例如 SHFE.rb，实得 %q", *product)
	}
	from, err := parseDay(*fromS)
	if err != nil {
		return fail("-from：%v", err)
	}
	to, err := parseDay(*toS)
	if err != nil {
		return fail("-to：%v", err)
	}
	if from > to {
		return fail("-from %s 晚于 -to %s", from, to)
	}

	// —— 头 ——
	fmt.Fprintf(out, "derivedreport · 品种 %s · 被问的那一段 [%s, %s] · 运行时刻 %s\n", pk, from, to, now.In(tickflow.CST).Format("2006-01-02 15:04:05 -0700"))
	days, err := readDays(*daysPath)
	if err != nil {
		return fail("交易日表：%v", err)
	}
	fmt.Fprintf(out, "日历来源 %s（%d 天 · md5 %s）\n", *daysPath, len(days), fileMD5(*daysPath))
	spans, err := readSpans(*spansPath)
	if err != nil {
		return fail("段文件：%v", err)
	}
	fmt.Fprintf(out, "段文件 %s（%d 份合约 · md5 %s）\n", *spansPath, len(spans), fileMD5(*spansPath))
	for _, sp := range spans {
		if sp.sym.ProductKey() != pk {
			return fail("段文件里的 %s 不属于品种 %s", sp.sym, pk)
		}
	}

	// —— 一段：结构前提（不过即停，后面一段都不出） ——
	fmt.Fprintln(out, "== 结构前提 ==")
	if gaps := uncoveredWindowDays(spans, days, from, to); len(gaps) > 0 {
		fmt.Fprintf(out, "⛔ 不过：窗口里 %d 个交易日一个合约的段都没落到（段文件不完整，不许静默裁掉）：%v\n", len(gaps), gaps)
		return exitFailed
	}
	lr, err := readLibrary(*storeRoot, spans, from, to)
	if err != nil {
		fmt.Fprintf(out, "⛔ 不过：%v\n", err)
		return exitFailed
	}
	fmt.Fprintln(out, "过：段文件覆盖窗口里每个交易日 · 每份合约的库都打得开、没有截断、coverage 逐段读得回")

	// —— 覆盖层 ——
	fmt.Fprintln(out, "== 覆盖层（段内没覆盖的天按位置转成洞；不按年龄分） ==")
	covHoles, covLines := classifyCoverage(spans, lr, days, from, to)
	for _, ln := range covLines {
		end := "（一段 coverage 都没有）"
		if ln.covEnd != 0 {
			end = ln.covEnd.String()
		}
		mt := "—"
		if t, ok := lr.modTime[ln.sym]; ok {
			mt = t.In(tickflow.CST).Format("2006-01-02 15:04:05")
		}
		fmt.Fprintf(out, "%-14s 段 %s..%s · coverage 末端 %s · 尾部未登记 %d 天 · 永久洞 %d 天 · 库文件修改时刻 %s",
			ln.sym, ln.sp.from, ln.sp.to, end, ln.tail, ln.holes, mt)
		if ln.exempt > 0 {
			fmt.Fprintf(out, " · 放行 %d 天（%s）", ln.exempt, ln.exemptReason)
		}
		fmt.Fprintln(out)
	}

	series, err := buildMain(pk.String(), lr.agg)
	if err != nil {
		return fail("拼主连：%v", err)
	}
	holes := make([]derived.Hole, 0, len(covHoles))
	for _, h := range covHoles {
		holes = append(holes, derived.Hole{Day: h.day, Reason: h.reason})
	}
	rep, err := derived.Judge(derived.Input{From: from, To: to, Main: series, Nights: lr.nights, Holes: holes})
	if err != nil {
		return fail("判据：%v", err)
	}

	// —— 二段：判据摘要 ——
	fmt.Fprintln(out, "== 判据 ==")
	fmt.Fprint(out, rep.Summary())

	// —— 三段：差异表 ——
	base, err := flattenBase(pk, days, from, to)
	if err != nil {
		return fail("摊平 base：%v", err)
	}
	if len(base) == 0 {
		return fail("注入表在 [%s, %s] 里一个交易日都没有 —— 无从比对", from, to)
	}
	diffs, err := derived.Compare(rep, base)
	if err != nil {
		return fail("比对：%v", err)
	}
	fmt.Fprintf(out, "== 差异（%d 条） ==\n", len(diffs))
	counts := map[derived.DiffKind]int{}
	for _, d := range diffs {
		counts[d.Kind]++
		fmt.Fprintf(out, "%s · %s · 观测 %s · 先查：%s\n", d.Day, d.Kind, d.Verdict, disposalHint[d.Kind])
	}

	// —— 尾 ——
	kinds := make([]derived.DiffKind, 0, len(counts))
	for k := range counts {
		kinds = append(kinds, k)
	}
	sort.Slice(kinds, func(i, j int) bool { return kinds[i] < kinds[j] })
	var parts []string
	for _, k := range kinds {
		parts = append(parts, fmt.Sprintf("%s %d", k, counts[k]))
	}
	fmt.Fprintf(out, "按种类：%s\n", strings.Join(parts, " · "))

	code := decideExit(rep, diffs, base[0].Day, base[len(base)-1].Day)
	fmt.Fprintf(out, "退出码 %d\n", code)
	return code
}

func parseProduct(s string) (tickflow.ProductKey, bool) {
	i := strings.IndexByte(s, '.')
	if i <= 0 || i == len(s)-1 {
		return tickflow.ProductKey{}, false
	}
	return tickflow.ProductKey{Exchange: s[:i], Product: s[i+1:]}, true
}

func fileMD5(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return "（读不了）"
	}
	h := md5.Sum(b)
	return hex.EncodeToString(h[:])
}
