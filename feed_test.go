package tickflow_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/embedded"
	"github.com/dream-until-dawn/futures-tickflow-go/indicator"
	"github.com/dream-until-dawn/futures-tickflow-go/store/segfile"
)

// v0.9 F-a：Feed 核心（单周期）。设计 design.md §十五「v0.9 起手」五（F1 / F2 / F6）与二·丁。

// ── 夹具 ──

// sliceWalker 是内存里的 BarWalker：一段 coverage，就是整份切片。只给 Feed 的逻辑测试用；
// 「两遍之间库变了」「坏库」那几格用真的 segfile。
type sliceWalker struct{ bars []tickflow.Bar }

func (w sliceWalker) Coverage() []tickflow.Span {
	if len(w.bars) == 0 {
		return nil
	}
	return []tickflow.Span{{From: w.bars[0].TradingDay, To: w.bars[len(w.bars)-1].TradingDay, Bars: len(w.bars), Days: len(w.bars)}}
}

func (w sliceWalker) Walk(from, to tickflow.TradingDay, fn func(tickflow.Bar) bool) error {
	if len(w.bars) == 0 || from < w.bars[0].TradingDay || to > w.bars[len(w.bars)-1].TradingDay {
		return fmt.Errorf("sliceWalker: [%s, %s]: %w", from, to, tickflow.ErrWalkOutsideCoverage)
	}
	deliver := true
	for _, b := range w.bars {
		if deliver && b.TradingDay >= from && b.TradingDay <= to {
			deliver = fn(b)
		}
	}
	return nil
}

var _ tickflow.BarWalker = sliceWalker{}

// sinaDaily 读 indicator/testdata 里的新浪日线（v0.8 那三份原样字节）。
func sinaDaily(t *testing.T, name string) []tickflow.Bar {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("indicator", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	i := bytes.Index(b, []byte("var _=("))
	j := bytes.LastIndex(b, []byte(");"))
	if i < 0 || j <= i {
		t.Fatalf("%s 不是预期的 JSONP", name)
	}
	var rows []map[string]string
	if err := json.Unmarshal(b[i+len("var _=("):j], &rows); err != nil {
		t.Fatal(err)
	}
	out := make([]tickflow.Bar, 0, len(rows))
	for _, r := range rows {
		n, err := strconv.Atoi(strings.ReplaceAll(r["d"], "-", ""))
		if err != nil {
			t.Fatal(err)
		}
		d := tickflow.TradingDay(n)
		num := func(k string) float64 {
			v, err := strconv.ParseFloat(r[k], 64)
			if err != nil {
				t.Fatalf("%s %s %q：%v", name, k, r[k], err)
			}
			return v
		}
		out = append(out, tickflow.Bar{Ts: at(d, 9, 0), TsEnd: at(d, 15, 0), TradingDay: d,
			Open: num("o"), High: num("h"), Low: num("l"), Close: num("c"), Volume: num("v"), OpenInterest: num("p"), Settle: math.NaN()})
	}
	return out
}

func daysOf(bars []tickflow.Bar) []tickflow.TradingDay {
	out := make([]tickflow.TradingDay, len(bars))
	for i, b := range bars {
		out[i] = b.TradingDay
	}
	return out
}

// dailyCfg 是日线 Feed 的基本配置（rb，日历由数据的交易日注入）。
func dailyCfg(t *testing.T, bars []tickflow.Bar, from, to tickflow.TradingDay, inds ...tickflow.Indicator) tickflow.FeedConfig {
	t.Helper()
	cal, err := embedded.New(daysOf(bars))
	if err != nil {
		t.Fatal(err)
	}
	return tickflow.FeedConfig{Key: keyRB, Calendar: cal, Base: tickflow.Daily, Rule: tickflow.AggTradingAxis,
		From: from, To: to, Indicators: map[string][]tickflow.Indicator{"1d": inds}}
}

// corruptSecondSpan 把 walkerLib 那个库第二段的最后一条记录改成交易日倒退（与 walker_test 同一手法）。
func corruptSecondSpan(t *testing.T, dir string) {
	t.Helper()
	var dat string
	filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() && strings.HasSuffix(p, ".dat") {
			dat = p
		}
		return nil
	})
	if dat == "" {
		t.Fatal("没找到 .dat —— 构造作废")
	}
	f, err := os.OpenFile(dat, os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rec := segfile.EncodeBar(walkerBar(walkerDays[0]))
	if _, err := f.WriteAt(rec[:], 3*segfile.RecordSize); err != nil {
		t.Fatal(err)
	}
}

