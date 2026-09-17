package main

import (
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/embedded"
	"github.com/dream-until-dawn/futures-tickflow-go/store/segfile"
)

// ⛔ 退出码必须对【编译出的二进制】断言（design.md 那一节 E1：go run 会把 2/3 压成 1）。
var binPath string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "derivedreport-bin-")
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	name := "derivedreport"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	binPath = filepath.Join(dir, name)
	if out, err := exec.Command("go", "build", "-o", binPath, ".").CombinedOutput(); err != nil {
		fmt.Printf("编译 derivedreport 失败：%v\n%s", err, out)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// runBin 跑编译出的二进制，交回退出码与标准输出。
func runBin(t *testing.T, args ...string) (int, string) {
	t.Helper()
	cmd := exec.Command(binPath, args...)
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0, string(out)
	case errors.As(err, &ee):
		return ee.ExitCode(), string(out)
	default:
		t.Fatalf("跑不起来：%v", err)
		return -1, ""
	}
}

var rb = tickflow.ProductKey{Exchange: tickflow.SHFE, Product: "rb"}

func sym(ym int) tickflow.Symbol {
	return tickflow.Symbol{Exchange: tickflow.SHFE, Product: "rb", YearMon: ym}
}

// dayPlan 是某份合约某天怎么造：night < 0 ⇒ 不造夜盘根。
type dayPlan struct{ night, day, oi float64 }

// commit 是一次写入并提交的一段 coverage：[from, to] 里 plans 给了的天落根，其余天确认为空。
type commit struct {
	from, to tickflow.TradingDay
	plans    map[tickflow.TradingDay]dayPlan
}

func at(d tickflow.TradingDay, hh, mm int) int64 {
	return time.Date(int(d)/10000, time.Month(int(d)/100%100), int(d)%100, hh, mm, 0, 0, tickflow.CST).UnixMilli()
}

// writeContract 在「<root>/<合约>」下造一份 1m 库，按 commits 逐段落根并提交 coverage。
func writeContract(t *testing.T, root string, cal tickflow.Calendar, s tickflow.Symbol, days []tickflow.TradingDay, commits []commit) {
	t.Helper()
	st, _, err := segfile.Open(filepath.Join(root, s.String()), tickflow.MustIntraday(1))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, c := range commits {
		n, k := 0, 0
		for _, d := range days {
			if d < c.from || d > c.to {
				continue
			}
			p, ok := c.plans[d]
			if !ok {
				continue
			}
			bar := func(ts int64, vol float64) tickflow.Bar {
				return tickflow.Bar{Ts: ts, TsEnd: ts + 60000, TradingDay: d, Open: 3000, High: 3000, Low: 3000, Close: 3000, Volume: vol, OpenInterest: p.oi}
			}
			var bars []tickflow.Bar
			if p.night >= 0 {
				bars = append(bars, bar(at(d, 0, 30), p.night/2), bar(at(d, 0, 31), p.night-p.night/2)) // 00:30 算夜盘（<04:00）
			}
			bars = append(bars, bar(at(d, 9, 0), p.day/2), bar(at(d, 9, 1), p.day-p.day/2))
			if err := st.AppendBars(bars); err != nil {
				t.Fatalf("%s %s 落盘：%v", s, d, err)
			}
			n += len(bars)
			k++
		}
		if err := st.CommitSpan(cal, rb, tickflow.Span{From: c.from, To: c.to, Bars: n, Days: k}, segfile.OutcomeComplete); err != nil {
			t.Fatalf("%s 提交 %s..%s：%v", s, c.from, c.to, err)
		}
	}
}

func writeFile(t *testing.T, path, body string) string {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func daysBody(days []tickflow.TradingDay) string {
	var b strings.Builder
	for _, d := range days {
		fmt.Fprintf(&b, "%d\n", int(d))
	}
	return b.String()
}

// —— 6.30 同形的合成库 ——

// table630Days 与 calendar/derived/judge_test.go 里的是同一张表（probe.md 6.30：新浪 RB0 的 241 个交易日）。
// 前面多放一个 2025-09-12，免得窗口首日的 base 答不出前一个交易日。
func table630Days(t *testing.T) []tickflow.TradingDay {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "days630.txt"))
	if err != nil {
		t.Fatal(err)
	}
	days, err := readDays(filepath.Join("testdata", "days630.txt"))
	if err != nil {
		t.Fatalf("%v（%d 字节）", err, len(b))
	}
	return days
}

var (
	c630 = map[tickflow.TradingDay]bool{20251009: true, 20260105: true, 20260224: true, 20260407: true, 20260506: true, 20260622: true}
)

