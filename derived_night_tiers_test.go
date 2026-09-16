package tickflow_test

// derived（v0.7）那条读流水线的**零点**：库 ⇒ 按天聚 ⇒ continuous.Build ⇒ 三档（a/b/c）。
//
// ⛔ 这一格是**合成夹具**：输入是造出来的，答案在造它的时候就已知 ——
// 它存在的全部理由是「真数据回来的那一刻，这条流水线自己已经在已知答案上出过声」。
// 没有它，真数据交出的那个「夜盘有量的交易日集合」错了也看不出来：
// 判据本身就是拿这个集合去比两条主连，**流水线错了会被读成「两条主连不等」**。
//
// 三档照 probe.md 6.21：
//
//	a  有夜盘根且量合计 > 0
//	b  有夜盘根而量合计 0
//	c  没有夜盘根
//
// ⚠️ 集合只收 a；b 与 c 分开记 —— 两条主连比对时，差在 b 还是差在 c，处置完全不同。

import (
	"path/filepath"
	"sort"
	"testing"
	"time"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/embedded"
	"github.com/dream-until-dawn/futures-tickflow-go/continuous"
	"github.com/dream-until-dawn/futures-tickflow-go/store/segfile"
)

// nightAgg 是某个合约在某个交易日聚出来的那几个数。
type nightAgg struct {
	vol, oi, nightVol float64
	nightBars, bars   int
	firstTs           int64
	last              tickflow.Bar
}

// nightTiers 是这条流水线的产物。
type nightTiers struct {
	c        continuous.Continuous
	a, b, cc []tickflow.TradingDay

	// othersHadNight 是【主连那天没有夜盘量，而同品种别的合约有】的那些天。
	//
	// ⛔ 它是这条判据**假阳的形状**：市场那一夜开着（别的合约有量），而主连说没开。
	// 这里只记录、不改行为 —— 6.21 证伪掉的是「按单个/一组具体合约判」，
	// 品种级这条**只是还没被证伪**，而这正是它可能垮的方向。
	othersHadNight []tickflow.TradingDay
}

// buildNightTiers 从「一个合约一个库」的那些库里读回根，按天聚，拼主连，分三档。
//
// ⚠️ 一个合约一个库是**必须**的：`tickflow.Bar` 里没有 Symbol（按合约分文件存），
// 从库里 Walk 回来的根自己说不出它是谁的。
func buildNightTiers(t *testing.T, root string, syms []tickflow.Symbol) nightTiers {
	t.Helper()
	agg := map[tickflow.TradingDay]map[tickflow.Symbol]*nightAgg{}
	for _, sym := range syms {
		st, _, err := segfile.Open(filepath.Join(root, sym.String()), tickflow.MustIntraday(1))
		if err != nil {
			t.Fatalf("%s 开库：%v", sym, err)
		}
		for _, span := range st.Coverage() {
			err := st.Walk(span.From, span.To, func(bar tickflow.Bar) bool {
				m := agg[bar.TradingDay]
				if m == nil {
					m = map[tickflow.Symbol]*nightAgg{}
					agg[bar.TradingDay] = m
				}
				a := m[sym]
				if a == nil {
					a = &nightAgg{firstTs: bar.Ts}
					m[sym] = a
				}
				a.bars++
				a.last = bar
				a.vol += bar.Volume
				a.oi = bar.OpenInterest
				if h := time.UnixMilli(bar.Ts).In(tickflow.CST).Hour(); h >= 20 || h < 4 {
					a.nightBars++
					a.nightVol += bar.Volume
				}
				return true
			})
			if err != nil {
				t.Fatalf("%s Walk %v：%v", sym, span, err)
			}
		}
		st.Close()
	}

	days := make([]tickflow.TradingDay, 0, len(agg))
	for d := range agg {
		days = append(days, d)
	}
	sort.Slice(days, func(i, j int) bool { return days[i] < days[j] })

	in := make([]continuous.DayBars, 0, len(days))
	for _, d := range days {
		db := continuous.DayBars{Day: d, Bars: map[tickflow.Symbol]tickflow.Bar{}}
		for sym, a := range agg[d] {
			db.Cands = append(db.Cands, continuous.ContractDay{
				Symbol: sym, Day: d, Volume: a.vol, OpenInterest: a.oi,
				Expiry: continuous.ExpiryUnknown,
			})
			bar := a.last
			bar.Ts = a.firstTs
			bar.Volume = a.vol
			db.Bars[sym] = bar
		}
		sort.Slice(db.Cands, func(i, j int) bool {
			return db.Cands[i].Symbol.YearMon < db.Cands[j].Symbol.YearMon
		})
		in = append(in, db)
	}

	// ⚠️ 未复权：复权改价格，不改「那一夜有没有量」（design.md 读数计划第②样）。
	c, err := continuous.Build(continuous.ContinuousSpec{
		Product: "SHFE.rb", Roll: continuous.ByOIAndVolume{}, Adjust: continuous.NoAdjust,
	}, in)
	if err != nil {
		t.Fatalf("Build：%v", err)
	}
	out := nightTiers{c: c}
	for i, bar := range c.Bars {
		sym, ok := c.ContractAt(i)
		if !ok {
			t.Fatalf("ContractAt(%d) 说「不知道」，而序列是它自己交出来的", i)
		}
		a := agg[bar.TradingDay][sym]
		switch {
		case a.nightBars > 0 && a.nightVol > 0:
			out.a = append(out.a, bar.TradingDay)
			continue
		case a.nightBars > 0:
			out.b = append(out.b, bar.TradingDay)
		default:
			out.cc = append(out.cc, bar.TradingDay)
		}
		// 主连这一天没有夜盘量 ⇒ 看看同一天别的合约有没有。
		//
		// ⚠️ `other != sym` 在今天这条路径上**是死的**：走到这里意味着主连自己的 nightVol 已经是 0，
		// 它满足不了下面那个 `> 0`。实测：把这个子句删掉，本测试全绿（突变没被接住）。
		// 留着它是为了写清意图 —— 但**别把它读成一道被守着的判据**；
		// 哪天分档改成只看根数不看量，它才会真的起作用，那时要补一格会红的输入。
		for other, oa := range agg[bar.TradingDay] {
			if other != sym && oa.nightVol > 0 {
				out.othersHadNight = append(out.othersHadNight, bar.TradingDay)
				break
			}
		}
	}
	return out
}

