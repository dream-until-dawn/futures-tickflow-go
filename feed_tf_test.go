package tickflow_test

import (
	"errors"
	"strings"
	"testing"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/embedded"
	"github.com/dream-until-dawn/futures-tickflow-go/indicator"
)

// v0.9 F-b：辅周期（TF）。设计 design.md §十五「v0.9 起手」五 F3 / F4 与二·乙、§十·一 / 二。

// auBars 造 au 在 days[1:] 上的主周期根（先逐分钟造 1m，再按交易时间轴聚成 p 分钟；p=1 即 1m 原样）。
// 首日不进：embedded 的首日取不到前一交易日，没有夜盘。skip 同 synthDay。
func auBars(t testing.TB, cal *embedded.Calendar, days []tickflow.TradingDay, p int, skip func(int64) bool) []tickflow.Bar {
	t.Helper()
	var out []tickflow.Bar
	for _, d := range days[1:] {
		day, err := cal.DayOf(keyAU, d)
		if err != nil {
			t.Fatal(err)
		}
		one := synthDay(day, skip)
		if p == 1 {
			out = append(out, one...)
			continue
		}
		tmpl, err := cal.Template(keyAU, d)
		if err != nil {
			t.Fatal(err)
		}
		agg, err := tickflow.Aggregate(tickflow.AggTradingAxis, tickflow.MustIntraday(p), tmpl, day, one)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, agg...)
	}
	return out
}

func auCal(t testing.TB) *embedded.Calendar {
	t.Helper()
	cal, err := embedded.New(synthDays)
	if err != nil {
		t.Fatal(err)
	}
	return cal
}

// dayClose 是交易日 d 末段的收盘时刻。
func dayClose(t testing.TB, cal *embedded.Calendar, d tickflow.TradingDay) int64 {
	t.Helper()
	day, err := cal.DayOf(keyAU, d)
	if err != nil {
		t.Fatal(err)
	}
	return day.Sessions[len(day.Sessions)-1].End
}