// mainOf630 是 6.30 那张表的主力安排（NoPick 日给出持仓最大与成交最大两份）。
func mainOf630(d tickflow.TradingDay) (oiMax, volMax tickflow.Symbol) {
	switch {
	case d == 20251201:
		return sym(2601), sym(2605)
	case d < 20251202:
		return sym(2601), sym(2601)
	case d == 20260402 || d == 20260403:
		return sym(2605), sym(2610)
	case d < 20260407:
		return sym(2605), sym(2605)
	case d == 20260828 || d == 20260831:
		return sym(2610), sym(2701)
	case d < 20260901:
		return sym(2610), sym(2610)
	default:
		return sym(2701), sym(2701)
	}
}

// build630Library 造 6.30 同形的库；交回 库根 · 交易日表路径 · 段文件路径。
func build630Library(t *testing.T) (root, daysPath, spansPath string) {
	t.Helper()
	dir := t.TempDir()
	root = filepath.Join(dir, "store")
	days := table630Days(t)
	cal, err := embedded.New(days)
	if err != nil {
		t.Fatal(err)
	}
	from, to := tickflow.TradingDay(20250915), tickflow.TradingDay(20260911)
	var spanLines strings.Builder
	for _, s := range []tickflow.Symbol{sym(2601), sym(2605), sym(2610), sym(2701)} {
		plans := map[tickflow.TradingDay]dayPlan{}
		for _, d := range days {
			if d < from || d > to {
				continue
			}
			oiMax, volMax := mainOf630(d)
			p := dayPlan{night: 5, day: 5, oi: 10}
			if s == oiMax {
				p.oi = 1000
			}
			if s == volMax {
				p.night, p.day = 500, 500
			}
			if c630[d] {
				p.night = -1
			}
			plans[d] = p
		}
		writeContract(t, root, cal, s, days, []commit{{from: from, to: to, plans: plans}})
		fmt.Fprintf(&spanLines, "%s %d %d\n", s, int(from), int(to))
	}
	daysPath = writeFile(t, filepath.Join(dir, "days.txt"), daysBody(days))
	spansPath = writeFile(t, filepath.Join(dir, "spans.txt"), spanLines.String())
	return root, daysPath, spansPath
}

// treeMD5 交出目录下每个文件的 md5（路径排序后拼起来），外加若干单个文件。
func treeMD5(t *testing.T, root string, files ...string) string {
	t.Helper()
	var paths []string
	filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			paths = append(paths, p)
		}
		return nil
	})
	sort.Strings(paths)
	paths = append(paths, files...)
	h := md5.New()
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(h, "%s:%x\n", filepath.Base(p), md5.Sum(b))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// 零点：6.30 同形的库 ⇒ 差异恰好那 6 天、退出码 2（编译后的二进制）；并且只读：跑前跑后库与两份输入文件 md5 不变。
func TestZeroPointTable630ExitsTwoAndIsReadOnly(t *testing.T) {
	root, daysPath, spansPath := build630Library(t)
	before := treeMD5(t, root, daysPath, spansPath)
	code, out := runBin(t, "-product", "SHFE.rb", "-from", "20250915", "-to", "20260911", "-store", root, "-days", daysPath, "-spans", spansPath)
	after := treeMD5(t, root, daysPath, spansPath)
	if before != after {
		t.Errorf("只读被破坏：跑前 md5 %s，跑后 %s", before, after)
	}
	if code != exitDiffs {
		t.Fatalf("退出码 %d，要 %d（有差异）\n%s", code, exitDiffs, out)
	}
	for _, frag := range []string{"判了 241 天 · a（夜盘有量）230 · b（有根无量）0 · c（没有夜盘根）6", "无结论 5 天（2.1%）", "== 差异（6 条） =="} {
		if !strings.Contains(out, frag) {
			t.Errorf("报文里缺「%s」：\n%s", frag, out)
		}
	}
	for d := range c630 {
		if !strings.Contains(out, d.String()+" · base 有夜盘 · 观测没有夜盘根") {
			t.Errorf("差异表里缺 %s：\n%s", d, out)
		}
	}
}

