package tickflow_test

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/continuous"
	"github.com/dream-until-dawn/futures-tickflow-go/indicator"
)

// v0.10 P-a：Feed.Push。设计 design.md §十五「v0.10 起手」五 L2 / L3 与分颗 P-a。
// 夹具：au 的 20260904（周五）· 0907（周一，夜盘在周五晚上）· 0908 三个交易日的合成 1m（synthDay），日历 embedded（auCal）。

// ── 夹具 ──

// flakyWalker 第一遍 Walk 给 nil、第二遍把根交完之后报错 —— 「两遍之间库变了」的最小替身。
type flakyWalker struct {
	bars []tickflow.Bar
	n    *int
}

func (w flakyWalker) Coverage() []tickflow.Span { return sliceWalker{w.bars}.Coverage() }
func (w flakyWalker) Walk(from, to tickflow.TradingDay, fn func(tickflow.Bar) bool) error {
	*w.n++
	if err := (sliceWalker{w.bars}).Walk(from, to, fn); err != nil {
		return err
	}
	if *w.n == 2 {
		return errors.New("flakyWalker：第二遍读坏了")
	}
	return nil
}

// aggDays 按 rule 把 days[1:] 每天的合成 1m 聚成 p 分钟（p＝1 原样）。
func aggDays(t testing.TB, rule tickflow.AggRule, p int, days []tickflow.TradingDay) ([]tickflow.Bar, []tickflow.Bar) {
	t.Helper()
	cal := auCal(t)
	var one, out []tickflow.Bar
	for _, d := range days[1:] {
		day, err := cal.DayOf(keyAU, d)
		if err != nil {
			t.Fatal(err)
		}
		m := synthDay(day, nil)
		one = append(one, m...)
		if p == 1 {
			out = append(out, m...)
			continue
		}
		tmpl, err := cal.Template(keyAU, d)
		if err != nil {
			t.Fatal(err)
		}
		a, err := tickflow.Aggregate(rule, tickflow.MustIntraday(p), tmpl, day, m)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, a...)
	}
	return one, out
}

// pushCfg 造一个 Feed 配置：每个周期挂一个 EMA(3)（每次新实例 —— 指标有状态）。
func pushCfg(t testing.TB, rule tickflow.AggRule, base tickflow.Period, extras []tickflow.Period, to tickflow.TradingDay) tickflow.FeedConfig {
	t.Helper()
	inds := map[string][]tickflow.Indicator{fmt.Sprint(base): {indicator.EMA(3)}}
	for _, e := range extras {
		inds[fmt.Sprint(e)] = []tickflow.Indicator{indicator.EMA(3)}
	}
	return tickflow.FeedConfig{Key: keyAU, Calendar: auCal(t), Base: base, Extra: extras, Rule: rule,
		From: synthDays[1], To: to, NoAutoWarmup: true, Lookback: 2, Indicators: inds}
}

// snap 是一步之后能读到的全部东西：每个周期的当前 / 已收盘那根、有效与否、全部指标值（按位比）。
type snap struct{ s string }

func takeSnap(f *tickflow.Feed) snap {
	var b strings.Builder
	for _, p := range f.Periods() {
		v := f.TF(p)
		fmt.Fprintf(&b, "%s valid=%v", p, v.Valid())
		if v.Valid() {
			x := v.Bar()
			fmt.Fprintf(&b, " %d %d %d o%x h%x l%x c%x v%x oi%x t%x s%x f%d", x.Ts, x.TsEnd, x.TradingDay,
				math.Float64bits(x.Open), math.Float64bits(x.High), math.Float64bits(x.Low), math.Float64bits(x.Close),
				math.Float64bits(x.Volume), math.Float64bits(x.OpenInterest), math.Float64bits(x.Turnover), math.Float64bits(x.Settle), x.Flags)
			for _, k := range f.Keys(p) {
				fmt.Fprintf(&b, " %s=%x", k, math.Float64bits(v.Ind(k)))
			}
			fmt.Fprintf(&b, " ready=%v defined=%v", v.Ready(), v.Defined())
		}
		b.WriteString(" | ")
	}
	return snap{b.String()}
}

