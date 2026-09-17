package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/embedded"
)

// —— 评审方 2026-09-17 退回 d60fade 时点名要补的格 ——

// smallLib 造一份单合约的小库：rb2605，03-03…03-04 两天有根并提交 coverage。
func smallLib(t *testing.T) (root, daysPath, spansPath string) {
	t.Helper()
	dir := t.TempDir()
	root = filepath.Join(dir, "store")
	days := []tickflow.TradingDay{20260302, 20260303, 20260304}
	cal, err := embedded.New(days)
	if err != nil {
		t.Fatal(err)
	}
	writeContract(t, root, cal, sym(2605), days, []commit{{from: 20260303, to: 20260304, plans: map[tickflow.TradingDay]dayPlan{
		20260303: {night: 500, day: 500, oi: 1000}, 20260304: {night: 500, day: 500, oi: 1000}}}})
	daysPath = writeFile(t, filepath.Join(dir, "days.txt"), daysBody(days))
	spansPath = writeFile(t, filepath.Join(dir, "spans.txt"), "SHFE.rb2605 20260303 20260304\n")
	return root, daysPath, spansPath
}

// 结构层（L1 / L2）：旧 .meta（没有 format）· .dat 在而 .meta 不在 ⇒ 停，退出码 1，只读。
// 两格原来都不停：旧 .meta 退 0（未知语义的 coverage 被当成「拉过」）；缺 .meta 退 3、处置写成「再同步一次」（其实是孤儿记录状态）。
func TestLegacyOrMissingMetaStops(t *testing.T) {
	for _, c := range []struct {
		name   string
		mangle func(t *testing.T, meta string)
		want   string
	}{
		{"旧 .meta（没有 format 字段）", func(t *testing.T, meta string) {
			b, err := os.ReadFile(meta)
			if err != nil {
				t.Fatal(err)
			}
			s := string(b)
			i := strings.Index(s, "\"format\"")
			if i < 0 {
				t.Fatalf("前提不成立：.meta 里没有 format 字段：%s", s)
			}
			j := strings.Index(s[i:], ",")
			if j < 0 {
				t.Fatalf("前提不成立：format 后面没有逗号：%s", s)
			}
			if err := os.WriteFile(meta, []byte(s[:i]+s[i+j+1:]), 0o644); err != nil {
				t.Fatal(err)
			}
		}, "旧 .meta"},
		{".dat 在而 .meta 不在（孤儿记录）", func(t *testing.T, meta string) {
			if err := os.Remove(meta); err != nil {
				t.Fatal(err)
			}
		}, "孤儿记录"},
	} {
		t.Run(c.name, func(t *testing.T) {
			root, daysPath, spansPath := smallLib(t)
			c.mangle(t, filepath.Join(root, sym(2605).String(), "1m.meta"))
			before := treeMD5(t, root, daysPath, spansPath)
			code, out := runBin(t, "-product", "SHFE.rb", "-from", "20260303", "-to", "20260304", "-store", root, "-days", daysPath, "-spans", spansPath)
			if after := treeMD5(t, root, daysPath, spansPath); after != before {
				t.Errorf("只读被破坏：跑前 md5 %s，跑后 %s", before, after)
			}
			if code != exitFailed {
				t.Fatalf("退出码 %d，要 %d\n%s", code, exitFailed, out)
			}
			if !strings.Contains(out, "⛔ 不过：") || !strings.Contains(out, c.want) {
				t.Errorf("没印出结构层不过的原因（要含「%s」）：\n%s", c.want, out)
			}
			for _, later := range []string{"== 覆盖层", "== 判据 ==", "尾部未登记"} {
				if strings.Contains(out, later) {
					t.Errorf("结构层不过却往下走了「%s」：\n%s", later, out)
				}
			}
		})
	}
}