// 码 3（一）：窗口里全是无结论 —— 四份合约一段 coverage 都没有 ⇒ 全是尾部未登记 ⇒ 差异 0 条，但不许退 0。
func TestNoJudgedDaysAllHolesExitsThree(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "store")
	days := []tickflow.TradingDay{20260302, 20260303, 20260304, 20260305}
	for _, s := range []tickflow.Symbol{sym(2605)} {
		st, _, err := segfile.Open(filepath.Join(root, s.String()), tickflow.MustIntraday(1))
		if err != nil {
			t.Fatal(err)
		}
		st.Close()
	}
	daysPath := writeFile(t, filepath.Join(dir, "days.txt"), daysBody(days))
	spansPath := writeFile(t, filepath.Join(dir, "spans.txt"), "SHFE.rb2605 20260303 20260305\n")
	code, out := runBin(t, "-product", "SHFE.rb", "-from", "20260303", "-to", "20260305", "-store", root, "-days", daysPath, "-spans", spansPath)
	if code != exitNoJudged {
		t.Fatalf("退出码 %d，要 %d（一天都没判出结论）\n%s", code, exitNoJudged, out)
	}
	if !strings.Contains(out, "尾部未登记 3") || !strings.Contains(out, "== 差异（0 条） ==") {
		t.Errorf("报文不对：\n%s", out)
	}
}

// 码 3（二）：base 窄到与判出结论的天交集为 0 —— 注入表在窗口里只认一个 NoPick 日。
func TestNoJudgedDaysNarrowBaseExitsThree(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "store")
	all := []tickflow.TradingDay{20251127, 20251128, 20251201, 20251202}
	cal, err := embedded.New(all)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []tickflow.Symbol{sym(2601), sym(2605)} {
		plans := map[tickflow.TradingDay]dayPlan{}
		for _, d := range []tickflow.TradingDay{20251128, 20251201, 20251202} {
			oiMax, volMax := mainOf630(d)
			p := dayPlan{night: 5, day: 5, oi: 10}
			if s == oiMax {
				p.oi = 1000
			}
			if s == volMax {
				p.night, p.day = 500, 500
			}
			plans[d] = p
		}
		writeContract(t, root, cal, s, all, []commit{{from: 20251128, to: 20251202, plans: plans}})
	}
	// 注入表只给 1127（base 答 1201 的前一交易日用）与 1201 ⇒ 窗口 [1128, 1202] 里 base 只认 1201（NoPick 日）
	daysPath := writeFile(t, filepath.Join(dir, "days.txt"), "20251127\n20251201\n")
	spansPath := writeFile(t, filepath.Join(dir, "spans.txt"), "SHFE.rb2601 20251128 20251202\nSHFE.rb2605 20251128 20251202\n")
	code, out := runBin(t, "-product", "SHFE.rb", "-from", "20251128", "-to", "20251202", "-store", root, "-days", daysPath, "-spans", spansPath)
	if code != exitNoJudged {
		t.Fatalf("退出码 %d，要 %d（比对窗口里一天都没判出结论）\n%s", code, exitNoJudged, out)
	}
}

// 2 与 3 重叠 ⇒ 2 优先：窗口里唯一有根的一天是 NoPick，另一天 coverage 覆盖而一根都没有 ⇒ 差异 1 条、a/b/c ＝ 0。
func TestDiffsWinOverNoJudged(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "store")
	all := []tickflow.TradingDay{20251128, 20251201, 20251202}
	cal, err := embedded.New(all)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []tickflow.Symbol{sym(2601), sym(2605)} {
		oiMax, volMax := mainOf630(20251201)
		p := dayPlan{night: 5, day: 5, oi: 10}
		if s == oiMax {
			p.oi = 1000
		}
		if s == volMax {
			p.night, p.day = 500, 500
		}
		writeContract(t, root, cal, s, all, []commit{{from: 20251201, to: 20251202, plans: map[tickflow.TradingDay]dayPlan{20251201: p}}})
	}
	daysPath := writeFile(t, filepath.Join(dir, "days.txt"), daysBody(all))
	spansPath := writeFile(t, filepath.Join(dir, "spans.txt"), "SHFE.rb2601 20251201 20251202\nSHFE.rb2605 20251201 20251202\n")
	code, out := runBin(t, "-product", "SHFE.rb", "-from", "20251201", "-to", "20251202", "-store", root, "-days", daysPath, "-spans", spansPath)
	if code != exitDiffs {
		t.Fatalf("退出码 %d，要 %d（有差异优先于一天都没判出结论）\n%s", code, exitDiffs, out)
	}
	if !strings.Contains(out, "2025-12-02 · base 是交易日 · 观测一根都没有") || !strings.Contains(out, "规则没给出主力 1") {
		t.Errorf("报文不对：\n%s", out)
	}
}

