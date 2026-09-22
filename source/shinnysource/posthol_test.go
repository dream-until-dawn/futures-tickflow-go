package shinnysource

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/source/pacing"
	"github.com/dream-until-dawn/futures-tickflow-go/store/segfile"
)

// v0.11 Q-0：v0.10 的日历看不见停夜盘时，节后首日怎么办（design.md「v0.11 起手」甲的动因 C 与临时出路）。
//
// 场景（testCalendar，rb）：0904（周五）是「节前最后一个交易日」，那晚交易所停夜盘 —— 而日历照样给 0907 一段 0904 21:00–23:00 的夜盘；
// 数据里没有这段根（等于那晚停了）。0907 是「节后首日」，0908 照常（它的夜盘 0907 21:00–23:00 真实存在）。

// guard: C 那一张脸 —— src 截在节前 15:00 ⇒ PushFrom 给的是那段并不存在的夜盘的第一分钟 ⇒ 节后首日早上照契约从 PushFrom 起步 ⇒ ErrStartGap；
// 临时出路 —— 节后首日盘中 Sync 拉不到当天（显式 To＝当天报错 · To＝0 截到节前）⇒ 那一天不接实时；它收盘后 Sync（To＝0），
// 第二个交易日从 PushFrom 起步 ⇒ Live 与 Push 照常接上。
func TestPostHolidayWithoutNoNight(t *testing.T) {
	cal := testCalendar(t)
	sym := tickflow.Symbol{Exchange: tickflow.SHFE, Product: "rb", YearMon: 2601}
	all := minuteBars(t, cal, rbKey, 20260904, 20260908)
	var bars []fakeBar
	for _, b := range all {
		ms := b.dt / 1e6
		if ms >= cst(2026, 9, 4, 21, 0) && ms < cst(2026, 9, 4, 23, 0) {
			continue // 0904 晚上停了夜盘：这段根不存在
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
	syn, err := tickflow.NewSyncer(tickflow.SyncerConfig{Calendar: cal, Store: st, NewSource: honest(fs.config(cal, farFuture)),
		Pacer: pacing.NoPacing(), Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	sync := func(label string, to tickflow.TradingDay, now int64) (tickflow.SyncReport, error) {
		t.Helper()
		rep, err := syn.Sync(context.Background(), tickflow.SyncRequest{Symbol: sym, Period: tickflow.MustIntraday(1), From: 20260904, To: to}, now)
		t.Logf("%s：err=%v · Bars=%d · Halt=%q · Complete=%v · Incidents=%q", label, err, rep.Bars, rep.Halt, rep.Complete(), rep.Incidents())
		return rep, err
	}
	feedFrom := func(to tickflow.TradingDay) (*tickflow.Feed, int64) {
		t.Helper()
		f, err := tickflow.NewFeed(st, tickflow.FeedConfig{Key: rbKey, Calendar: cal, Base: tickflow.MustIntraday(1),
			Rule: tickflow.AggTradingAxis, From: 20260904, To: to, NoAutoWarmup: true, Lookback: 1})
		if err != nil {
			t.Fatalf("NewFeed(To=%s)：%v", to, err)
		}
		for f.Next() {
		}
		if err := f.Err(); err != nil {
			t.Fatal(err)
		}
		from, err := f.PushFrom()
		if err != nil {
			t.Fatalf("PushFrom：%v", err)
		}
		return f, from
	}

	// 一  节前收盘后 Sync，节后首日早上照契约起步 ⇒ C
	if _, err := sync("节前收盘后 To=0", 0, cst(2026, 9, 4, 15, 10)); err != nil {
		t.Fatal(err)
	}
	f0, from0 := feedFrom(20260904)
	f0.Close()
	if from0 != cst(2026, 9, 4, 21, 0) {
		t.Fatalf("前提：节前截止的 src 上 PushFrom ＝ %s，应为那段并不存在的夜盘的第一分钟 09-04 21:00", fmtMs(from0))
	}
	var clock atomic.Int64
	clock.Store(cst(2026, 9, 7, 9, 2) + 10000)
	lcfg := fs.config(cal, 0)
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
	push := func(ts ...int64) {
		var ids []int64
		for _, x := range ts {
			ids = append(ids, idx[x])
		}
		data := map[string]any{}
		for _, id := range ids {
			b := bars[id]
			data[itoa(id)] = map[string]any{"datetime": b.dt, "open": b.open, "high": b.high, "low": b.low, "close": b.close, "volume": b.volume, "close_oi": b.closeOI}
		}
		fs.push <- pushCmd{data: map[string]any{"klines": map[string]any{sym.Native(): map[string]any{minuteKey: map[string]any{"data": data}}}}}
	}
	live0, err := lc.Live(sym, from0, LiveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	push(cst(2026, 9, 7, 9, 0), cst(2026, 9, 7, 9, 1), cst(2026, 9, 7, 9, 2))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_, err = live0.Next(ctx)
	cancel()
	live0.Close()
	fs.push <- pushCmd{drop: true} // 假服务器那一侧 live0 的处理协程还在等指令（写失败才会走）⇒ 先喂它一条断开，免得它吃掉下一帧
	if !errors.Is(err, ErrStartGap) || !strings.Contains(err.Error(), "一根都没有") {
		t.Fatalf("C：节后首日早上从 PushFrom 起步：%v，应 Is ErrStartGap（历史通道一根都补不回来）", err)
	}

	// 二  节后首日盘中：显式 To＝当天报错 · To＝0 截到节前（拉不到当天）
	if _, err := sync("节后首日 10:00 显式 To=0907", 20260907, cst(2026, 9, 7, 10, 0)); err == nil || !strings.Contains(err.Error(), "还没收盘") {
		t.Errorf("节后首日盘中显式 To＝当天：%v，应报「还没收盘」", err)
	}
	if rep, err := sync("节后首日 10:00 To=0", 0, cst(2026, 9, 7, 10, 0)); err != nil || rep.Requested[1] != 20260904 {
		t.Errorf("节后首日盘中 To＝0：Requested %v · %v，应截到 20260904", rep.Requested, err)
	}

	// 三  节后首日收盘后 Sync（To＝0）⇒ 当天进库；第二个交易日从 PushFrom 起步 ⇒ 正是 0907 21:00（0908 的真实夜盘）
	rep, err := sync("节后首日收盘后 To=0", 0, cst(2026, 9, 7, 15, 10))
	if err != nil || rep.Requested[1] != 20260907 {
		t.Fatalf("节后首日收盘后 To＝0：Requested %v · %v，应到 20260907", rep.Requested, err)
	}
	f1, from1 := feedFrom(20260907)
	defer f1.Close()
	if from1 != cst(2026, 9, 7, 21, 0) {
		t.Fatalf("收盘后同步过的 src 上 PushFrom ＝ %s，应为 09-07 21:00（0908 的夜盘，真实存在）", fmtMs(from1))
	}
	clock.Store(cst(2026, 9, 7, 21, 3) + 10000)
	live1, err := lc.Live(sym, from1, LiveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer live1.Close()
	push(cst(2026, 9, 7, 21, 0), cst(2026, 9, 7, 21, 1), cst(2026, 9, 7, 21, 2), cst(2026, 9, 7, 21, 3))
	for _, want := range []int64{cst(2026, 9, 7, 21, 0), cst(2026, 9, 7, 21, 1), cst(2026, 9, 7, 21, 2)} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		b, err := live1.Next(ctx)
		cancel()
		if err != nil || b.Ts != want {
			t.Fatalf("次日起步：Live 交出 %s · %v，应为 %s", fmtMs(b.Ts), err, fmtMs(want))
		}
		if _, err := f1.Push(b); err != nil {
			t.Fatalf("次日起步：Push %s：%v", fmtMs(b.Ts), err)
		}
	}
}

func itoa(i int64) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