func drain(t testing.TB, f *tickflow.Feed) []snap {
	t.Helper()
	var out []snap
	for f.Next() {
		out = append(out, takeSnap(f))
	}
	if err := f.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// halfAndHalf：src ＝ base[:cut]，其余以 1m Push；返回全部步进的快照（src 那几步 ＋ Push 里步进了的那几步）。
func halfAndHalf(t *testing.T, cfg tickflow.FeedConfig, one, base []tickflow.Bar, cut int) ([]snap, *tickflow.Feed) {
	t.Helper()
	cfg.To = base[cut-1].TradingDay
	f, err := tickflow.NewFeed(sliceWalker{base[:cut]}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	out := drain(t, f)
	for _, m := range one {
		if m.Ts < base[cut-1].TsEnd {
			continue
		}
		stepped, err := f.Push(m)
		if err != nil {
			t.Fatalf("cut %d：Push %s：%v", cut, hm(m.Ts), err)
		}
		if stepped {
			out = append(out, takeSnap(f))
		}
	}
	return out, f
}

func sameSnaps(t *testing.T, label string, want, got []snap) {
	t.Helper()
	if len(want) != len(got) {
		t.Fatalf("%s：步数 %d，应为 %d（全部从 src 读）", label, len(got), len(want))
	}
	for i := range want {
		if want[i] != got[i] {
			t.Fatalf("%s：第 %d 步不等\n  src  %s\n  push %s", label, i, want[i].s, got[i].s)
		}
	}
}

// cutAt 给 base 里收盘时刻 ＝ day 那天 hh:mm 的那一根之后的下标（src 到它为止）。
func cutAt(t *testing.T, base []tickflow.Bar, d tickflow.TradingDay, natural time.Time) int {
	t.Helper()
	ms := natural.UnixMilli()
	for i, b := range base {
		if b.TradingDay == d && b.TsEnd == ms {
			return i + 1
		}
	}
	t.Fatalf("判别力不在场：%s 那天没有收盘于 %s 的根", d, natural.Format("01-02 15:04"))
	return 0
}

func clock(y int, mo time.Month, d, hh, mm int) time.Time {
	return time.Date(y, mo, d, hh, mm, 0, 0, tickflow.CST)
}

// ── 一致性（本档最要紧的证据：实时与回测走同一个 step）──

// guard: 主周期 1m —— 同一串 1m，一半 src ＋ 一半 Push ＝ 全部 src：每一步 View、TF("15m" / "60m" / "1d")、全部指标按位相等。
// 切点覆盖：日盘中间 · 小节休息前（首推 10:30）· 午休前（首推 13:30）· 15:00 收盘（首推 21:00 夜盘，属下一个交易日）·
// 周五夜盘收尾（周六 02:30 ⇒ 首推周一 09:00，同一交易日 0907）。Push 跨过 15m / 60m 格边界时 TF 与指标同样按位相等（评审方加）。
func TestFeedPushEqualsSrc1m(t *testing.T) {
	one, _ := aggDays(t, tickflow.AggTradingAxis, 1, synthDays)
	extras := []tickflow.Period{tickflow.MustIntraday(15), tickflow.MustIntraday(60), tickflow.Daily}
	full, err := tickflow.NewFeed(sliceWalker{one}, pushCfg(t, tickflow.AggTradingAxis, tickflow.MustIntraday(1), extras, synthDays[3]))
	if err != nil {
		t.Fatal(err)
	}
	want := drain(t, full)
	full.Close()
	cuts := map[string]int{
		"日盘中间 09:37":      cutAt(t, one, 20260904, clock(2026, 9, 4, 9, 37)),
		"小节休息前 10:15":     cutAt(t, one, 20260904, clock(2026, 9, 4, 10, 15)),
		"午休前 11:30":       cutAt(t, one, 20260904, clock(2026, 9, 4, 11, 30)),
		"15:00 收盘":        cutAt(t, one, 20260904, clock(2026, 9, 4, 15, 0)),
		"周五夜盘收尾 周六 02:30": cutAt(t, one, 20260907, clock(2026, 9, 5, 2, 30)),
	}
	for label, cut := range cuts {
		got, f := halfAndHalf(t, pushCfg(t, tickflow.AggTradingAxis, tickflow.MustIntraday(1), extras, 0), one, one, cut)
		sameSnaps(t, label, want, got)
		f.Close()
	}
	// 15:00 那一刀：首推的 21:00 那根属下一个交易日（0907），TF("1d") 给 0904（§十·二）
	cut := cuts["15:00 收盘"]
	f, err := tickflow.NewFeed(sliceWalker{one[:cut]}, pushCfg(t, tickflow.AggTradingAxis, tickflow.MustIntraday(1), extras, 20260904))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	drain(t, f)
	if _, err := f.Push(one[cut]); err != nil {
		t.Fatal(err)
	}
	if v, d := f.View(), f.TF("1d"); hm(v.Ts()) != "09-04 21:00" || v.TradingDay() != 20260907 || !d.Valid() || d.TradingDay() != 20260904 {
		t.Errorf("首推那根 %s 属 %s、TF(1d) %s（valid %v），应为 09-04 21:00 · 20260907 · 20260904", hm(v.Ts()), v.TradingDay(), d.TradingDay(), d.Valid())
	}
}

// guard: 主周期更长 —— Push 攒 1m、用同一个 aggCell 出主周期的根：一半 src（主周期根）＋ 一半 Push（1m）＝ 全部 src，逐步按位相等；
// 攒到一半的格子不步进。交易时间轴 15m / 60m（60m 在 au 上有跨小节休息与跨午休的格子）与时钟网格 30m（格子收盘裁到时段末端，
// 如 10:00 那格收 10:15 —— 评审方：取 AggRule 给的收盘，不自己按网格算）三种，每个格子边界都切一刀。
func TestFeedPushEqualsSrcLongerBase(t *testing.T) {
	for _, c := range []struct {
		rule   tickflow.AggRule
		p      int
		extras []tickflow.Period
	}{
		{tickflow.AggTradingAxis, 15, []tickflow.Period{tickflow.MustIntraday(60), tickflow.Daily}},
		{tickflow.AggTradingAxis, 60, []tickflow.Period{tickflow.Daily}},
		{tickflow.AggClockGrid, 30, []tickflow.Period{tickflow.MustIntraday(60), tickflow.Daily}},
	} {
		label := fmt.Sprintf("%v %dm", c.rule, c.p)
		one, base := aggDays(t, c.rule, c.p, synthDays)
		cfg := pushCfg(t, c.rule, tickflow.MustIntraday(c.p), c.extras, synthDays[3])
		full, err := tickflow.NewFeed(sliceWalker{base}, cfg)
		if err != nil {
			t.Fatal(err)
		}
		want := drain(t, full)
		full.Close()
		straddle, trimmed := false, false
		for cut := 1; cut < len(base); cut++ {
			got, f := halfAndHalf(t, pushCfg(t, c.rule, tickflow.MustIntraday(c.p), c.extras, 0), one, base, cut)
			sameSnaps(t, fmt.Sprintf("%s cut %d（src 末根 %s）", label, cut, hm(base[cut-1].TsEnd)), want, got)
			f.Close()
			b := base[cut]
			straddle = straddle || (b.TsEnd-b.Ts) > int64(c.p)*60000
			trimmed = trimmed || (b.TsEnd-b.Ts) < int64(c.p)*60000
		}
		// 判别力：交易时间轴 60m 要有跨休息的格子（界长于 60 分钟）；时钟网格 30m 要有裁过的格子（界短于 30 分钟）
		if c.p == 60 && !straddle {
			t.Errorf("%s：判别力不在场 —— 没有一个格子跨休息", label)
		}
		if c.rule == tickflow.AggClockGrid && !trimmed {
			t.Errorf("%s：判别力不在场 —— 没有一个格子被裁到时段末端", label)
		}
	}
}

// guard: 主周期 Daily —— 按交易日攒 1m、一天一步；与「日线从 src 读」按位相等（src 的日线由同一套聚合得出：Settle NaN）。
func TestFeedPushEqualsSrcDaily(t *testing.T) {
	one, _ := aggDays(t, tickflow.AggTradingAxis, 1, synthDays)
	// 日线由 1m 主周期 Feed 的 TF("1d") 取（同一个 aggCell）
	g, err := tickflow.NewFeed(sliceWalker{one}, pushCfg(t, tickflow.AggTradingAxis, tickflow.MustIntraday(1), []tickflow.Period{tickflow.Daily}, synthDays[3]))
	if err != nil {
		t.Fatal(err)
	}
	var daily []tickflow.Bar
	for g.Next() {
		if v := g.TF("1d"); v.Valid() && (len(daily) == 0 || daily[len(daily)-1].TradingDay != v.TradingDay()) {
			daily = append(daily, v.Bar())
		}
	}
	g.Close()
	if len(daily) != 3 {
		t.Fatalf("前提没成立：日线 %d 根，应为 3", len(daily))
	}
	full, err := tickflow.NewFeed(sliceWalker{daily}, pushCfg(t, tickflow.AggTradingAxis, tickflow.Daily, nil, synthDays[3]))
	if err != nil {
		t.Fatal(err)
	}
	want := drain(t, full)
	full.Close()
	for cut := 1; cut < len(daily); cut++ {
		got, f := halfAndHalf(t, pushCfg(t, tickflow.AggTradingAxis, tickflow.Daily, nil, 0), one, daily, cut)
		sameSnaps(t, fmt.Sprintf("Daily cut %d", cut), want, got)
		f.Close()
	}
}

// ── 校验（L2 / L3）：报错时状态一格不动 ──

// guard: L2 —— Next 没走完 · src 作废 · 已 Close · 主连模式 · DailyWalker ⇒ Push 各报错。
func TestFeedPushRefusesWrongState(t *testing.T) {
	one, _ := aggDays(t, tickflow.AggTradingAxis, 1, synthDays)
	cfg := pushCfg(t, tickflow.AggTradingAxis, tickflow.MustIntraday(1), nil, synthDays[1])
	src := one[:100]
	f, err := tickflow.NewFeed(sliceWalker{src}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	f.Next()
	if _, err := f.Push(one[100]); err == nil || !strings.Contains(err.Error(), "src 还没读完") {
		t.Errorf("Next 没走完就 Push：%v，应报「src 还没读完」", err)
	}
	for f.Next() {
	}
	if _, err := f.Push(one[100]); err != nil {
		t.Errorf("对照：走完之后 Push 应能收，得 %v", err)
	}
	f.Close()
	if _, err := f.Push(one[101]); err == nil || !strings.Contains(err.Error(), "已经 Close") {
		t.Errorf("Close 之后 Push：%v，应报「已经 Close」", err)
	}

	n := 0
	v, err := tickflow.NewFeed(flakyWalker{src, &n}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	for v.Next() {
	}
	if !errors.Is(v.Err(), tickflow.ErrFeedVoided) {
		t.Fatalf("前提没成立：第二遍读坏了而 Err() ＝ %v", v.Err())
	}
	if _, err := v.Push(one[100]); !errors.Is(err, tickflow.ErrFeedVoided) {
		t.Errorf("src 作废之后 Push：%v，应报 ErrFeedVoided", err)
	}
	v.Close()

	m, _ := mainFeed(t, continuous.NoAdjust)
	for m.Next() {
	}
	if _, err := m.Push(tickflow.Bar{}); err == nil || !strings.Contains(err.Error(), "主连模式") {
		t.Errorf("主连模式 Push：%v，应报「主连模式」", err)
	}
	m.Close()

	cal := auCal(t)
	var daily []tickflow.Bar
	for _, d := range synthDays[1:] {
		daily = append(daily, tickflow.Bar{Ts: at(d, 9, 0), TsEnd: dayClose(t, cal, d), TradingDay: d, Open: 1, High: 2, Low: 0.5, Close: 1.5, Settle: 1.4})
	}
	dcfg := pushCfg(t, tickflow.AggTradingAxis, tickflow.MustIntraday(1), []tickflow.Period{tickflow.Daily}, synthDays[1])
	dcfg.DailyWalker = sliceWalker{daily}
	w, err := tickflow.NewFeed(sliceWalker{src}, dcfg)
	if err != nil {
		t.Fatal(err)
	}
	for w.Next() {
	}
	if err := w.Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Push(one[100]); err == nil || !strings.Contains(err.Error(), "DailyWalker") {
		t.Errorf("DailyWalker 下 Push：%v，应报「DailyWalker」", err)
	}
	w.Close()
}

// guard: L3 —— 重复 · 乱序 · 跳格（Is ErrPushGap，报文写缺的第一格）· 开盘不整分 · 长度不是一分钟 · 落在休息时段 · 交易日与日历不符
// ⇒ 各报错，且报错前后快照完全相同（状态一格不动）；之后推对的那一根照常步进。
func TestFeedPushRejectsBadBarsWithoutSideEffects(t *testing.T) {
	one, _ := aggDays(t, tickflow.AggTradingAxis, 1, synthDays)
	extras := []tickflow.Period{tickflow.MustIntraday(15), tickflow.Daily}
	k := cutAt(t, one, 20260904, clock(2026, 9, 4, 9, 37))
	f, err := tickflow.NewFeed(sliceWalker{one[:k]}, pushCfg(t, tickflow.AggTradingAxis, tickflow.MustIntraday(1), extras, 20260904))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	drain(t, f)
	// 先推对两根，让「上一根」是 Push 进来的那一根
	for _, b := range one[k : k+2] {
		if _, err := f.Push(b); err != nil {
			t.Fatal(err)
		}
	}
	k += 2
	s0 := takeSnap(f)
	shift := func(b tickflow.Bar, d int64) tickflow.Bar { b.Ts += d; b.TsEnd += d; return b }
	long := one[k]
	long.TsEnd += 60000
	lunch := one[k]
	lunch.Ts, lunch.TsEnd = at(20260904, 12, 0), at(20260904, 12, 1)
	wrongDay := one[k]
	wrongDay.TradingDay = 20260907
	for _, c := range []struct {
		name string
		b    tickflow.Bar
		want string
		gap  bool
	}{
		{"重复", one[k-1], "重复", false},
		{"乱序", one[k-5], "乱序", false},
		{"跳格", one[k+1], hm(one[k].Ts)[6:], true},
		{"开盘不整分", shift(one[k], 30000), "1m 格子", false},
		{"长度两分钟", long, "1m 格子", false},
		{"落在午休", lunch, "不在任何交易时段", false},
		{"交易日不符", wrongDay, "交易日", false},
	} {
		_, err := f.Push(c.b)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s：%v，应报含「%s」", c.name, err, c.want)
		}
		if got := errors.Is(err, tickflow.ErrPushGap); got != c.gap {
			t.Errorf("%s：Is ErrPushGap ＝ %v，应为 %v", c.name, got, c.gap)
		}
		if s := takeSnap(f); s != s0 {
			t.Errorf("%s：报错之后状态变了\n  前 %s\n  后 %s", c.name, s0.s, s.s)
		}
	}
	if stepped, err := f.Push(one[k]); err != nil || !stepped {
		t.Errorf("对照：推对的那一根 stepped=%v err=%v，应步进", stepped, err)
	}
}

// guard: 起步 —— 第一根必须是 src 最后一根之后那一格的第一分钟：主周期 1m 跳一格 ⇒ ErrPushGap；
// 主周期 15m 从格子中间接（下一格的第二分钟）⇒ ErrPushGap（攒出来的那根会缺头）；对照：第一分钟能接。
func TestFeedPushFirstBarMustStartNextCell(t *testing.T) {
	one, _ := aggDays(t, tickflow.AggTradingAxis, 1, synthDays)
	k := cutAt(t, one, 20260904, clock(2026, 9, 4, 9, 37))
	f, err := tickflow.NewFeed(sliceWalker{one[:k]}, pushCfg(t, tickflow.AggTradingAxis, tickflow.MustIntraday(1), nil, 20260904))
	if err != nil {
		t.Fatal(err)
	}
	drain(t, f)
	if _, err := f.Push(one[k+1]); !errors.Is(err, tickflow.ErrPushGap) {
		t.Errorf("主周期 1m 起步跳一格：%v，应 Is ErrPushGap", err)
	}
	if _, err := f.Push(one[k]); err != nil {
		t.Errorf("对照：起步推下一格：%v", err)
	}
	f.Close()

	_, base := aggDays(t, tickflow.AggTradingAxis, 15, synthDays)
	c := 3
	g, err := tickflow.NewFeed(sliceWalker{base[:c]}, pushCfg(t, tickflow.AggTradingAxis, tickflow.MustIntraday(15), nil, base[c-1].TradingDay))
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	drain(t, g)
	var first int
	for i, m := range one {
		if m.Ts == base[c-1].TsEnd {
			first = i
		}
	}
	s0 := takeSnap(g)
	if _, err := g.Push(one[first+1]); !errors.Is(err, tickflow.ErrPushGap) {
		t.Errorf("主周期 15m 从格子中间接：%v，应 Is ErrPushGap", err)
	}
	if takeSnap(g) != s0 {
		t.Error("从格子中间接报错之后状态变了")
	}
	if stepped, err := g.Push(one[first]); err != nil || stepped {
		t.Errorf("对照：格子第一分钟 stepped=%v err=%v，应收下、不步进（只攒）", stepped, err)
	}
}

// guard: 起步时 src 最后一根的收盘不是 Rule 的格子收盘 ⇒ Push 报错（口径不一致唯一查得到的一格）。
// 交易时间轴 60m 的根（有跨休息、收盘不在整点的格子）配上 AggClockGrid ⇒ 接不上；对照：收盘恰在整点的那一刀能接。
func TestFeedPushRejectsSrcFromAnotherRule(t *testing.T) {
	_, base := aggDays(t, tickflow.AggTradingAxis, 60, synthDays)
	one, _ := aggDays(t, tickflow.AggTradingAxis, 1, synthDays)
	bad, good := 0, 0
	for i, b := range base {
		mm := time.UnixMilli(b.TsEnd).In(tickflow.CST).Minute()
		if bad == 0 && mm != 0 && mm != 15 && mm != 30 {
			bad = i + 1
		}
		if good == 0 && mm == 0 {
			good = i + 1
		}
	}
	if bad == 0 || good == 0 {
		t.Fatalf("判别力不在场：bad %d · good %d", bad, good)
	}
	for _, c := range []struct {
		cut  int
		want bool // 应报错
	}{{bad, true}, {good, false}} {
		f, err := tickflow.NewFeed(sliceWalker{base[:c.cut]}, pushCfg(t, tickflow.AggClockGrid, tickflow.MustIntraday(60), nil, base[c.cut-1].TradingDay))
		if err != nil {
			t.Fatal(err)
		}
		drain(t, f)
		var next tickflow.Bar
		for _, m := range one {
			if m.Ts >= base[c.cut-1].TsEnd {
				next = m
				break
			}
		}
		_, err = f.Push(next)
		if got := err != nil && strings.Contains(err.Error(), "口径不一致"); got != c.want {
			t.Errorf("src 末根收盘 %s：报「口径不一致」%v，应为 %v（err %v）", hm(base[c.cut-1].TsEnd), got, c.want, err)
		}
		f.Close()
	}
}

// guard: 日历覆盖到头 —— 覆盖范围里最后一天的最后一分钟照收（它本身合法）；「下一格」算不出来的错留到下一次 Push 才报，
// 那时状态照样不动。第一版在收最后一分钟时就报了（预先算下一格失败），把一根合法的根拒了 —— 一致性那几格当场红在最后一根上。
func TestFeedPushLastCoveredMinute(t *testing.T) {
	one, _ := aggDays(t, tickflow.AggTradingAxis, 1, synthDays)
	k := len(one) - 1
	f, err := tickflow.NewFeed(sliceWalker{one[:k]}, pushCfg(t, tickflow.AggTradingAxis, tickflow.MustIntraday(1), nil, synthDays[3]))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	drain(t, f)
	if stepped, err := f.Push(one[k]); err != nil || !stepped {
		t.Fatalf("覆盖范围里最后一分钟 %s：stepped=%v err=%v，应收下", hm(one[k].Ts), stepped, err)
	}
	s0 := takeSnap(f)
	after := one[k]
	after.Ts, after.TsEnd, after.TradingDay = after.Ts+60000, after.TsEnd+60000, 20260909
	if _, err := f.Push(after); err == nil || !strings.Contains(err.Error(), "日历覆盖不到") {
		t.Errorf("再推一根：%v，应报「日历覆盖不到」", err)
	}
	if takeSnap(f) != s0 {
		t.Error("报「日历覆盖不到」之后状态变了")
	}
}

// guard: src 最后一根主周期根不完整（FlagPartial）⇒ 起步报错，不静默接上（评审方 2026-09-21 实测）——
// 0904 的 1m 截到 09:37、按交易时间轴聚成 15m 当 src ⇒ 末根 [09:30, 09:45) 只含 09:30–09:36、带 FlagPartial。
// 第一版把它当完结根：09:37–09:43 报「乱序」、09:44 报「重复」（假话）、09:45 起收下 ⇒ Feed 里 09:30 那根停在 7 分钟的值上、不报错。
// 现在：推任何一根都报「src 截在了格子中间」，报错前后快照相同。
func TestFeedPushRejectsPartialSrcTail(t *testing.T) {
	cal := auCal(t)
	day, err := cal.DayOf(keyAU, 20260904)
	if err != nil {
		t.Fatal(err)
	}
	tmpl, err := cal.Template(keyAU, 20260904)
	if err != nil {
		t.Fatal(err)
	}
	all := synthDay(day, nil)
	var part []tickflow.Bar
	for _, b := range all {
		if b.TsEnd <= at(20260904, 9, 37) {
			part = append(part, b)
		}
	}
	src, err := tickflow.Aggregate(tickflow.AggTradingAxis, tickflow.MustIntraday(15), tmpl, day, part)
	if err != nil {
		t.Fatal(err)
	}
	if last := src[len(src)-1]; hm(last.Ts) != "09-04 09:30" || last.Flags&tickflow.FlagPartial == 0 {
		t.Fatalf("前提没成立：src 末根 %s flags %d，应为 09:30 那根、带 FlagPartial", hm(last.Ts), last.Flags)
	}
	f, err := tickflow.NewFeed(sliceWalker{src}, pushCfg(t, tickflow.AggTradingAxis, tickflow.MustIntraday(15), nil, 20260904))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	drain(t, f)
	s0 := takeSnap(f)
	for _, b := range all {
		if b.TsEnd <= at(20260904, 9, 37) || b.Ts > at(20260904, 9, 45) {
			continue
		}
		if _, err := f.Push(b); err == nil || !strings.Contains(err.Error(), "格子中间") {
			t.Errorf("Push %s：%v，应报「src 截在了格子中间」", hm(b.Ts), err)
		}
		if takeSnap(f) != s0 {
			t.Fatalf("Push %s 报错之后状态变了", hm(b.Ts))
		}
	}
}

// guard: 起步时早于接续点的根报「早于接续点」，不说「已经推过」—— 那一分钟是 src 里的（或主周期更长时根本不在 src 里），不是推过的
// （评审方 2026-09-21：第一版起步即重推 src 末根那一分钟，报「那根已经推过」）。对照：真推过的那根再推一次 ⇒ 报「推过」。
func TestFeedPushAnchorMessageIsNotPushed(t *testing.T) {
	one, _ := aggDays(t, tickflow.AggTradingAxis, 1, synthDays)
	k := cutAt(t, one, 20260904, clock(2026, 9, 4, 9, 37))
	f, err := tickflow.NewFeed(sliceWalker{one[:k]}, pushCfg(t, tickflow.AggTradingAxis, tickflow.MustIntraday(1), nil, 20260904))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	drain(t, f)
	_, err = f.Push(one[k-1])
	if err == nil || strings.Contains(err.Error(), "推过") || !strings.Contains(err.Error(), "接续点") {
		t.Errorf("起步即重推 src 末根那一分钟：%v，应报「早于接续点」、不许说「推过」", err)
	}
	if _, err := f.Push(one[k]); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Push(one[k]); err == nil || !strings.Contains(err.Error(), "推过") {
		t.Errorf("对照：真推过的那根再推一次：%v，应报「推过」", err)
	}
}

// ── 读数 ──

// BenchmarkFeedPush：Push 一步的纳秒数（主周期 1m 每推一步；15m 每推一根 1m，15 根步进一次）。与 BenchmarkFeedNext 同一张表。
func BenchmarkFeedPush(b *testing.B) {
	for _, p := range []int{1, 15} {
		b.Run(fmt.Sprintf("base%dm", p), func(b *testing.B) {
			one, base := aggDays(b, tickflow.AggTradingAxis, p, synthDays)
			cut := len(base) / 3
			var f *tickflow.Feed
			var rest []tickflow.Bar
			reset := func() {
				if f != nil {
					f.Close()
				}
				var err error
				f, err = tickflow.NewFeed(sliceWalker{base[:cut]}, tickflow.FeedConfig{Key: keyAU, Calendar: auCal(b), Base: tickflow.MustIntraday(p),
					Rule: tickflow.AggTradingAxis, From: synthDays[1], To: base[cut-1].TradingDay, NoAutoWarmup: true, Lookback: 1})
				if err != nil {
					b.Fatal(err)
				}
				for f.Next() {
				}
				rest = rest[:0]
				for _, m := range one {
					if m.Ts >= base[cut-1].TsEnd {
						rest = append(rest, m)
					}
				}
			}
			reset()
			b.ReportAllocs()
			b.ResetTimer()
			i := 0
			for n := 0; n < b.N; n++ {
				if i == len(rest) {
					b.StopTimer()
					reset()
					i = 0
					b.StartTimer()
				}
				if _, err := f.Push(rest[i]); err != nil {
					b.Fatal(err)
				}
				i++
			}
		})
	}
}
