package shinnysource

import (
	"testing"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
)

// v0.10 P-e（L10，probe.md 6.40）：Assemble 的截止 ——「TsEnd ≤ now 且（id＋1 已在这一窗里，或 now ≥ TsEnd ＋ CloseGrace）」。

// guard: L10 —— 窗里最新那一根（下一根还没出现）要等到收盘 ＋ G 才收；下一根出现了 ⇒ 收盘一到就收；
// 而「TsEnd ≤ now」照旧是必要条件：下一根已在窗里、本机却还没到收盘（本机慢）⇒ 不收（设计原文的「或」照字面会放松 CheckBars 的契约）。
func TestAssembleWaitsForNextOrGrace(t *testing.T) {
	cal := testCalendar(t)
	all := minuteBars(t, cal, rbKey, 20260903, 20260908)
	open := cst(2026, 9, 7, 9, 30)
	i := -1
	for j, b := range all {
		if b.dt == open*1e6 {
			i = j
		}
	}
	if i < 0 {
		t.Fatal("表里没有 9/7 9:30")
	}
	rows := func(last int) []Row {
		var out []Row
		for j := 0; j <= last; j++ {
			b := all[j]
			out = append(out, Row{ID: int64(j), Datetime: b.dt, Open: b.open, High: b.high, Low: b.low, Close: b.close, Volume: b.volume, CloseOI: b.closeOI})
		}
		return out
	}
	req := rbReq(2601, 20260903, 20260907)
	end := open + 60000
	const g = int64(4000) // 6.37 的读数；写成字面量，旧代码上也编得过（先跑后写）—— 与常量对不上另由 TestCloseGraceIsFourSeconds 报
	cells := []struct {
		name    string
		last    int   // 窗里最后一根的 id
		now     int64 // 本机时刻
		wantEnd int64 // 收下的最后一根的 TsEnd
	}{
		{"下一根没出现 · 恰为收盘 ⇒ 不收", i, end, open},
		{"下一根没出现 · 收盘 ＋ G − 1 ms ⇒ 不收", i, end + g - 1, open},
		{"下一根没出现 · 收盘 ＋ G ⇒ 收", i, end + g, end},
		{"下一根已出现 · 恰为收盘 ⇒ 收", i + 1, end, end},
		{"下一根已出现 · 收盘前 1 ms（本机慢）⇒ 不收", i + 1, end - 1, open},
	}
	for _, ce := range cells {
		bars, err := Assemble(rows(ce.last), cal, req, ce.now)
		if err != nil {
			t.Fatalf("%s：%v", ce.name, err)
		}
		got := int64(0)
		if len(bars) > 0 {
			got = bars[len(bars)-1].TsEnd
		}
		if got != ce.wantEnd {
			t.Errorf("%s：收下的末根 TsEnd %s，应为 %s", ce.name, fmtTs(got), fmtTs(ce.wantEnd))
		}
		if err := tickflow.CheckBars(req, bars, ce.now); err != nil {
			t.Errorf("%s：CheckBars：%v", ce.name, err)
		}
	}
}

// guard: CloseGrace 就是 6.37 定的 4 秒（上面的边界格按字面量 4000 ms 写）。
func TestCloseGraceIsFourSeconds(t *testing.T) {
	if CloseGrace.Milliseconds() != 4000 {
		t.Errorf("CloseGrace ＝ %v，应为 4 秒（probe.md 6.37）", CloseGrace)
	}
}
