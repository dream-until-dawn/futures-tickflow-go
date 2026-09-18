package tickflow_test

import (
	"errors"
	"math"
	"strings"
	"testing"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/embedded"
	"github.com/dream-until-dawn/futures-tickflow-go/continuous"
	"github.com/dream-until-dawn/futures-tickflow-go/indicator"
)

// v0.9 F-c：主连日线 Feed 与 View 的四个主连方法（RawClose · Contract · IsRollDay · Basis）。
// 设计 design.md §十五「v0.9 起手」五 F5；用户 2026-09-18 裁 U2（四方法进 v0.9）· U5（日内主连不进）。

var mainDays = []tickflow.TradingDay{20200803, 20200804, 20200805, 20200806, 20200807}

func rbSym(ym int) tickflow.Symbol {
	return tickflow.Symbol{Exchange: tickflow.SHFE, Product: "rb", YearMon: ym}
}

// mainInput：五天两合约。0803–0804 主力 2601；0805 起持仓反超 ⇒ 换到 2605。
// 2605 每天比 2601 高 100 ⇒ 换月日基差 100。各天价格不同，复权前后才分得开。
func mainInput() []continuous.DayBars {
	a, b := rbSym(2601), rbSym(2605)
	var in []continuous.DayBars
	for i, d := range mainDays {
		oiA, oiB := 100.0, 10.0
		if i >= 2 {
			oiA, oiB = 10, 100
		}
		pa := 1000 + float64(i)*10
		mk := func(p, oi float64) tickflow.Bar {
			return tickflow.Bar{Ts: at(d, 9, 0), TsEnd: at(d, 15, 0), TradingDay: d, Open: p, High: p + 5, Low: p - 5, Close: p, Volume: oi, OpenInterest: oi, Settle: math.NaN()}
		}
		in = append(in, continuous.DayBars{Day: d,
			Cands: []continuous.ContractDay{{Symbol: a, OpenInterest: oiA, Volume: oiA}, {Symbol: b, OpenInterest: oiB, Volume: oiB}},
			Bars:  map[tickflow.Symbol]tickflow.Bar{a: mk(pa, oiA), b: mk(pa+100, oiB)}})
	}
	return in
}