// 结构前提不过 ⇒ 退出码 1，后面几段一行都不印。
func TestStructuralFailStopsBeforeLaterSections(t *testing.T) {
	root, daysPath, _ := build630Library(t)
	dir := filepath.Dir(daysPath)
	// 段文件漏掉 2025-09-15…2025-09-16：窗口里那两天一个合约的段都没落到
	spansPath := writeFile(t, filepath.Join(dir, "spans_bad.txt"),
		"SHFE.rb2601 20250917 20260911\nSHFE.rb2605 20250917 20260911\nSHFE.rb2610 20250917 20260911\nSHFE.rb2701 20250917 20260911\n")
	code, out := runBin(t, "-product", "SHFE.rb", "-from", "20250915", "-to", "20260911", "-store", root, "-days", daysPath, "-spans", spansPath)
	if code != exitFailed {
		t.Fatalf("退出码 %d，要 %d\n%s", code, exitFailed, out)
	}
	if !strings.Contains(out, "⛔ 不过：窗口里 2 个交易日一个合约的段都没落到") {
		t.Errorf("没印出不过的原因：\n%s", out)
	}
	for _, later := range []string{"== 覆盖层", "== 判据 ==", "== 差异", "退出码 "} {
		if strings.Contains(out, later) {
			t.Errorf("前提不过却印了后面那段「%s」：\n%s", later, out)
		}
	}
}

// 覆盖层按位置分：尾部（最后一段之后，不管过了多少天）⇒ 尾部未登记；两段之间与第一段之前 ⇒ 永久洞。
func TestCoverageHolesClassifiedByPosition(t *testing.T) {
	days := []tickflow.TradingDay{
		20260227,
		20260302, 20260303, 20260304, 20260305, 20260306,
		20260309, 20260310, 20260311, 20260312, 20260313,
		20260316, 20260317, 20260318, 20260319, 20260320,
	}
	dir := t.TempDir()
	root := filepath.Join(dir, "store")
	cal, err := embedded.New(days)
	if err != nil {
		t.Fatal(err)
	}
	full := func(from, to tickflow.TradingDay) map[tickflow.TradingDay]dayPlan {
		m := map[tickflow.TradingDay]dayPlan{}
		for _, d := range days {
			if d >= from && d <= to {
				m[d] = dayPlan{night: 500, day: 500, oi: 1000}
			}
		}
		return m
	}
	// 头部洞 0302 · 0303（段从 0302 起、coverage 从 0304 起）；段内空洞 0309…0310；尾部 0313…0320（6 个交易日，已超过年龄上限 5）
	writeContract(t, root, cal, sym(2605), days, []commit{
		{from: 20260304, to: 20260306, plans: full(20260304, 20260306)},
		{from: 20260311, to: 20260312, plans: full(20260311, 20260312)},
	})
	daysPath := writeFile(t, filepath.Join(dir, "days.txt"), daysBody(days))
	spansPath := writeFile(t, filepath.Join(dir, "spans.txt"), "SHFE.rb2605 20260302 20260320\n")
	code, out := runBin(t, "-product", "SHFE.rb", "-from", "20260302", "-to", "20260320", "-store", root, "-days", daysPath, "-spans", spansPath)
	if code != exitOK {
		t.Fatalf("退出码 %d，要 %d（有洞不是差异；判出的 5 天都有夜盘、base 也有）\n%s", code, exitOK, out)
	}
	if !strings.Contains(out, "coverage 末端 2026-03-12 · 尾部未登记 6 天 · 永久洞 4 天") {
		t.Errorf("覆盖层那一行不对（要 末端 03-12 · 尾部 6 · 永久洞 4）：\n%s", out)
	}
	if !strings.Contains(out, "尾部未登记 6 · 永久洞 4") {
		t.Errorf("判据摘要里的洞分类不对：\n%s", out)
	}
	for _, d := range []string{"2026-03-02", "2026-03-03", "2026-03-09", "2026-03-10"} {
		if !strings.Contains(out, "无结论 "+d+"：库侧没拉过（永久洞）") {
			t.Errorf("%s 应当是永久洞：\n%s", d, out)
		}
	}
	for _, d := range []string{"2026-03-13", "2026-03-20"} {
		if !strings.Contains(out, "无结论 "+d+"：尾部未登记（挂起或还没同步到）") {
			t.Errorf("%s 应当是尾部未登记（不管过了几个交易日）：\n%s", d, out)
		}
	}
}

