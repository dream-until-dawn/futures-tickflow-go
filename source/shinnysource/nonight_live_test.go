package shinnysource

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/embedded"
	"github.com/dream-until-dawn/futures-tickflow-go/source/pacing"
	"github.com/dream-until-dawn/futures-tickflow-go/store/segfile"
)

// v0.11 Q-b：停夜盘那一晚与节后首日的三张脸（design.md「v0.11 起手」甲的动因 A / B / C）—— 不注入照旧报错，注入 NoNightAfter 之后不再报。
// 场景（testCalendar，rb）：0904（周五）当「节前最后一个交易日」，那晚停夜盘 ⇒ 注入 NoNightAfter(20260904) 去掉的是 0907 的夜盘（0904 21:00–23:00）。

func noNightCal(t *testing.T) tickflow.Calendar {
	t.Helper()
	cal, err := embedded.New(testDays, embedded.NoNightAfter(20260904))
	if err != nil {
		t.Fatal(err)
	}
	return cal
}

// guard: A —— 那一晚 20:59 连上（推送里当前那根是 0904 14:59）、21:02:01 让时间走：
// 不注入 ⇒ 日历说 21:00 开了、一根都不来 ⇒ ErrSuspectedFreeze；注入 ⇒ 那一刻不在任何时段 ⇒ 不报。
// B —— 21:05 连上：不注入 ⇒ ErrStaleSnapshot（当前那根 14:59 早于 now − N）；注入 ⇒ 不在时段，启动自检不做 ⇒ 不报。
func TestNoNightEveningFaces(t *testing.T) {
	bar := at2(2026, 9, 4, 14, 59, 0, 0)
	for _, x := range []struct {
		name    string
		cal     tickflow.Calendar
		wantErr bool
	}{{"不注入", testCalendar(t), true}, {"注入 NoNightAfter(0904)", noNightCal(t), false}} {
		a := newLiveCore(x.cal, liveSym, 0, LiveOptions{})
		_, err := a.feed(at2(2026, 9, 4, 20, 59, 0, 0), kFrame(100, bar, 3000, ""))
		if err == nil {
			_, err = a.tick(at2(2026, 9, 4, 21, 2, 1, 0))
		}
		if x.wantErr != errors.Is(err, ErrSuspectedFreeze) || (!x.wantErr && err != nil) {
			t.Errorf("A %s：20:59 连上、21:02:01 tick ⇒ %v；应%s", x.name, err, map[bool]string{true: " Is ErrSuspectedFreeze", false: "不报"}[x.wantErr])
		}
		b := newLiveCore(x.cal, liveSym, 0, LiveOptions{})
		_, err = b.feed(at2(2026, 9, 4, 21, 5, 0, 0), kFrame(100, bar, 3000, ""))
		if x.wantErr != errors.Is(err, ErrStaleSnapshot) || (!x.wantErr && err != nil) {
			t.Errorf("B %s：21:05 连上 ⇒ %v；应%s", x.name, err, map[bool]string{true: " Is ErrStaleSnapshot", false: "不报"}[x.wantErr])
		}
	}
}