func mainFeed(t *testing.T, adj continuous.AdjustMethod, inds ...tickflow.Indicator) (*tickflow.Feed, continuous.Continuous) {
	t.Helper()
	c, err := continuous.Build(continuous.ContinuousSpec{Product: "SHFE.rb", Roll: continuous.ByOpenInterest{}, Adjust: adj}, mainInput())
	if err != nil {
		t.Fatal(err)
	}
	cal, err := embedded.New(mainDays)
	if err != nil {
		t.Fatal(err)
	}
	cfg := tickflow.FeedConfig{Key: keyRB, Calendar: cal, Base: tickflow.Daily, Rule: tickflow.AggTradingAxis,
		From: mainDays[0], To: mainDays[len(mainDays)-1], Main: c, Lookback: 1,
		Indicators: map[string][]tickflow.Indicator{"1d": inds}}
	f, err := tickflow.NewFeed(c.Walker(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	return f, c
}

// guard: 主连四方法（比例后复权）—— Contract 逐天对上换月（2601 ×2 → 2605 ×3）· RawClose ＝ 那天那个合约的未复权收盘 ·
// IsRollDay 只在 0805 · Basis 换月日 100、其余 NaN；判别力在前：换月之后复权价与未复权价真的不同（否则 RawClose 判不出东西）。
func TestFeedMainViewMethods(t *testing.T) {
	f, c := mainFeed(t, continuous.RatioBack)
	defer f.Close()
	raw := map[tickflow.TradingDay]float64{}
	for i, d := range mainInput() {
		s := rbSym(2601)
		if i >= 2 {
			s = rbSym(2605)
		}
		raw[d.Day] = d.Bars[s].Close
	}
	differs := false
	for i, b := range c.Bars {
		differs = differs || b.Close != raw[b.TradingDay]
		if want, ok := c.ContractAt(i); !ok || (i < 2) != (want == rbSym(2601)) {
			t.Fatalf("前提没成立：第 %d 根 ContractAt=%v", i, want)
		}
	}
	if !differs {
		t.Fatal("判别力不在场：复权后的收盘与未复权处处相等 —— RawClose 判不出东西")
	}
	k := 0
	for f.Next() {
		v := f.View()
		d := v.TradingDay()
		wantSym := rbSym(2601)
		if k >= 2 {
			wantSym = rbSym(2605)
		}
		if s, ok := v.Contract(); !ok || s != wantSym {
			t.Errorf("%s：Contract=(%v, %v)，应为 %v", d, s, ok, wantSym)
		}
		if v.RawClose() != raw[d] {
			t.Errorf("%s：RawClose %v，应为未复权 %v（复权价 %v）", d, v.RawClose(), raw[d], v.Close())
		}
		roll := d == 20200805
		if v.IsRollDay() != roll {
			t.Errorf("%s：IsRollDay=%v，应为 %v", d, v.IsRollDay(), roll)
		}
		if roll && v.Basis() != 100 {
			t.Errorf("%s：换月日 Basis %v，应为 100", d, v.Basis())
		}
		if !roll && !math.IsNaN(v.Basis()) {
			t.Errorf("%s：非换月日 Basis %v，应为 NaN（不给 0）", d, v.Basis())
		}
		if k >= 1 {
			if p := v.Prev(1); p.RawClose() != raw[p.TradingDay()] {
				t.Errorf("%s：Prev(1).RawClose %v，应为 %v", d, p.RawClose(), raw[p.TradingDay()])
			}
		}
		k++
	}
	if err := f.Err(); err != nil || k != len(mainDays) {
		t.Fatalf("走了 %d 步，err=%v", k, err)
	}
}

// guard: 不复权时 RawClose 与 Close 逐根相等（对照：上一格的「不等」来自复权，不是 RawClose 取错了列）。
func TestFeedMainNoAdjustRawEqualsClose(t *testing.T) {
	f, _ := mainFeed(t, continuous.NoAdjust)
	defer f.Close()
	k := 0
	for f.Next() {
		k++
		if v := f.View(); v.RawClose() != v.Close() {
			t.Errorf("%s：不复权而 RawClose %v ≠ Close %v", v.TradingDay(), v.RawClose(), v.Close())
		}
	}
	if k != len(mainDays) {
		t.Fatalf("走了 %d 步", k)
	}
}

// guard: 主连模式只收日线主周期（日内主连 v0.9 不做；日线主周期本来就不许辅周期）；非主连 Feed 的四方法给空答案（NaN / false），不编一个。
func TestFeedMainModeBoundaries(t *testing.T) {
	c, err := continuous.Build(continuous.ContinuousSpec{Product: "SHFE.rb", Roll: continuous.ByOpenInterest{}}, mainInput())
	if err != nil {
		t.Fatal(err)
	}
	cal, _ := embedded.New(mainDays)
	base := tickflow.FeedConfig{Key: keyRB, Calendar: cal, Base: tickflow.Daily, Rule: tickflow.AggTradingAxis, From: mainDays[0], To: mainDays[4], NoAutoWarmup: true}
	// 对照：同一配置不带 Main 能建，且四方法给空答案
	f, err := tickflow.NewFeed(c.Walker(), base)
	if err != nil {
		t.Fatalf("对照失败：%v", err)
	}
	if !f.Next() {
		t.Fatal(f.Err())
	}
	v := f.View()
	if s, ok := v.Contract(); ok || !math.IsNaN(v.RawClose()) || v.IsRollDay() || !math.IsNaN(v.Basis()) {
		t.Errorf("非主连 Feed 的四方法：Contract=(%v,%v) RawClose=%v IsRollDay=%v Basis=%v，应为 (零值,false) NaN false NaN", s, ok, v.RawClose(), v.IsRollDay(), v.Basis())
	}
	f.Close()
	// 主连模式 ＋ 日内主周期 ⇒ 报错，报文说的是「主连」（不是别的检查碰巧拦下）
	cfg := base
	cfg.Main = c
	cfg.Base = tickflow.MustIntraday(1)
	if _, err := tickflow.NewFeed(c.Walker(), cfg); err == nil || !strings.Contains(err.Error(), "主连") {
		t.Errorf("主连模式配日内主周期：err=%v，应报错并说明是主连模式的限制", err)
	}
}

// guard: 主连日线上挂指标照常工作（复权价喂指标 ⇒ 信号用复权价）：MA(2) 在换月日之后等于复权收盘的均值，不是未复权的。
func TestFeedMainIndicatorsUseAdjustedPrice(t *testing.T) {
	f, c := mainFeed(t, continuous.RatioBack, indicator.MA(2))
	defer f.Close()
	k := 0
	for f.Next() {
		if k >= 1 {
			v := f.View()
			want := (c.Bars[k].Close + c.Bars[k-1].Close) / 2
			rawMean := (v.RawClose() + v.Prev(1).RawClose()) / 2
			if math.Abs(v.Ind("ma2")-want) > 1e-9 {
				t.Errorf("%s：MA2 %v，应为复权收盘均值 %v（未复权均值 %v）", v.TradingDay(), v.Ind("ma2"), want, rawMean)
			}
		}
		k++
	}
}

// guard: continuous 的 Walker() 守 BarWalker 契约（经由接口调）—— 越段 ⇒ Is ErrWalkOutsideCoverage、一根都不回调；
// 停 ⇒ fn 返回 false 之后不再回调、结论照给 nil。对照：段内整段回调 len(Bars) 根（突变 C9 / C10 量出这两条原来没人守）。
func TestContinuousWalkerHonorsBarWalkerContract(t *testing.T) {
	c, err := continuous.Build(continuous.ContinuousSpec{Product: "SHFE.rb", Roll: continuous.ByOpenInterest{}}, mainInput())
	if err != nil {
		t.Fatal(err)
	}
	var w tickflow.BarWalker = c.Walker()
	n := 0
	if err := w.Walk(mainDays[0], mainDays[4], func(tickflow.Bar) bool { n++; return true }); err != nil || n != len(c.Bars) {
		t.Fatalf("对照失败：段内 (err=%v, 回调 %d)，期望 (nil, %d)", err, n, len(c.Bars))
	}
	n = 0
	if err := w.Walk(mainDays[0], 20200810, func(tickflow.Bar) bool { n++; return true }); !errors.Is(err, tickflow.ErrWalkOutsideCoverage) || n != 0 {
		t.Errorf("越段：(err=%v, 回调 %d)，应 Is ErrWalkOutsideCoverage 且一根不回调", err, n)
	}
	n = 0
	if err := w.Walk(mainDays[0], mainDays[4], func(tickflow.Bar) bool { n++; return false }); err != nil || n != 1 {
		t.Errorf("fn 第一根返回 false：(err=%v, 回调 %d)，期望 (nil, 1) —— 停只停回调、结论照给", err, n)
	}
}