// guard: 乙 —— 主周期 15m、辅周期 1d：TF("1d") 不超前（给的那根已收盘）、不落后（它之后那个交易日还没收盘）；
// 跳变发生在 15:00 而不是午夜：T 日 21:00 那根 15m 已属 T+1，TF 给 T；周五夜盘属下周一，TF 给周五。
func TestFeedDailyVisibilityJumpsAt1500(t *testing.T) {
	cal := auCal(t)
	bars := auBars(t, cal, synthDays, 15, nil)
	cfg := tickflow.FeedConfig{Key: keyAU, Calendar: cal, Base: tickflow.MustIntraday(15), Extra: []tickflow.Period{tickflow.Daily},
		Rule: tickflow.AggTradingAxis, From: synthDays[1], To: synthDays[3], NoAutoWarmup: true}
	f, err := tickflow.NewFeed(sliceWalker{bars}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	next := map[tickflow.TradingDay]tickflow.TradingDay{}
	for i := 1; i+1 < len(synthDays); i++ {
		next[synthDays[i]] = synthDays[i+1]
	}
	nightT1, friMon, at1500 := 0, 0, 0
	// ⚠️ 视图是「指针加下标」：存下来等走完再读，环形缓冲早已前进 ⇒ 当步把值取出来
	type tfv struct {
		valid bool
		day   tickflow.TradingDay
		close float64
	}
	type step struct {
		base tickflow.Bar
		tf   tfv
	}
	var steps []step
	for f.Next() {
		v := f.TF("1d")
		steps = append(steps, step{f.View().Bar(), tfv{v.Valid(), v.TradingDay(), v.Close()}})
	}
	if err := f.Err(); err != nil {
		t.Fatal(err)
	}
	// 判别力在前：真有「21:00 之后属 T+1 的根」、真有「周五夜盘属周一的根」、真有「15:00 收盘那根」
	for _, s := range steps {
		h := hm(s.base.Ts)[6:]
		if h >= "21:00" && s.base.TradingDay != tickflow.TradingDay(0) {
			nightT1++
			if s.base.TradingDay == 20260907 {
				friMon++
			}
		}
		if s.base.TsEnd == dayClose(t, cal, s.base.TradingDay) {
			at1500++
		}
	}
	if nightT1 == 0 || friMon == 0 || at1500 == 0 {
		t.Fatalf("判别力不在场：夜盘根 %d · 周五夜盘属周一 %d · 收盘那根 %d", nightT1, friMon, at1500)
	}
	for _, s := range steps {
		now := s.base.TsEnd
		if s.tf.valid {
			d := s.tf.day
			if c := dayClose(t, cal, d); c > now {
				t.Errorf("超前：主周期 %s 时 TF(1d) 给了 %s，而它 %s 才收盘", hm(now), d, hm(c))
			}
			if nd, ok := next[d]; ok {
				if c := dayClose(t, cal, nd); c <= now {
					t.Errorf("落后：主周期 %s 时 TF(1d) 还是 %s，而 %s 已于 %s 收盘", hm(now), d, nd, hm(c))
				}
			}
		} else if c := dayClose(t, cal, synthDays[1]); c <= now {
			t.Errorf("落后：主周期 %s 时 TF(1d) 还是无效，而首日 %s 已收盘", hm(now), hm(c))
		}
		// 周五夜盘（属周一）那几根：TF 必须给周五
		if s.base.TradingDay == 20260907 && hm(s.base.Ts)[:5] == "09-04" {
			if !s.tf.valid || s.tf.day != 20260904 {
				t.Errorf("周五夜盘 %s（属周一）时 TF(1d) 给 %s，应为周五 20260904", hm(s.base.Ts), s.tf.day)
			}
		}
	}
	// 日线的值：收盘价 ＝ 那天最后一根 15m 的收盘（聚合口径 aggCell，与 Aggregate 同一份）
	last := map[tickflow.TradingDay]float64{}
	for _, b := range bars {
		last[b.TradingDay] = b.Close
	}
	for _, s := range steps {
		if s.tf.valid && s.tf.close != last[s.tf.day] {
			t.Fatalf("TF(1d) %s 收盘 %v，那天最后一根 15m 收 %v", s.tf.day, s.tf.close, last[s.tf.day])
		}
	}
}

// guard: F4 —— 日内辅周期的「已收盘」按日历边界：时钟网格 60m 的 11:00 那格在 11:30 收（不是 12:00）；
// 11:15–11:30 缺根时，它在下一根主周期（13:30 那根）到来时才可见、带 FlagPartial —— 不看数据来没来、也不因缺根提前。
func TestFeedIntradayExtraClosesOnCalendar(t *testing.T) {
	cal := auCal(t)
	const d = tickflow.TradingDay(20260907)
	type tfv struct {
		valid   bool
		ts, end int64
		partial bool
	}
	run := func(skip func(int64) bool) map[int64]tfv {
		bars := auBars(t, cal, synthDays, 1, skip)
		cfg := tickflow.FeedConfig{Key: keyAU, Calendar: cal, Base: tickflow.MustIntraday(1), Extra: []tickflow.Period{tickflow.MustIntraday(60)},
			Rule: tickflow.AggClockGrid, From: d, To: d, NoAutoWarmup: true}
		f, err := tickflow.NewFeed(sliceWalker{bars}, cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { f.Close() })
		out := map[int64]tfv{} // 当步取值（视图存下来会随缓冲前进失效）
		for f.Next() {
			v := f.TF("60m")
			out[f.View().TsEnd()] = tfv{v.Valid(), v.Ts(), v.TsEnd(), v.Bar().Flags.Has(tickflow.FlagPartial)}
		}
		if err := f.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}
	full := run(nil)
	// 11:29 那根（TsEnd 11:30）：11:00 那格刚收盘 ⇒ 可见；11:28 那根（TsEnd 11:29）：还是 10:00 那格
	if v := full[at(d, 11, 30)]; !v.valid || v.ts != at(d, 11, 0) || v.end != at(d, 11, 30) {
		t.Errorf("主周期 11:30 收盘时 TF(60m) 给 %s–%s，应为刚收盘的 11:00–11:30", hm(v.ts), hm(v.end))
	}
	if v := full[at(d, 11, 29)]; !v.valid || v.ts != at(d, 10, 0) {
		t.Errorf("主周期 11:29 时 TF(60m) 给 %s，应仍是 10:00 那格（11:00 那格还没收）", hm(v.ts))
	}
	gap := run(func(ts int64) bool { return ts >= at(d, 11, 15) && ts < at(d, 11, 30) })
	if v := gap[at(d, 11, 15)]; !v.valid || v.ts != at(d, 10, 0) {
		t.Errorf("缺尾时主周期 11:15 那步 TF(60m) 给 %s，应仍是 10:00 那格（不因缺根提前收盘）", hm(v.ts))
	}
	v := gap[at(d, 13, 31)]
	if !v.valid || v.ts != at(d, 11, 0) || !v.partial {
		t.Errorf("缺尾时下一根（13:31 收）那步 TF(60m) 给 %s partial=%v，应为 11:00 那格且带 FlagPartial", hm(v.ts), v.partial)
	}
}

// guard: F3 b —— Daily 辅周期从 DailyWalker 读（带结算价）；某个已收盘交易日它没有根 ⇒ 那几步 TF("1d") 无效（NaN），不补不猜。
func TestFeedDailyWalkerAlignsByTradingDay(t *testing.T) {
	cal := auCal(t)
	bars := auBars(t, cal, synthDays, 15, nil)
	var daily []tickflow.Bar
	for _, d := range synthDays[1:] {
		if d == 20260907 {
			continue // 周一那天日线库里没有
		}
		daily = append(daily, tickflow.Bar{Ts: at(d, 9, 0), TsEnd: dayClose(t, cal, d), TradingDay: d, Open: 1, High: 2, Low: 0.5, Close: 1.5, Settle: 1.4})
	}
	// 周一缺，而 DailyWalker 的 coverage 仍是一段（sliceWalker 按首末算）—— 缺的是根，不是没拉过
	cfg := tickflow.FeedConfig{Key: keyAU, Calendar: cal, Base: tickflow.MustIntraday(15), Extra: []tickflow.Period{tickflow.Daily},
		DailyWalker: sliceWalker{daily}, Rule: tickflow.AggTradingAxis, From: synthDays[1], To: synthDays[3], NoAutoWarmup: true}
	f, err := tickflow.NewFeed(sliceWalker{bars}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sawFri, sawMonGap := false, false
	for f.Next() {
		now := f.View().TsEnd()
		v := f.TF("1d")
		switch {
		case now >= dayClose(t, cal, 20260908): // 周二已收盘 ⇒ 最近一个已收盘日是周二，库里有
			if !v.Valid() || v.TradingDay() != 20260908 {
				t.Errorf("主周期 %s：TF(1d) 应为周二那根，得 valid=%v %s", hm(now), v.Valid(), v.TradingDay())
			}
		case now >= dayClose(t, cal, 20260907): // 周一已收盘 ⇒ 最近一个已收盘日是周一，而库里没有
			if v.Valid() {
				t.Errorf("主周期 %s：周一已收盘而日线库没有周一，TF(1d) 却有效（%s）", hm(now), v.TradingDay())
			}
			sawMonGap = true
		case now >= dayClose(t, cal, 20260904):
			if !v.Valid() || v.TradingDay() != 20260904 || v.Bar().Settle != 1.4 {
				t.Errorf("主周期 %s：TF(1d) 应为周五那根（带结算价 1.4），得 valid=%v %s settle=%v", hm(now), v.Valid(), v.TradingDay(), v.Bar().Settle)
			}
			sawFri = true
		}
	}
	if err := f.Err(); err != nil {
		t.Fatal(err)
	}
	if !sawFri || !sawMonGap {
		t.Fatalf("判别力不在场：走到周五收盘之后 %v · 走到周一收盘之后 %v", sawFri, sawMonGap)
	}
}

// guard: 辅周期的构造校验 —— 日线主周期不许有日内辅周期 · 辅周期不是主周期整数倍 · 周期重复 · 给了 DailyWalker 而没有 Daily ·
// 同一实例挂在两个周期上（报文说「同一个实例」）。对照：合法的主周期 ＋ 两个辅周期能建、指标挂在辅周期上能建。
func TestFeedRejectsBadExtra(t *testing.T) {
	cal := auCal(t)
	bars := auBars(t, cal, synthDays, 5, nil)
	good := func() tickflow.FeedConfig {
		return tickflow.FeedConfig{Key: keyAU, Calendar: cal, Base: tickflow.MustIntraday(5),
			Extra: []tickflow.Period{tickflow.MustIntraday(15), tickflow.Daily}, Rule: tickflow.AggTradingAxis,
			From: synthDays[2], To: synthDays[3], Indicators: map[string][]tickflow.Indicator{"15m": {indicator.MA(3)}}}
	}
	f, err := tickflow.NewFeed(sliceWalker{bars}, good())
	if err != nil {
		t.Fatalf("对照失败：%v", err)
	}
	f.Close()
	shared := indicator.MA(4)
	for name, c := range map[string]struct {
		mut  func(*tickflow.FeedConfig)
		says string
	}{
		"日线主周期带日内辅周期": {func(c *tickflow.FeedConfig) {
			c.Base = tickflow.Daily
			c.Extra = []tickflow.Period{tickflow.MustIntraday(60)}
		}, ""},
		"辅周期不是整数倍": {func(c *tickflow.FeedConfig) { c.Extra = []tickflow.Period{tickflow.MustIntraday(12)} }, ""},
		"辅周期短于主周期": {func(c *tickflow.FeedConfig) { c.Extra = []tickflow.Period{tickflow.MustIntraday(1)} }, ""},
		"周期重复": {func(c *tickflow.FeedConfig) {
			c.Extra = []tickflow.Period{tickflow.MustIntraday(15), tickflow.MustIntraday(15)}
		}, ""},
		"DailyWalker 没有 Daily": {func(c *tickflow.FeedConfig) {
			c.Extra = []tickflow.Period{tickflow.MustIntraday(15)}
			c.DailyWalker = sliceWalker{bars}
		}, ""},
		"同一实例挂两个周期": {func(c *tickflow.FeedConfig) {
			c.Indicators = map[string][]tickflow.Indicator{"5m": {shared}, "15m": {shared}}
		}, "同一个实例"},
	} {
		cfg := good()
		c.mut(&cfg)
		_, err := tickflow.NewFeed(sliceWalker{bars}, cfg)
		if err == nil {
			t.Errorf("%s：没有报错", name)
			continue
		}
		if c.says != "" && !strings.Contains(err.Error(), c.says) {
			t.Errorf("%s：报文 %q 没说「%s」", name, err, c.says)
		}
		if errors.Is(err, tickflow.ErrAggRuleUnset) {
			t.Errorf("%s：误报成 ErrAggRuleUnset", name)
		}
	}
}

// guard: 自动预热按辅周期自己的格子数往前数（F2）—— 主周期 1m 无指标、辅周期 60m 挂 MA(12)：
// au 每天 10 格 60m ⇒ 要往前跨两天；第一步 TF("60m").Ready() 为真。对照：不预热时为假。
func TestFeedWarmupCountsExtraCells(t *testing.T) {
	cal := auCal(t)
	bars := auBars(t, cal, synthDays, 1, nil)
	mk := func(noWarm bool) tickflow.FeedConfig {
		return tickflow.FeedConfig{Key: keyAU, Calendar: cal, Base: tickflow.MustIntraday(1), Extra: []tickflow.Period{tickflow.MustIntraday(60)},
			Rule: tickflow.AggTradingAxis, From: synthDays[3], To: synthDays[3], NoAutoWarmup: noWarm,
			Indicators: map[string][]tickflow.Indicator{"60m": {indicator.MA(12)}}}
	}
	day, _ := cal.DayOf(keyAU, synthDays[3])
	tmpl, _ := cal.Template(keyAU, synthDays[3])
	bs, _ := tickflow.AggTradingAxis.Bounds(tickflow.MustIntraday(60), tmpl, day)
	if !(len(bs) < 12 && 12 < 2*len(bs)) {
		t.Fatalf("判别力不在场：每天 %d 格 60m，MA(12) 应在一天与两天之间", len(bs))
	}
	ready := func(noWarm bool) bool {
		f, err := tickflow.NewFeed(sliceWalker{bars}, mk(noWarm))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if !f.Next() {
			t.Fatalf("第一步没走出来：%v", f.Err())
		}
		return f.TF("60m").Ready()
	}
	if ready(true) {
		t.Fatal("对照失败：不预热时第一步 TF(60m) 就 Ready")
	}
	if !ready(false) {
		t.Error("自动预热后第一步 TF(60m) 不 Ready —— 预热没按辅周期自己的格子数往前数")
	}
}

// BenchmarkFeedNextWithTF 量带两个辅周期（60m 聚合 ＋ 1d 聚合）时推进一步的开销（戊）。
func BenchmarkFeedNextWithTF(b *testing.B) {
	cal := auCal(b)
	bars := auBars(b, cal, synthDays, 1, nil)
	cfg := tickflow.FeedConfig{Key: keyAU, Calendar: cal, Base: tickflow.MustIntraday(1),
		Extra: []tickflow.Period{tickflow.MustIntraday(60), tickflow.Daily}, Rule: tickflow.AggTradingAxis,
		From: synthDays[1], To: synthDays[3], NoAutoWarmup: true}
	b.ResetTimer()
	steps := 0
	for steps < b.N {
		f, err := tickflow.NewFeed(sliceWalker{bars}, cfg)
		if err != nil {
			b.Fatal(err)
		}
		for steps < b.N && f.Next() {
			_ = f.TF("60m").Close()
			steps++
		}
		f.Close()
	}
}

// rbIntraday 造 rb 在 days 上的 1m 主周期根（日历由 walkerDays 注入；首日没有夜盘）。
func rbIntraday(t *testing.T, days ...tickflow.TradingDay) (*embedded.Calendar, []tickflow.Bar) {
	t.Helper()
	cal, err := embedded.New(walkerDays)
	if err != nil {
		t.Fatal(err)
	}
	var out []tickflow.Bar
	for _, d := range days {
		day, err := cal.DayOf(walkerKey, d)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, synthDay(day, nil)...)
	}
	return cal, out
}

// guard: DailyWalker 同样先核后流 —— 两遍之间日线库被写坏 ⇒ 走完 Err() Is ErrFeedVoided（F1 一对第二个源同样成立）。对照：不改库 nil。
func TestFeedDailyWalkerChangedBetweenPasses(t *testing.T) {
	run := func(corrupt bool) error {
		lib, dir := walkerLib(t)
		defer lib.Close()
		cal, bars := rbIntraday(t, walkerDays[0], walkerDays[1])
		cfg := tickflow.FeedConfig{Key: walkerKey, Calendar: cal, Base: tickflow.MustIntraday(1), Extra: []tickflow.Period{tickflow.Daily},
			DailyWalker: lib, Rule: tickflow.AggTradingAxis, From: walkerDays[0], To: walkerDays[1], NoAutoWarmup: true}
		f, err := tickflow.NewFeed(sliceWalker{bars}, cfg)
		if err != nil {
			t.Fatalf("NewFeed：%v", err)
		}
		defer f.Close()
		if corrupt {
			corruptSecondSpan(t, dir)
		}
		for f.Next() {
		}
		return f.Err()
	}
	if err := run(false); err != nil {
		t.Fatalf("对照失败：不改日线库 Err()=%v", err)
	}
	if err := run(true); !errors.Is(err, tickflow.ErrFeedVoided) {
		t.Errorf("两遍之间写坏日线库：Err()=%v，应 errors.Is ErrFeedVoided", err)
	}
}

// guard: DailyWalker 覆盖不到 [预热起点, To] ⇒ NewFeed 报错、Is ErrWalkOutsideCoverage、报文提示「先同步」（与主源同一条，F1 二）。
// 对照：同一个日线库、区间在它的段里 ⇒ 能建。
func TestFeedDailyWalkerOutsideCoverage(t *testing.T) {
	lib, _ := walkerLib(t)
	defer lib.Close()
	cal, bars := rbIntraday(t, walkerDays[0], walkerDays[1], walkerDays[2])
	cfg := func(to tickflow.TradingDay) tickflow.FeedConfig {
		return tickflow.FeedConfig{Key: walkerKey, Calendar: cal, Base: tickflow.MustIntraday(1), Extra: []tickflow.Period{tickflow.Daily},
			DailyWalker: lib, Rule: tickflow.AggTradingAxis, From: walkerDays[0], To: to, NoAutoWarmup: true}
	}
	f, err := tickflow.NewFeed(sliceWalker{bars}, cfg(walkerDays[1]))
	if err != nil {
		t.Fatalf("对照失败：%v", err)
	}
	f.Close()
	_, err = tickflow.NewFeed(sliceWalker{bars}, cfg(walkerDays[2])) // 0807 主源有，日线库没拉
	if !errors.Is(err, tickflow.ErrWalkOutsideCoverage) || !strings.Contains(errText(err), "先同步") {
		t.Errorf("日线库没覆盖 To：err=%v，应 Is ErrWalkOutsideCoverage 且提示「先同步」", err)
	}
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// guard: 主周期那根不整个落在辅周期的某一格里 ⇒ Next 停、Err 说明「不相容」，不猜它归哪格（与 Aggregate 同一条「不满足就报错」）。
func TestFeedRejectsBaseBarAcrossExtraCell(t *testing.T) {
	cal := auCal(t)
	bars := auBars(t, cal, synthDays, 1, nil)
	// 把 10:59 那根拉长到 11:01 ⇒ 跨过时钟网格 60m 的 11:00 边界（并删掉 11:00 那根，免得重叠）
	d := tickflow.TradingDay(20260907)
	var in []tickflow.Bar
	for _, b := range bars {
		if b.Ts == at(d, 11, 0) {
			continue
		}
		if b.Ts == at(d, 10, 59) {
			b.TsEnd = at(d, 11, 1)
		}
		in = append(in, b)
	}
	cfg := tickflow.FeedConfig{Key: keyAU, Calendar: cal, Base: tickflow.MustIntraday(1), Extra: []tickflow.Period{tickflow.MustIntraday(60)},
		Rule: tickflow.AggClockGrid, From: d, To: d, NoAutoWarmup: true}
	// 对照：原样输入走完无错
	f, err := tickflow.NewFeed(sliceWalker{bars}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	for f.Next() {
	}
	if err := f.Close(); err != nil {
		t.Fatalf("对照失败：%v", err)
	}
	f, err = tickflow.NewFeed(sliceWalker{in}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for f.Next() {
	}
	if f.Err() == nil || !strings.Contains(f.Err().Error(), "不相容") {
		t.Errorf("跨格子的主周期根没被拦：Err()=%v", f.Err())
	}
}