func walkerCfg(t *testing.T, from, to tickflow.TradingDay) tickflow.FeedConfig {
	t.Helper()
	cal, err := embedded.New(walkerDays)
	if err != nil {
		t.Fatal(err)
	}
	return tickflow.FeedConfig{Key: walkerKey, Calendar: cal, Base: tickflow.Daily, Rule: tickflow.AggTradingAxis, From: from, To: to, NoAutoWarmup: true}
}

// ── 构造校验 ──

// guard: NewFeed 的构造校验 —— nil 源（F6）· AggRule 零值（U1，Is ErrAggRuleUnset）· nil 日历 · 区间不合法 ·
// 指标挂在不存在的周期上 · 同一实例挂两次 · 不支持的主周期 · 负 Lookback，各自报错。对照（合法配置能建）在前。
func TestFeedRejectsBadConfig(t *testing.T) {
	bars := sinaDaily(t, "daily_RB2501.jsonp")
	first, last := bars[0].TradingDay, bars[len(bars)-1].TradingDay
	good := func() tickflow.FeedConfig { return dailyCfg(t, bars, first, last, indicator.MA(5)) }
	f, err := tickflow.NewFeed(sliceWalker{bars}, good())
	if err != nil {
		t.Fatalf("对照失败：合法配置 NewFeed 报错 %v", err)
	}
	f.Close()
	if _, err := tickflow.NewFeed(nil, good()); err == nil {
		t.Error("nil 源没有报错（v0.9 没有 Push）")
	}
	shared := indicator.MA(5)
	cases := []struct {
		name    string
		mut     func(*tickflow.FeedConfig)
		isUnset bool
		says    string // 报文必须含的字样（空 ⇒ 不查）
	}{
		{"AggRule 零值", func(c *tickflow.FeedConfig) { c.Rule = 0 }, true, ""},
		{"nil 日历", func(c *tickflow.FeedConfig) { c.Calendar = nil }, false, ""},
		{"From > To", func(c *tickflow.FeedConfig) { c.From, c.To = c.To, c.From }, false, ""},
		{"From 为 0", func(c *tickflow.FeedConfig) { c.From = 0 }, false, ""},
		{"指标挂在不存在的周期", func(c *tickflow.FeedConfig) { c.Indicators["15m"] = []tickflow.Indicator{indicator.MA(3)} }, false, ""},
		// ⚠️ 同一实例挂两次必然也同名 ⇒「两个指标同名」那条检查也会报；而两条的处置相反 ——
		// 同名的处置是「改名」，而给同一个实例改名两边一起改、什么都没解决。⇒ 报文必须说的是「同一个实例」
		{"同一实例挂两次", func(c *tickflow.FeedConfig) { c.Indicators["1d"] = []tickflow.Indicator{shared, shared} }, false, "同一个实例"},
		{"主周期 Weekly", func(c *tickflow.FeedConfig) { c.Base = tickflow.Weekly }, false, ""},
		{"主周期 nil", func(c *tickflow.FeedConfig) { c.Base = nil }, false, ""},
		{"Lookback 为负", func(c *tickflow.FeedConfig) { c.Lookback = -1 }, false, ""},
	}
	for _, c := range cases {
		cfg := good()
		c.mut(&cfg)
		_, err := tickflow.NewFeed(sliceWalker{bars}, cfg)
		if err == nil {
			t.Errorf("%s：没有报错", c.name)
			continue
		}
		if got := errors.Is(err, tickflow.ErrAggRuleUnset); got != c.isUnset {
			t.Errorf("%s：errors.Is(err, ErrAggRuleUnset) = %v，应为 %v（err=%v）", c.name, got, c.isUnset, err)
		}
		if c.says != "" && !strings.Contains(err.Error(), c.says) {
			t.Errorf("%s：报文 %q 没说「%s」—— 处置会被引到别的方向", c.name, err, c.says)
		}
	}
}