// 覆盖层要看段与窗口（P10 / P1 / P4）：
//
//	P10 段起之前没覆盖的天 ⇒ 不是洞（段是上市日的代理：上市之前哪来的「头部永久洞」）
//	P1  段越出窗口两端、窗口外有没覆盖的天 ⇒ 洞不许落到窗口外（否则 Judge 报 ErrOutsideWindow ⇒ 真实使用下退出码 1）
//	P4  交易日表越过窗口右端、那里没有任何段 ⇒ 结构层仍过
func TestHolesRespectSpanAndWindow(t *testing.T) {
	days := []tickflow.TradingDay{20260227, 20260302, 20260303, 20260304, 20260305, 20260306, 20260309, 20260310}
	dir := t.TempDir()
	root := filepath.Join(dir, "store")
	cal, err := embedded.New(days)
	if err != nil {
		t.Fatal(err)
	}
	// rb2605：段 03-02…03-10（越出窗口 03-04…03-05 两端），coverage 只有 03-04…03-05 ⇒ 窗口外 03-02/03-03、03-06…03-10 都没覆盖
	writeContract(t, root, cal, sym(2605), days, []commit{{from: 20260304, to: 20260305, plans: map[tickflow.TradingDay]dayPlan{
		20260304: {night: 500, day: 500, oi: 1000}, 20260305: {night: 500, day: 500, oi: 1000}}}})
	// rb2701：段从 03-05 起（晚于窗口起点 03-04），coverage 03-05 ⇒ 03-04 在段起之前 ⇒ 不是洞（P10）
	writeContract(t, root, cal, sym(2701), days, []commit{{from: 20260305, to: 20260305, plans: map[tickflow.TradingDay]dayPlan{
		20260305: {night: 5, day: 5, oi: 10}}}})
	daysPath := writeFile(t, filepath.Join(dir, "days.txt"), daysBody(days))
	spansPath := writeFile(t, filepath.Join(dir, "spans.txt"), "SHFE.rb2605 20260302 20260310\nSHFE.rb2701 20260305 20260305\n")
	code, out := runBin(t, "-product", "SHFE.rb", "-from", "20260304", "-to", "20260305", "-store", root, "-days", daysPath, "-spans", spansPath)
	if code != exitOK {
		t.Fatalf("退出码 %d，要 %d（窗口 03-04…03-05 两天都判出 a、base 也有夜盘）\n%s", code, exitOK, out)
	}
	for _, frag := range []string{
		"SHFE.rb2605    段 2026-03-02..2026-03-10 · coverage 末端 2026-03-05 · 尾部未登记 0 天 · 永久洞 0 天", // P1
		"SHFE.rb2701    段 2026-03-05..2026-03-05 · coverage 末端 2026-03-05 · 尾部未登记 0 天 · 永久洞 0 天", // P10
		"无结论 0 天",
	} {
		if !strings.Contains(out, frag) {
			t.Errorf("报文里缺「%s」：\n%s", frag, out)
		}
	}

	// P4：段收到窗口之内，交易日表仍越过窗口右端（03-06…03-10 没有任何段）⇒ 结构层仍过
	spans2 := writeFile(t, filepath.Join(dir, "spans2.txt"), "SHFE.rb2605 20260304 20260305\nSHFE.rb2701 20260305 20260305\n")
	code, out = runBin(t, "-product", "SHFE.rb", "-from", "20260304", "-to", "20260305", "-store", root, "-days", daysPath, "-spans", spans2)
	if code != exitOK || strings.Contains(out, "⛔") {
		t.Errorf("交易日表越过窗口右端、那里没有段 ⇒ 结构层应当仍过：退出码 %d\n%s", code, out)
	}
}

// 到期放行（P2 / P3）：
//
//	P2 lastBar ＝ 最后交易日，而最后交易日之前有个段间洞 ⇒ 仍是永久洞（放行只管最后交易日之后）
//	P3 15 日本身是交易日（2026-09-15 周二）⇒ 最后交易日就是 15 日，不顺延
func TestExpiryExemptionOnlyAfterLastTradingDay(t *testing.T) {
	days := []tickflow.TradingDay{20260910, 20260911, 20260914, 20260915, 20260916, 20260917}
	if got := lastTradingDay(sym(2609), days); got != 20260915 {
		t.Fatalf("rb2609 最后交易日算得 %s，要 2026-09-15（15 日是交易日，不顺延）", got)
	}
	lr := libraryRead{
		coverage: map[tickflow.Symbol][]tickflow.Span{sym(2609): {{From: 20260910, To: 20260910}, {From: 20260914, To: 20260915}}},
		lastBar:  map[tickflow.Symbol]tickflow.TradingDay{sym(2609): 20260915},
	}
	holes, lines := classifyCoverage([]span{{sym: sym(2609), from: 20260910, to: 20260917}}, lr, days, 20260910, 20260917)
	got := map[tickflow.TradingDay]string{}
	for _, h := range holes {
		got[h.day] = h.reason.String()
	}
	if got[20260911] != "库侧没拉过（永久洞）" {
		t.Errorf("09-11 是最后交易日之前的段间洞，应当仍是永久洞：%v", got)
	}
	if _, ok := got[20260916]; ok || lines[0].exempt != 2 {
		t.Errorf("09-16/09-17 在最后交易日之后应当放行：洞 %v · 放行 %d", got, lines[0].exempt)
	}
}