// —— 夹具本体 ——

// ntDays 是夹具的交易日（真实工作日）。第一个只用来当「前一个交易日」，不参与构造。
var ntDays = []tickflow.TradingDay{
	20260227,
	20260302, 20260303, 20260304, 20260305, 20260306,
	20260309, 20260310, 20260311, 20260312, 20260313,
	20260316, 20260317,
}

// ntPlan 是某一天怎么造：两个合约各自的夜盘量、日盘量、持仓。
type ntPlan struct {
	aNight, aDay, aOI float64
	bNight, bDay, bOI float64
	why               string
}

// ntPlans 逐天写死构造与它**应当**落进哪一档。⛔ 期望值来自这张表，不来自某次跑出来的输出。
var ntPlans = []ntPlan{
	{40, 60, 1000, 4, 6, 100, "a：主力 A，夜盘有量"},
	{40, 60, 1000, 4, 6, 100, "a"},
	{40, 60, 1000, 4, 6, 100, "a"},
	{0, 100, 1000, 0, 10, 100, "b：两边都有夜盘根而量 0（主力仍是 A）"},
	{-1, 100, 1000, -1, 10, 100, "c：这一天根本没有夜盘根（-1 ＝ 不造夜盘根）"},
	{4, 6, 100, 40, 60, 1000, "换月：B 的持仓与成交都反超 ⇒ 接缝落在这一天；且是 a"},
	{4, 6, 100, 40, 60, 1000, "a"},
	{4, 6, 1000, 40, 60, 100, "NoPick：持仓最大是 A、成交最大是 B ⇒ 规则说不清"},
	{4, 6, 100, 40, 60, 1000, "a"},
	{40, 6, 100, 0, 100, 1000, "⛔ 假阳的形状：主力 B 夜盘量 0，而 A 夜盘有量 ⇒ 落进 b，并被 othersHadNight 点名"},
	{4, 6, 100, 40, 60, 1000, "a"},
	{4, 6, 100, 40, 60, 1000, "a"},
}

func ntBar(day tickflow.TradingDay, ts time.Time, vol, oi, px float64) tickflow.Bar {
	return tickflow.Bar{
		Ts: ts.UnixMilli(), TsEnd: ts.Add(time.Minute).UnixMilli(), TradingDay: day,
		Open: px, High: px, Low: px, Close: px,
		Volume: vol, OpenInterest: oi,
	}
}