// guard: v0.9 只收单段（F1 二）—— From 不在段里 · To 越出段终点 · 跨两段之间的空档 ⇒ NewFeed 报错且 Is ErrWalkOutsideCoverage。
// 对照：两段各自整段都能建。
func TestFeedRangeMustLieInOneSpan(t *testing.T) {
	lib, _ := walkerLib(t)
	defer lib.Close()
	for _, ok := range [][2]tickflow.TradingDay{{walkerDays[0], walkerDays[1]}, {walkerDays[3], walkerDays[4]}} {
		f, err := tickflow.NewFeed(lib, walkerCfg(t, ok[0], ok[1]))
		if err != nil {
			t.Fatalf("对照失败：段内 [%s, %s] 报错 %v", ok[0], ok[1], err)
		}
		f.Close()
	}
	for name, r := range map[string][2]tickflow.TradingDay{
		"From 不在任何段里": {walkerDays[2], walkerDays[2]},
		"To 越出段终点":    {walkerDays[0], walkerDays[2]},
		"跨两段之间的空档":    {walkerDays[1], walkerDays[3]},
	} {
		_, err := tickflow.NewFeed(lib, walkerCfg(t, r[0], r[1]))
		if !errors.Is(err, tickflow.ErrWalkOutsideCoverage) {
			t.Errorf("%s [%s, %s]：err=%v，应 errors.Is ErrWalkOutsideCoverage", name, r[0], r[1], err)
		}
		// ⚠️ 只看 Is 不够：Walk 自己的前置也报这个哨兵，而那条路径上 NewFeed 的报文是「核库没过」——
		// 把「没拉过」说成了「坏了」。报文必须给出对的处置（突变 F4 量出来的）
		if err != nil && !strings.Contains(err.Error(), "先同步") {
			t.Errorf("%s：报文 %q 没提示「先同步」", name, err)
		}
	}
}

// ── 两遍 Walk ──

// guard: 坏库 ⇒ NewFeed 第一遍就报错、一根都不交（F1 乙）；这个错误不 Is ErrWalkOutsideCoverage（坏了 ≠ 没拉过）。
func TestFeedBrokenLibraryDeliversNothing(t *testing.T) {
	lib, dir := walkerLib(t)
	lib.Close()
	corruptSecondSpan(t, dir)
	store, truncated, err := segfile.Open(dir, tickflow.Daily)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if truncated != 0 {
		t.Fatalf("前提没成立：重开截掉了 %d 字节", truncated)
	}
	f, err := tickflow.NewFeed(store, walkerCfg(t, walkerDays[0], walkerDays[1]))
	if err == nil {
		f.Close()
		t.Fatal("另一段有坏记录，而 NewFeed 没有报错 —— 第一遍的结论没被检查")
	}
	if errors.Is(err, tickflow.ErrWalkOutsideCoverage) {
		t.Errorf("坏库的错误 Is 了 ErrWalkOutsideCoverage：%v", err)
	}
}