// 6.31 的到期规则：rb2608 最后交易日 08-17（08-15 周六顺延）＝ 库里最后一根 ⇒ 之后没覆盖的天放行、不算洞；
// 01/02 月不适用 ⇒ 同样形状的 rb2602 不放行。
func TestExpiredContractTailIsExempt(t *testing.T) {
	days := []tickflow.TradingDay{20260812, 20260813, 20260814, 20260817, 20260818, 20260819}
	if got := lastTradingDay(sym(2608), days); got != 20260817 {
		t.Fatalf("rb2608 最后交易日算得 %s，要 2026-08-17", got)
	}
	if got := lastTradingDay(sym(2602), days); got != 0 {
		t.Errorf("rb2602（春节月份）应当返回 0（规则不作数），实得 %s", got)
	}
	if got := lastTradingDay(tickflow.Symbol{Exchange: tickflow.DCE, Product: "rr", YearMon: 2609}, days); got != 0 {
		t.Errorf("没有成文规则的品种应当返回 0，实得 %s", got)
	}
	lr := libraryRead{
		coverage: map[tickflow.Symbol][]tickflow.Span{sym(2608): {{From: 20260813, To: 20260817}}},
		lastBar:  map[tickflow.Symbol]tickflow.TradingDay{sym(2608): 20260817},
	}
	holes, lines := classifyCoverage([]span{{sym: sym(2608), from: 20260813, to: 20260819}}, lr, days, 20260813, 20260819)
	if len(holes) != 0 || lines[0].exempt != 2 {
		t.Errorf("rb2608 到期后两天应当放行：洞 %+v · 放行 %d", holes, lines[0].exempt)
	}
	// 库里最后一根 ≠ 规则算出的最后交易日 ⇒ 不放行，按位置记成尾部未登记
	lr.lastBar[sym(2608)] = 20260814
	holes, _ = classifyCoverage([]span{{sym: sym(2608), from: 20260813, to: 20260819}}, lr, days, 20260813, 20260819)
	if len(holes) != 2 || holes[0].reason.String() != "尾部未登记（挂起或还没同步到）" {
		t.Errorf("最后一根对不上规则 ⇒ 不许放行：%+v", holes)
	}
}

// 结构层：库本身坏了（截断 · 少一整条记录让 coverage 核不上）⇒ 停，退出码 1；⛔ 不许折成洞、不许往下判。
func TestBrokenLibraryStopsNotFoldedIntoHoles(t *testing.T) {
	for _, c := range []struct {
		name string
		cut  int64 // 从 1m.dat 尾部截掉多少字节
		want string
	}{
		{"截掉半条记录（残尾：打开它会截断文件）", segfile.RecordSize / 2, "残尾"},
		{"截掉一整条记录（coverage 核不上）", segfile.RecordSize, "Walk"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			root := filepath.Join(dir, "store")
			days := []tickflow.TradingDay{20260302, 20260303, 20260304}
			cal, err := embedded.New(days)
			if err != nil {
				t.Fatal(err)
			}
			writeContract(t, root, cal, sym(2605), days, []commit{{from: 20260303, to: 20260304, plans: map[tickflow.TradingDay]dayPlan{
				20260303: {night: 500, day: 500, oi: 1000}, 20260304: {night: 500, day: 500, oi: 1000}}}})
			dat := filepath.Join(root, sym(2605).String(), "1m.dat")
			fi, err := os.Stat(dat)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Truncate(dat, fi.Size()-c.cut); err != nil {
				t.Fatal(err)
			}
			daysPath := writeFile(t, filepath.Join(dir, "days.txt"), daysBody(days))
			spansPath := writeFile(t, filepath.Join(dir, "spans.txt"), "SHFE.rb2605 20260303 20260304\n")
			before := treeMD5(t, root, daysPath, spansPath)
			code, out := runBin(t, "-product", "SHFE.rb", "-from", "20260303", "-to", "20260304", "-store", root, "-days", daysPath, "-spans", spansPath)
			// ⛔ 坏库上也只读：segfile.Open 遇残尾会截断文件 —— 工具必须在打开之前就停
			if after := treeMD5(t, root, daysPath, spansPath); after != before {
				t.Errorf("只读被破坏：跑前 md5 %s，跑后 %s（工具改了一份坏库）", before, after)
			}
			if code != exitFailed {
				t.Fatalf("退出码 %d，要 %d\n%s", code, exitFailed, out)
			}
			if !strings.Contains(out, "⛔ 不过：") || !strings.Contains(out, c.want) {
				t.Errorf("没印出结构层不过的原因（要含「%s」）：\n%s", c.want, out)
			}
			for _, later := range []string{"== 覆盖层", "== 判据 ==", "永久洞", "尾部未登记"} {
				if strings.Contains(out, later) {
					t.Errorf("库坏了却往下走了「%s」：\n%s", later, out)
				}
			}
		})
	}
}
