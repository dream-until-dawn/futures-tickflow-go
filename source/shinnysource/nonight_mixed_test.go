package shinnysource

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/embedded"
	"github.com/dream-until-dawn/futures-tickflow-go/source/pacing"
	"github.com/dream-until-dawn/futures-tickflow-go/store/segfile"
)

// v0.11：contract.md「停夜盘名单」里「同一个日历」那一条 —— 三个组件各自注入 / 不注入，哪一张脸由谁的日历决定（读数先行：
// 2026-09-22 用临时测试把组合全跑了一遍，下面是收成断言的版本）。场景同 nonight_live_test.go：0904 晚停夜盘，节后首日 0907。
//
//	当晚（A）        只看 Live（Config.Calendar）的日历 —— 起始格取自注入或不注入的 Feed，结果相同
//	节后首日早上（C） 只看 Feed 的日历（它决定 PushFrom）—— Feed 不注入 ⇒ Live 注入也救不回来，照样 ErrStartGap
//	Syncer           同步截在节前 15:10 或跨过节后首日收盘，注入与否报告 / coverage / 根数逐项相同
//
// ⚠️ 射程：只量了这三条路（1m、AggTradingAxis、rb、一次停夜盘）；Feed 的多周期聚合、Syncer 在别的截止时刻，没量 ⇒ 契约仍要求「同一个日历」。

func nnMixedBars(t *testing.T) []fakeBar {
	t.Helper()
	var bars []fakeBar
	for _, b := range minuteBars(t, testCalendar(t), rbKey, 20260904, 20260908) {
		if ms := b.dt / 1e6; ms >= cst(2026, 9, 4, 21, 0) && ms < cst(2026, 9, 4, 23, 0) {
			continue // 那一晚停了
		}
		bars = append(bars, b)
	}
	return bars
}