// guard: 两遍之间库变了（第一遍之后才写坏）⇒ 第二遍的结论经 Err() 报出、errors.Is ErrFeedVoided（F1 一）。
// 对照：不写坏时同一个区间走完 Err() 为 nil、交出 2 根。
func TestFeedLibraryChangedBetweenPasses(t *testing.T) {
	run := func(corrupt bool) (int, error) {
		lib, dir := walkerLib(t)
		defer lib.Close()
		f, err := tickflow.NewFeed(lib, walkerCfg(t, walkerDays[0], walkerDays[1]))
		if err != nil {
			t.Fatalf("NewFeed：%v", err)
		}
		defer f.Close()
		if corrupt {
			corruptSecondSpan(t, dir)
		}
		n := 0
		for f.Next() {
			n++
		}
		return n, f.Err()
	}
	n, err := run(false)
	if err != nil || n != 2 {
		t.Fatalf("对照失败：不改库 (交出 %d 根, err=%v)，期望 (2, nil)", n, err)
	}
	n, err = run(true)
	if !errors.Is(err, tickflow.ErrFeedVoided) {
		t.Errorf("两遍之间写坏了库：交出 %d 根后 Err()=%v，应 errors.Is ErrFeedVoided —— 第二遍的结论没被检查", n, err)
	}
	t.Logf("两遍之间改库：交出 %d 根后报 %v", n, err)
}

// ── 丁：Ready / Defined ──

// guard: 丁 —— 单合约完整日线上挂 MACD：Ready 在任何一步都为假（Settle 比整条序列还长），Defined 在 Warmup 之后为真。
func TestFeedReadyNeverOnSingleContractDaily(t *testing.T) {
	bars := sinaDaily(t, "daily_RB2501.jsonp")
	macd := indicator.MACD(12, 26, 9)
	settle, warm := tickflow.IndicatorSettle(macd), macd.Warmup()
	// 判别力在前：Settle 真的比整条序列长，且 Warmup < 根数（否则 Defined 那半格判不出东西）
	if settle <= len(bars) || warm >= len(bars) {
		t.Fatalf("判别力不在场：Settle %d · Warmup %d · 根数 %d", settle, warm, len(bars))
	}
	f, err := tickflow.NewFeed(sliceWalker{bars}, dailyCfg(t, bars, bars[0].TradingDay, bars[len(bars)-1].TradingDay, macd))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	k := 0
	for f.Next() {
		k++
		v := f.View()
		if v.Ready() {
			t.Fatalf("第 %d 步 Ready 为真，而 Settle %d > 整条序列 %d 根", k, settle, len(bars))
		}
		if v.Defined() != (k >= warm) {
			t.Errorf("第 %d 步 Defined=%v，Warmup %d", k, v.Defined(), warm)
		}
	}
	if err := f.Err(); err != nil || k != len(bars) {
		t.Fatalf("走了 %d 步（%d 根），err=%v", k, len(bars), err)
	}
}