// 聚日取当日【最后一根】的持仓（P7）：A 当日第一根持仓最大、最后一根最小，B 反过来且成交最大 ——
// 取最后一根 ⇒ B 两项都最大 ⇒ 主力 B；取第一根 ⇒ 持仓最大 A、成交最大 B ⇒ 说不清（NoPick）。
func TestDayAggregationUsesLastOpenInterest(t *testing.T) {
	days := []tickflow.TradingDay{20260302, 20260303}
	dir := t.TempDir()
	root := filepath.Join(dir, "store")
	cal, err := embedded.New(days)
	if err != nil {
		t.Fatal(err)
	}
	writeContract(t, root, cal, sym(2605), days, []commit{{from: 20260303, to: 20260303, plans: map[tickflow.TradingDay]dayPlan{
		20260303: {night: 5, day: 5, oi: 10, oiFirst: 5000}}}})
	writeContract(t, root, cal, sym(2610), days, []commit{{from: 20260303, to: 20260303, plans: map[tickflow.TradingDay]dayPlan{
		20260303: {night: 500, day: 500, oi: 1000, oiFirst: 1}}}})
	spans := []span{{sym: sym(2605), from: 20260303, to: 20260303}, {sym: sym(2610), from: 20260303, to: 20260303}}
	lr, err := readLibrary(root, spans, 20260303, 20260303)
	if err != nil {
		t.Fatal(err)
	}
	series, err := buildMain("SHFE.rb", lr.agg)
	if err != nil {
		t.Fatal(err)
	}
	if len(series.NoPick) != 0 || len(series.Bars) != 1 {
		t.Fatalf("NoPick %v · 根 %d —— 取的不是最后一根的持仓（取第一根会说不清主力）", series.NoPick, len(series.Bars))
	}
	if s, ok := series.ContractAt(0); !ok || s != sym(2610) {
		t.Errorf("主力 %s，要 SHFE.rb2610", s)
	}
}

// 「最后一根」按全部根算，不按窗口（自查出来的，评审方没点到）：窗口整个落在到期之后时，
// 按窗口算它是 0 ⇒ 到期放行对不上 ⇒ 到期合约的空档全成了洞、整天无结论。
func TestLastBarCountsBarsOutsideWindow(t *testing.T) {
	days := []tickflow.TradingDay{20260813, 20260814, 20260817, 20260818, 20260819}
	dir := t.TempDir()
	root := filepath.Join(dir, "store")
	cal, err := embedded.New(days)
	if err != nil {
		t.Fatal(err)
	}
	writeContract(t, root, cal, sym(2608), days, []commit{{from: 20260814, to: 20260817, plans: map[tickflow.TradingDay]dayPlan{
		20260814: {night: 5, day: 5, oi: 10}, 20260817: {night: 5, day: 5, oi: 10}}}})
	// 窗口 08-18…08-19 整个在 rb2608 到期（08-17）之后
	lr, err := readLibrary(root, []span{{sym: sym(2608), from: 20260814, to: 20260819}}, 20260818, 20260819)
	if err != nil {
		t.Fatal(err)
	}
	if lr.lastBar[sym(2608)] != 20260817 {
		t.Fatalf("最后一根 %s，要 2026-08-17（窗口外的根也要算）", lr.lastBar[sym(2608)])
	}
	holes, lines := classifyCoverage([]span{{sym: sym(2608), from: 20260814, to: 20260819}}, lr, days, 20260818, 20260819)
	if len(holes) != 0 || lines[0].exempt != 2 {
		t.Errorf("到期之后的 08-18/08-19 应当放行：洞 %+v · 放行 %d", holes, lines[0].exempt)
	}
}

// 段文件里混进别的品种 ⇒ 停（P6）。
func TestSpansOfOtherProductRejected(t *testing.T) {
	root, daysPath, _ := smallLib(t)
	spansPath := writeFile(t, filepath.Join(filepath.Dir(daysPath), "spans_other.txt"), "SHFE.rb2605 20260303 20260304\nSHFE.hc2605 20260303 20260304\n")
	code, out := runBin(t, "-product", "SHFE.rb", "-from", "20260303", "-to", "20260304", "-store", root, "-days", daysPath, "-spans", spansPath)
	if code != exitFailed || !strings.Contains(out, "不属于品种") {
		t.Errorf("退出码 %d，要 %d 且点名「不属于品种」\n%s", code, exitFailed, out)
	}
}