// guard: C（注入之后）—— src 截在节前 15:00，Feed 用注入过名单的日历 ⇒ PushFrom ＝ 节后首日 09:00；
// 节后首日早上 Live（同一个日历）从它起步 ⇒ 不报 ErrStartGap，09:00 / 09:01 照常交出、Push 成功。
// 不注入时 C 那一格（ErrStartGap）由 posthol_test.go 的 TestPostHolidayWithoutNoNight 钉着。
// ＋ 对照（评审方要求，「同一个日历」那一条）：Feed 注入、Live 用没注入的日历 ⇒ 那一晚 Live 照旧报冻结。
func TestNoNightPostHolidayMorningAndMixedCalendars(t *testing.T) {
	inj := noNightCal(t)
	plain := testCalendar(t)
	sym := tickflow.Symbol{Exchange: tickflow.SHFE, Product: "rb", YearMon: 2601}
	var bars []fakeBar
	for _, b := range minuteBars(t, plain, rbKey, 20260904, 20260908) {
		if ms := b.dt / 1e6; ms >= cst(2026, 9, 4, 21, 0) && ms < cst(2026, 9, 4, 23, 0) {
			continue // 那一晚停了
		}
		bars = append(bars, b)
	}
	fs := newFakeServer(t)
	fs.series[sym.Native()] = bars
	st, _, err := segfile.Open(t.TempDir(), tickflow.MustIntraday(1))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	syn, err := tickflow.NewSyncer(tickflow.SyncerConfig{Calendar: inj, Store: st, NewSource: honest(fs.config(inj, farFuture)),
		Pacer: pacing.NoPacing(), Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := syn.Sync(context.Background(), tickflow.SyncRequest{Symbol: sym, Period: tickflow.MustIntraday(1), From: 20260904}, cst(2026, 9, 4, 15, 10)); err != nil {
		t.Fatal(err)
	}
	f, err := tickflow.NewFeed(st, tickflow.FeedConfig{Key: rbKey, Calendar: inj, Base: tickflow.MustIntraday(1),
		Rule: tickflow.AggTradingAxis, From: 20260904, To: 20260904, NoAutoWarmup: true, Lookback: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for f.Next() {
	}
	from, err := f.PushFrom()
	if err != nil {
		t.Fatal(err)
	}
	if from != cst(2026, 9, 7, 9, 0) {
		t.Fatalf("注入之后，节前截止的 src 上 PushFrom ＝ %s，应为节后首日 09:00", fmtMs(from))
	}

	// 对照：Feed 注入、Live 没注入 ⇒ 那一晚 Live 按不存在的夜盘判冻结（与 A 同一张脸）；同样的起始格、Live 也注入 ⇒ 不报
	for _, x := range []struct {
		name    string
		cal     tickflow.Calendar
		wantErr bool
	}{{"Live 没注入", plain, true}, {"Live 也注入", inj, false}} {
		c := newLiveCore(x.cal, liveSym, from, LiveOptions{})
		_, err := c.feed(at2(2026, 9, 4, 20, 59, 0, 0), kFrame(100, at2(2026, 9, 4, 14, 59, 0, 0), 3000, ""))
		if err == nil {
			_, err = c.tick(at2(2026, 9, 4, 21, 2, 1, 0))
		}
		if x.wantErr != errors.Is(err, ErrSuspectedFreeze) || (!x.wantErr && err != nil) {
			t.Errorf("对照 Feed 注入、%s：那一晚 21:02:01 ⇒ %v；应%s", x.name, err, map[bool]string{true: " Is ErrSuspectedFreeze", false: "不报"}[x.wantErr])
		}
	}

	// C：节后首日早上，Live（注入过名单的日历）从 PushFrom 起步
	var clock atomic.Int64
	clock.Store(cst(2026, 9, 7, 9, 2) + 10000)
	lcfg := fs.config(inj, 0)
	lcfg.Now = clock.Load
	lc, err := New(lcfg)
	if err != nil {
		t.Fatal(err)
	}
	oldTick, oldBack := liveTick, liveBackoff
	liveTick, liveBackoff = 5*time.Millisecond, func(int) time.Duration { return 0 }
	defer func() { liveTick, liveBackoff = oldTick, oldBack }()
	idx := map[int64]int64{}
	for i, b := range bars {
		idx[b.dt/1e6] = int64(i)
	}
	data := map[string]any{}
	for _, ts := range []int64{cst(2026, 9, 7, 9, 0), cst(2026, 9, 7, 9, 1), cst(2026, 9, 7, 9, 2)} {
		b := bars[idx[ts]]
		data[itoa(idx[ts])] = map[string]any{"datetime": b.dt, "open": b.open, "high": b.high, "low": b.low, "close": b.close, "volume": b.volume, "close_oi": b.closeOI}
	}
	fs.push <- pushCmd{data: map[string]any{"klines": map[string]any{sym.Native(): map[string]any{minuteKey: map[string]any{"data": data}}}}}
	live, err := lc.Live(sym, from, LiveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	for _, want := range []int64{cst(2026, 9, 7, 9, 0), cst(2026, 9, 7, 9, 1)} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		b, err := live.Next(ctx)
		cancel()
		if err != nil || b.Ts != want {
			t.Fatalf("C（注入之后）：Live 交出 %s · %v，应为 %s（不报 ErrStartGap）", fmtMs(b.Ts), err, fmtMs(want))
		}
		if _, err := f.Push(b); err != nil {
			t.Fatalf("C（注入之后）：Push %s：%v", fmtMs(b.Ts), err)
		}
	}
}