// nnMorning：用 syncCal 同步到节前 15:10、用 feedCal 建 Feed 取 PushFrom、节后首日 09:02:10 用 liveCal 的 Live 从它起步，
// 交回 PushFrom、交出并 Push 成功的根、以及第一个错。
func nnMorning(t *testing.T, syncCal, feedCal, liveCal tickflow.Calendar) (from int64, got []int64, err error) {
	t.Helper()
	bars := nnMixedBars(t)
	sym := tickflow.Symbol{Exchange: tickflow.SHFE, Product: "rb", YearMon: 2601}
	fs := newFakeServer(t)
	fs.series[sym.Native()] = bars
	st, _, err := segfile.Open(t.TempDir(), tickflow.MustIntraday(1))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	syn, err := tickflow.NewSyncer(tickflow.SyncerConfig{Calendar: syncCal, Store: st, NewSource: honest(fs.config(syncCal, farFuture)),
		Pacer: pacing.NoPacing(), Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := syn.Sync(context.Background(), tickflow.SyncRequest{Symbol: sym, Period: tickflow.MustIntraday(1), From: 20260904}, cst(2026, 9, 4, 15, 10)); err != nil {
		t.Fatal(err)
	}
	f, err := tickflow.NewFeed(st, tickflow.FeedConfig{Key: rbKey, Calendar: feedCal, Base: tickflow.MustIntraday(1),
		Rule: tickflow.AggTradingAxis, From: 20260904, To: 20260904, NoAutoWarmup: true, Lookback: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for f.Next() {
	}
	if from, err = f.PushFrom(); err != nil {
		t.Fatal(err)
	}
	var clock atomic.Int64
	clock.Store(cst(2026, 9, 7, 9, 2) + 10000)
	lcfg := fs.config(liveCal, 0)
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
		return from, nil, err
	}
	defer live.Close()
	for i := 0; i < 2; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		b, err := live.Next(ctx)
		cancel()
		if err != nil {
			return from, got, err
		}
		if _, err := f.Push(b); err != nil {
			return from, got, fmt.Errorf("Push %s：%w", fmtMs(b.Ts), err)
		}
		got = append(got, b.Ts)
	}
	return from, got, nil
}

// guard: 节后首日早上（C）只看 Feed 的日历 —— Syncer × Feed × Live 各两种共 8 格：
// Feed 不注入 ⇒ PushFrom ＝ 0904 21:00 ⇒ 不管 Live、Syncer 注没注入，都 ErrStartGap（反向那一格：Feed 不注入、Live 注入，救不回来）；
// Feed 注入 ⇒ PushFrom ＝ 0907 09:00 ⇒ 不管 Live、Syncer 注没注入，09:00 / 09:01 照常交出、Push 成功。
func TestNoNightMorningDependsOnFeedCalendar(t *testing.T) {
	inj, plain := noNightCal(t), testCalendar(t)
	cals := []struct {
		name string
		cal  tickflow.Calendar
	}{{"不注入", plain}, {"注入", inj}}
	for _, s := range cals {
		for _, fc := range cals {
			for _, lc := range cals {
				name := fmt.Sprintf("Syncer %s · Feed %s · Live %s", s.name, fc.name, lc.name)
				from, got, err := nnMorning(t, s.cal, fc.cal, lc.cal)
				if fc.cal == plain {
					if from != cst(2026, 9, 4, 21, 0) || !errors.Is(err, ErrStartGap) || len(got) != 0 {
						t.Errorf("%s：PushFrom %s · 交出 %d 根 · %v；应为 09-04 21:00 · 0 根 · Is ErrStartGap", name, fmtMs(from), len(got), err)
					}
					continue
				}
				if from != cst(2026, 9, 7, 9, 0) || err != nil || len(got) != 2 || got[0] != cst(2026, 9, 7, 9, 0) || got[1] != cst(2026, 9, 7, 9, 1) {
					t.Errorf("%s：PushFrom %s · 交出 %d 根 · %v；应为 09-07 09:00 · 09:00 / 09:01 · 不报", name, fmtMs(from), len(got), err)
				}
			}
		}
	}
}

// guard: 当晚（A）只看 Live 的日历 —— 起始格取自不注入的 Feed（0904 21:00）或注入的 Feed（0907 09:00），× Live 两种：
// Live 不注入 ⇒ 21:02:01 ErrSuspectedFreeze（两个起始格都一样）；Live 注入 ⇒ 到 21:10 都不报（两个起始格都一样）。
func TestNoNightEveningDependsOnLiveCalendar(t *testing.T) {
	inj, plain := noNightCal(t), testCalendar(t)
	for _, from := range []int64{cst(2026, 9, 4, 21, 0), cst(2026, 9, 7, 9, 0)} {
		for _, lc := range []struct {
			name    string
			cal     tickflow.Calendar
			wantErr bool
		}{{"Live 不注入", plain, true}, {"Live 注入", inj, false}} {
			c := newLiveCore(lc.cal, liveSym, from, LiveOptions{})
			_, err := c.feed(at2(2026, 9, 4, 20, 59, 0, 0), kFrame(100, at2(2026, 9, 4, 14, 59, 0, 0), 3000, ""))
			for _, at := range []int64{at2(2026, 9, 4, 21, 2, 1, 0), at2(2026, 9, 4, 21, 10, 0, 0)} {
				if err == nil {
					_, err = c.tick(at)
				}
			}
			if lc.wantErr != errors.Is(err, ErrSuspectedFreeze) || (!lc.wantErr && err != nil) {
				t.Errorf("起始格 %s · %s：%v；应%s", fmtMs(from), lc.name, err, map[bool]string{true: " Is ErrSuspectedFreeze", false: "不报"}[lc.wantErr])
			}
		}
	}
}

// guard: Syncer 的日历在这两条路上不起作用 —— 同步截在节前 15:10、或跨过节后首日收盘（0907 15:10），注入与否：
// 报告（String）与 coverage 逐项相同；跨过收盘那一次，之后用任一日历建 Feed 读回来都不报错，PushFrom ＝ 节后首日晚上 21:00（契约里的临时出路）。
// 标定：「逐项相同」这把尺子要分得出不同 —— 换一个 Syncer 确实在意的日历（交易日表里少了最后一天 0908，「日历覆盖」随之变）⇒ 两个截止时刻读数都必须不同，否则上面的相同什么也没说明。
// ⚠️ 射程（评审方 2026-09-22）：标定造出的差别在【覆盖区间】，不在夜盘 ⇒ 它只证明这把尺子分得出覆盖上的不同，没证明分得出夜盘造成的不同；
// 这一格能说的只是「这两个截止时刻、这几项读数相同」，撑不起「Syncer 不用注入」—— 契约仍要求三个组件用同一个日历。
func TestNoNightSyncerCalendarIrrelevantHere(t *testing.T) {
	inj, plain := noNightCal(t), testCalendar(t)
	noLast, err := embedded.New([]tickflow.TradingDay{20260903, 20260904, 20260907})
	if err != nil {
		t.Fatal(err)
	}
	sym := tickflow.Symbol{Exchange: tickflow.SHFE, Product: "rb", YearMon: 2601}
	bars := nnMixedBars(t)
	for _, now := range []int64{cst(2026, 9, 4, 15, 10), cst(2026, 9, 7, 15, 10)} {
		var reads []string
		for _, sc := range []tickflow.Calendar{plain, inj, noLast} {
			fs := newFakeServer(t)
			fs.series[sym.Native()] = bars
			st, _, err := segfile.Open(t.TempDir(), tickflow.MustIntraday(1))
			if err != nil {
				t.Fatal(err)
			}
			syn, err := tickflow.NewSyncer(tickflow.SyncerConfig{Calendar: sc, Store: st, NewSource: honest(fs.config(sc, farFuture)),
				Pacer: pacing.NoPacing(), Timeout: 5 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			rep, err := syn.Sync(context.Background(), tickflow.SyncRequest{Symbol: sym, Period: tickflow.MustIntraday(1), From: 20260904}, now)
			if err != nil {
				t.Fatal(err)
			}
			reads = append(reads, fmt.Sprintf("%v · gaps %v · coverage %+v", rep, rep.Gaps, st.Coverage()))
			if now == cst(2026, 9, 7, 15, 10) && sc != noLast {
				for _, fc := range []tickflow.Calendar{plain, inj} {
					f, err := tickflow.NewFeed(st, tickflow.FeedConfig{Key: rbKey, Calendar: fc, Base: tickflow.MustIntraday(1),
						Rule: tickflow.AggTradingAxis, From: 20260904, To: 20260907, NoAutoWarmup: true, Lookback: 1})
					if err != nil {
						t.Fatal(err)
					}
					n := 0
					for f.Next() {
						n++
					}
					from, perr := f.PushFrom()
					if f.Err() != nil || perr != nil || n != 570 || from != cst(2026, 9, 7, 21, 0) {
						t.Errorf("跨过收盘同步之后读回来：Next %d 次 · Err %v · PushFrom %s %v；应为 570 次 · 不报 · 09-07 21:00", n, f.Err(), fmtMs(from), perr)
					}
					f.Close()
				}
			}
			st.Close()
		}
		if reads[0] != reads[1] {
			t.Errorf("截止 %s：Syncer 不注入 / 注入读数不同：\n  %s\n  %s", fmtMs(now), reads[0], reads[1])
		}
		if reads[2] == reads[0] {
			t.Errorf("标定：截止 %s，交易日表少了 0908 的日历读数也一样（%s）—— 这把尺子分不出不同", fmtMs(now), reads[2])
		}
	}
}