// ntWrite 把一个合约的那些根写进它自己的库，并把整段 coverage 提交上去。
func ntWrite(t *testing.T, root string, cal tickflow.Calendar, sym tickflow.Symbol,
	pick func(p ntPlan) (night, day, oi float64)) {
	t.Helper()
	st, _, err := segfile.Open(filepath.Join(root, sym.String()), tickflow.MustIntraday(1))
	if err != nil {
		t.Fatalf("%s 开库：%v", sym, err)
	}
	defer st.Close()
	days := ntDays[1:]
	total := 0
	for i, d := range days {
		night, day, oi := pick(ntPlans[i])
		var bars []tickflow.Bar
		px := 3000 + float64(i)
		if night >= 0 { // night < 0 ⇒ 这一天不造夜盘根
			prev := ntDays[i]
			py, pm, pd := int(prev)/10000, int(prev)/100%100, int(prev)%100
			n0 := time.Date(py, time.Month(pm), pd, 21, 0, 0, 0, tickflow.CST)
			bars = append(bars,
				ntBar(d, n0, night/2, oi, px),
				ntBar(d, n0.Add(time.Minute), night-night/2, oi, px))
		}
		y, m, dd := int(d)/10000, int(d)/100%100, int(d)%100
		d0 := time.Date(y, time.Month(m), dd, 9, 0, 0, 0, tickflow.CST)
		bars = append(bars,
			ntBar(d, d0, day/2, oi, px),
			ntBar(d, d0.Add(time.Minute), day-day/2, oi, px))
		if err := st.AppendBars(bars); err != nil {
			t.Fatalf("%s %s 落盘：%v", sym, d, err)
		}
		total += len(bars)
	}
	sp := tickflow.Span{From: days[0], To: days[len(days)-1], Bars: total, Days: len(days)}
	if err := st.CommitSpan(cal, sym.ProductKey(), sp, segfile.OutcomeComplete); err != nil {
		t.Fatalf("%s CommitSpan：%v", sym, err)
	}
}

func TestDerivedNightTiersOnSyntheticFixture(t *testing.T) {
	root := t.TempDir()
	cal, err := embedded.New(ntDays)
	if err != nil {
		t.Fatal(err)
	}
	symA := tickflow.Symbol{Exchange: tickflow.SHFE, Product: "rb", YearMon: 2601}
	symB := tickflow.Symbol{Exchange: tickflow.SHFE, Product: "rb", YearMon: 2605}
	if len(ntPlans) != len(ntDays)-1 {
		t.Fatalf("夹具自己不自洽：%d 条计划 vs %d 个交易日", len(ntPlans), len(ntDays)-1)
	}
	ntWrite(t, root, cal, symA, func(p ntPlan) (float64, float64, float64) { return p.aNight, p.aDay, p.aOI })
	ntWrite(t, root, cal, symB, func(p ntPlan) (float64, float64, float64) { return p.bNight, p.bDay, p.bOI })

	got := buildNightTiers(t, root, []tickflow.Symbol{symA, symB})
	t.Logf("天轴 %d 天 · 序列 %d 根 · 换月 %d 次 · 首段 %s", len(got.c.Days), len(got.c.Bars), len(got.c.Rolls), got.c.First)
	t.Logf("a %v", got.a)
	t.Logf("b %v", got.b)
	t.Logf("c %v", got.cc)
	t.Logf("NoPick %v · 主连无量而别的合约有量 %v", got.c.NoPick, got.othersHadNight)

	// ⛔ 期望值逐条来自 ntPlans 那张表（造它的时候就知道），不是把某次输出粘进来。
	want := struct {
		a, b, cc, noPick, others []tickflow.TradingDay
	}{
		a:      []tickflow.TradingDay{20260302, 20260303, 20260304, 20260309, 20260310, 20260312, 20260316, 20260317},
		b:      []tickflow.TradingDay{20260305, 20260313},
		cc:     []tickflow.TradingDay{20260306},
		noPick: []tickflow.TradingDay{20260311},
		others: []tickflow.TradingDay{20260313},
	}
	same := func(got, want []tickflow.TradingDay) bool {
		if len(got) != len(want) {
			return false
		}
		for i := range got {
			if got[i] != want[i] {
				return false
			}
		}
		return true
	}
	if !same(got.a, want.a) {
		t.Errorf("a 档（夜盘有量）：得 %v，要 %v", got.a, want.a)
	}
	if !same(got.b, want.b) {
		t.Errorf("b 档（有夜盘根而量 0）：得 %v，要 %v", got.b, want.b)
	}
	if !same(got.cc, want.cc) {
		t.Errorf("c 档（没有夜盘根）：得 %v，要 %v", got.cc, want.cc)
	}
	if !same(got.c.NoPick, want.noPick) {
		t.Errorf("NoPick（规则没给出主力）：得 %v，要 %v", got.c.NoPick, want.noPick)
	}
	if !same(got.othersHadNight, want.others) {
		t.Errorf("主连无量而别的合约有量：得 %v，要 %v\n"+
			"  ⇒ 这一格是判据【假阳】的形状：市场那一夜开着，而主连说没开", got.othersHadNight, want.others)
	}
	if len(got.c.Rolls) != 1 || got.c.Rolls[0].Day != 20260309 ||
		got.c.Rolls[0].From != symA || got.c.Rolls[0].To != symB {
		t.Errorf("换月：得 %+v，要【一次、落在 2026-03-09、%s → %s】", got.c.Rolls, symA, symB)
	}
	if got.c.First != symA {
		t.Errorf("首段：得 %s，要 %s", got.c.First, symA)
	}
}