// guard: 丁的另一半 —— 同一个指标喂主连（RB0 长序列、不预热、从头走）：Ready 恰在第 Settle() 步变真。
func TestFeedReadyFlipsAtSettleOnLongSeries(t *testing.T) {
	bars := sinaDaily(t, "daily_RB0.jsonp")
	macd := indicator.MACD(12, 26, 9)
	settle, warm := tickflow.IndicatorSettle(macd), macd.Warmup()
	if settle <= warm || settle >= len(bars) {
		t.Fatalf("判别力不在场：Settle %d · Warmup %d · 根数 %d（Settle 须在两者之间，Ready 与 Defined 才分得开）", settle, warm, len(bars))
	}
	cfg := dailyCfg(t, bars, bars[0].TradingDay, bars[len(bars)-1].TradingDay, macd)
	cfg.NoAutoWarmup = true
	f, err := tickflow.NewFeed(sliceWalker{bars}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	k := 0
	for f.Next() {
		k++
		if got := f.View().Ready(); got != (k >= settle) {
			t.Fatalf("第 %d 步 Ready=%v，Settle %d", k, got, settle)
		}
	}
	if k != len(bars) {
		t.Fatalf("走了 %d 步，应 %d", k, len(bars))
	}
}

// relErr 与 indicator/settle_test 的定义一致：|部分 − 全量| / max(1, |全量|)。
func relErr(a, b float64) float64 { return math.Abs(a-b) / math.Max(1, math.Abs(b)) }

// guard: 自动预热读够（F2）—— From 取 RB0 末段，第一步 Ready 为真，且各键与「从头喂到底」那一步相对误差 ≤ 2e-15（契约 C）；
// 对照在前：同一个 From 不预热时，第一步至少一个键的相对误差 > 2e-15（否则「预热读够」判不出来）。
func TestFeedAutoWarmupReachesSettle(t *testing.T) {
	bars := sinaDaily(t, "daily_RB0.jsonp")
	mk := func() []tickflow.Indicator { return []tickflow.Indicator{indicator.MACD(12, 26, 9), indicator.RSI(14)} }
	from := bars[len(bars)-100].TradingDay
	last := bars[len(bars)-1].TradingDay

	// 全量：从头喂到 From 那一步
	full := map[string]float64{}
	{
		cfg := dailyCfg(t, bars, bars[0].TradingDay, last, mk()...)
		cfg.NoAutoWarmup = true
		f, err := tickflow.NewFeed(sliceWalker{bars}, cfg)
		if err != nil {
			t.Fatal(err)
		}
		for f.Next() {
			if f.View().TradingDay() == from {
				for _, k := range f.Keys("1d") {
					full[k] = f.View().Ind(k)
				}
				break
			}
		}
		f.Close()
	}
	if len(full) == 0 {
		t.Fatal("全量那一遍没走到 From")
	}
	first := func(noWarm bool) (map[string]float64, bool) {
		cfg := dailyCfg(t, bars, from, last, mk()...)
		cfg.NoAutoWarmup = noWarm
		f, err := tickflow.NewFeed(sliceWalker{bars}, cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if !f.Next() || f.View().TradingDay() != from {
			t.Fatalf("第一步不是 From：err=%v", f.Err())
		}
		got := map[string]float64{}
		for _, k := range f.Keys("1d") {
			got[k] = f.View().Ind(k)
		}
		return got, f.View().Ready()
	}
	cold, _ := first(true)
	worst := 0.0
	for k, v := range cold {
		if r := relErr(v, full[k]); r > worst || math.IsNaN(r) {
			worst = math.Max(worst, r)
			if math.IsNaN(r) {
				worst = math.Inf(1)
			}
		}
	}
	if worst <= 2e-15 {
		t.Fatalf("判别力不在场：不预热时第一步最大相对误差 %.3g ≤ 2e-15 —— 这份数据上预热与否分不开", worst)
	}
	warm, ready := first(false)
	if !ready {
		t.Errorf("自动预热之后第一步 Ready 为假 —— 预热没读够 Settle")
	}
	for k, v := range warm {
		if r := relErr(v, full[k]); !(r <= 2e-15) {
			t.Errorf("%s：自动预热后第一步 %v，全量 %v，相对误差 %.3g > 2e-15", k, v, full[k], r)
		}
	}
	t.Logf("不预热时第一步最大相对误差 %.3g；预热后各键都 ≤ 2e-15", worst)
}

// guard: 预热起点早于 coverage 段起点 ⇒ 夹到段起点、不报错；Ready 如实为假，直到喂够 Settle 根（F2）。
func TestFeedWarmupClampedToSpanStart(t *testing.T) {
	all := sinaDaily(t, "daily_RB0.jsonp")
	bars := all[len(all)-900:] // 段从倒数第 900 根起（要比 MACD 的 Settle 长，Ready 才有机会变真）
	macd := indicator.MACD(12, 26, 9)
	settle := tickflow.IndicatorSettle(macd)
	from := bars[10].TradingDay // 段起点之后第 10 根：能预热的只有 10 根
	if settle <= 10 || settle >= len(bars) {
		t.Fatalf("判别力不在场：Settle %d 应在 (10, %d) 之间", settle, len(bars))
	}
	f, err := tickflow.NewFeed(sliceWalker{bars}, dailyCfg(t, bars, from, bars[len(bars)-1].TradingDay, macd))
	if err != nil {
		t.Fatalf("预热不够时 NewFeed 报错 %v —— 应夹到段起点、不报错", err)
	}
	defer f.Close()
	k := 0
	for f.Next() {
		k++
		n := 10 + k // 已喂的根数：段起点起 10 根预热 ＋ k 步
		if got := f.View().Ready(); got != (n >= settle) {
			t.Fatalf("第 %d 步（已喂 %d 根）Ready=%v，Settle %d", k, n, got, settle)
		}
	}
	if k != len(bars)-10 {
		t.Fatalf("走了 %d 步，应 %d", k, len(bars)-10)
	}
}

// ── 视图 ──

// guard: 视图 —— Prev 在 Lookback 之内有效、之外无效（取值 NaN 不 panic）；Handle 认周期、At 与 Ind 一致；未知键 NaN。
func TestFeedViewLookbackAndHandles(t *testing.T) {
	bars := sinaDaily(t, "daily_RB2501.jsonp")
	cfg := dailyCfg(t, bars, bars[0].TradingDay, bars[len(bars)-1].TradingDay, indicator.MA(3))
	cfg.Lookback = 2
	f, err := tickflow.NewFeed(sliceWalker{bars}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Handle("15m", "ma3"); err == nil {
		t.Error("Handle 周期不属于本 Feed 时没有报错")
	}
	h, err := f.Handle("1d", "ma3")
	if err != nil {
		t.Fatal(err)
	}
	k := 0
	for f.Next() {
		k++
		v := f.View()
		if v.Close() != bars[k-1].Close || v.TradingDay() != bars[k-1].TradingDay {
			t.Fatalf("第 %d 步视图 %v / %s，应 %v / %s", k, v.Close(), v.TradingDay(), bars[k-1].Close, bars[k-1].TradingDay)
		}
		if a, b := v.At(h), v.Ind("ma3"); !(a == b || math.IsNaN(a) && math.IsNaN(b)) {
			t.Fatalf("At %v ≠ Ind %v", a, b)
		}
		if !math.IsNaN(v.Ind("nope")) {
			t.Fatal("未知键没有给 NaN")
		}
		if k >= 3 {
			if p := v.Prev(2); !p.Valid() || p.Close() != bars[k-3].Close {
				t.Fatalf("第 %d 步 Prev(2) 无效或值错", k)
			}
			if p := v.Prev(3); p.Valid() || !math.IsNaN(p.Close()) {
				t.Fatalf("第 %d 步 Prev(3) 超出 Lookback 2，应无效且取值 NaN", k)
			}
		}
		if k == 1 && v.Prev(1).Valid() {
			t.Fatal("第 1 步 Prev(1) 不该有效")
		}
	}
}

// badWidth 声明两个字段而 Update 只返回一个值。
type badWidth struct{}

func (badWidth) Name() string                  { return "bad" }
func (badWidth) Fields() []string              { return []string{"a", "b"} }
func (badWidth) Warmup() int                   { return 1 }
func (badWidth) Update(tickflow.Bar) []float64 { return []float64{1} }
func (badWidth) Reset()                        {}

// guard: 指标 Update 返回的长度与声明的字段数不一致 ⇒ Next 返回 false、Err 非 nil（第一次推进时校验）。
func TestFeedRejectsIndicatorWidthMismatch(t *testing.T) {
	bars := sinaDaily(t, "daily_RB2501.jsonp")
	f, err := tickflow.NewFeed(sliceWalker{bars}, dailyCfg(t, bars, bars[0].TradingDay, bars[len(bars)-1].TradingDay, badWidth{}))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if f.Next() || f.Err() == nil {
		t.Errorf("字段数不一致的指标没有被拦：Next 返回真或 Err 为 nil（%v）", f.Err())
	}
}

// BenchmarkFeedNext 量推进一步的开销（戊的前半）：内存源、1m、无指标 ⇒ 基本就是 iter.Pull 的协程切换 ＋ 环形缓冲。
func BenchmarkFeedNext(b *testing.B) {
	days := []tickflow.TradingDay{20260903, 20260904, 20260907, 20260908}
	cal, err := embedded.New(days)
	if err != nil {
		b.Fatal(err)
	}
	var bars []tickflow.Bar
	for _, d := range days[1:] {
		day, err := cal.DayOf(keyAU, d)
		if err != nil {
			b.Fatal(err)
		}
		bars = append(bars, synthDay(day, nil)...)
	}
	cfg := tickflow.FeedConfig{Key: keyAU, Calendar: cal, Base: tickflow.MustIntraday(1), Rule: tickflow.AggTradingAxis,
		From: days[1], To: days[3], NoAutoWarmup: true}
	b.ResetTimer()
	steps := 0
	for steps < b.N {
		f, err := tickflow.NewFeed(sliceWalker{bars}, cfg)
		if err != nil {
			b.Fatal(err)
		}
		for steps < b.N && f.Next() {
			steps++
		}
		f.Close()
	}
}

// guard: 日内主周期的预热按「每个交易日的格子数」往前数（F2）—— 1m、MA(600)，一天 555 根（au：夜盘 330 ＋ 日盘 225），
// 要往前跨两天才够；第一步 Ready 为真、MA 与从头喂的那一遍相等。对照：同一配置不预热时第一步 Ready 为假。
func TestFeedIntradayWarmupCountsCellsPerDay(t *testing.T) {
	days := synthDays // 周四 · 周五 · 周一 · 周二
	cal, err := embedded.New(days)
	if err != nil {
		t.Fatal(err)
	}
	var bars []tickflow.Bar
	perDay := 0
	for _, d := range days[1:] { // 首日没有夜盘（embedded 的首日取不到前一交易日），不进库
		day, err := cal.DayOf(keyAU, d)
		if err != nil {
			t.Fatal(err)
		}
		bs := synthDay(day, nil)
		perDay = len(bs)
		bars = append(bars, bs...)
	}
	const n = 600
	// 判别力：600 根要跨过不止一天（否则按天数与按格子数给出同一个起点）
	if !(perDay < n && n < 2*perDay) {
		t.Fatalf("判别力不在场：每天 %d 根，MA(%d) 应在一天与两天之间", perDay, n)
	}
	from, to := days[3], days[3]
	cfg := func(noWarm bool) tickflow.FeedConfig {
		return tickflow.FeedConfig{Key: keyAU, Calendar: cal, Base: tickflow.MustIntraday(1), Rule: tickflow.AggTradingAxis,
			From: from, To: to, NoAutoWarmup: noWarm, Indicators: map[string][]tickflow.Indicator{"1m": {indicator.MA(n)}}}
	}
	firstStep := func(noWarm bool) tickflow.View {
		f, err := tickflow.NewFeed(sliceWalker{bars}, cfg(noWarm))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { f.Close() })
		if !f.Next() {
			t.Fatalf("第一步没走出来：%v", f.Err())
		}
		return f.View()
	}
	if v := firstStep(true); v.Ready() {
		t.Fatal("对照失败：不预热时第一步 Ready 为真")
	}
	v := firstStep(false)
	if !v.Ready() {
		t.Fatalf("自动预热后第一步 Ready 为假 —— 预热没数够 %d 根（每天 %d 格）", n, perDay)
	}
	// 从头算那一刻的 MA：第一步那根之前（含）的 n 根收盘均值
	idx := -1
	for i, b := range bars {
		if b.Ts == v.Ts() {
			idx = i
		}
	}
	sum := 0.0
	for _, b := range bars[idx-n+1 : idx+1] {
		sum += b.Close
	}
	if want := sum / n; relErr(v.Ind("ma600"), want) > 1e-12 {
		t.Errorf("第一步 MA600 %v，从头算 %v", v.Ind("ma600"), want)
	}
}
