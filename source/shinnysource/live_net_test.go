package shinnysource

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// v0.10 P-c：Live（live.go）走本地假服务器（fake_test.go 的推送模式 ＋ 历史窗口）的测试。不连外网。
//
// 数据：9/4 一整天的 1m（含 9/3 晚的夜盘），id ＝ 下标；推送帧与历史窗口取自同一张表（两条通道同一套编号，推论，实盘要验）。
//
// ⚠️ 这些测试走真实计时（liveTick 5ms ＋ 协程调度），所以突变下【红哪几格】不稳定（评审方 2026-09-21 要求写明）：
// 突变 N16（live.go 第一次连上时不把冻结计时的起点设成连上那一刻，即删掉 else 分支里的 `l.core.lastChg = l.c.cfg.Now()`）
// 整模块每次都红，而红格集合每次不同 —— 2026-09-22 共四次（99428ed 上我跑 3 次、5d999fc 上评审方跑 1 次）：每次红 6–7 个，
// 名单四次各不相同，几次都红的交集跑一次就缩一次 ⇒ 这里不列名单（列了也会过期）。未施加时稳定全绿（评审方在 5d999fc 上 shinnysource 包 -count=10 全 ok）。
// ⇒ 引用这条突变时写「每次都红、红格集合不稳定」，不要写死红哪几格；判它有没有守住，看「红 ≥ 1」而不是看名单相等。

type netRig struct {
	fs    *fakeServer
	bars  []fakeBar
	clock atomic.Int64
	live  *Live
}

// newNetRig：起始格 start（毫秒，必须是表里某根的开盘）。
func newNetRig(t *testing.T, start int64, opt LiveOptions) *netRig {
	t.Helper()
	oldTick, oldBack := liveTick, liveBackoff
	liveTick, liveBackoff = 5*time.Millisecond, func(int) time.Duration { return 0 }
	t.Cleanup(func() { liveTick, liveBackoff = oldTick, oldBack })
	cal := testCalendar(t)
	r := &netRig{fs: newFakeServer(t), bars: minuteBars(t, cal, rbKey, 20260904, 20260904)}
	r.fs.series[liveSym.Native()] = r.bars
	cfg := r.fs.config(cal, 0)
	cfg.Now = r.clock.Load
	cl, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if r.live, err = cl.Live(liveSym, start, opt); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.live.Close() })
	return r
}

// id：开盘时刻 ts 那根的 id。
func (r *netRig) id(t *testing.T, ts int64) int64 {
	t.Helper()
	for i, b := range r.bars {
		if b.dt == ts*1e6 {
			return int64(i)
		}
	}
	t.Fatalf("表里没有开盘 %s 的根", fmtTs(ts))
	return 0
}

func (r *netRig) ts(id int64) int64 { return r.bars[id].dt / 1e6 }

// send 推一帧，带 ids 这几根（值取自表；over 里给的 id 换成那个收盘价）。
func (r *netRig) send(ids []int64, over map[int64]float64) {
	data := map[string]any{}
	for _, id := range ids {
		b := r.bars[id]
		c := b.close
		if v, ok := over[id]; ok {
			c = v
		}
		data[strconv.FormatInt(id, 10)] = map[string]any{"datetime": b.dt, "open": b.open, "high": b.high, "low": b.low, "close": c,
			"volume": b.volume, "close_oi": b.closeOI}
	}
	r.fs.push <- pushCmd{data: map[string]any{"klines": map[string]any{liveSym.Native(): map[string]any{minuteKey: map[string]any{"data": data}}}}}
}

func (r *netRig) drop() { r.fs.push <- pushCmd{drop: true} }

func span(a, b int64) []int64 {
	var out []int64
	for id := a; id <= b; id++ {
		out = append(out, id)
	}
	return out
}

// next 取 n 根，断言开盘时刻依次是 want。
func (r *netRig) next(t *testing.T, want ...int64) {
	t.Helper()
	for _, w := range want {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		b, err := r.live.Next(ctx)
		cancel()
		if err != nil || b.Ts != w {
			t.Fatalf("Next：开盘 %s · %v；应为 %s", fmtTs(b.Ts), err, fmtTs(w))
		}
	}
}

func (r *netRig) nextErr(t *testing.T) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	b, err := r.live.Next(ctx)
	if err == nil {
		t.Fatalf("Next 交出 %s，应报错", fmtTs(b.Ts))
	}
	return err
}

func (r *netRig) counts() (push, handshakes int) {
	r.fs.mu.Lock()
	defer r.fs.mu.Unlock()
	return r.fs.pushConns, r.fs.handshakes
}

// guard: L12 联网 —— 推送窗口从 9:35 起、起始格 9:30 ⇒ 走历史通道补 [9:30, 9:35)，然后紧接交出 9:30 … 9:36（9:37 是当前根，不交）。
func TestLiveNetBackfillsFromHistory(t *testing.T) {
	m := at2(2026, 9, 4, 9, 30, 0, 0)
	r := newNetRig(t, m, LiveOptions{})
	i := r.id(t, m)
	r.clock.Store(r.ts(i+7) + 10000)
	r.send(span(i+5, i+7), nil)
	var want []int64
	for k := int64(0); k <= 6; k++ {
		want = append(want, r.ts(i+k))
	}
	r.next(t, want...)
	if p, h := r.counts(); p != 1 || h != 2 {
		t.Errorf("推送连接 %d 条、握手 %d 次；应为 1 条推送 ＋ 1 次历史（共 2 次握手）", p, h)
	}
}

// guard: L4 联网 —— 段末根（10:14）没有 k＋1 可等：靠 Next 里的计时让时间走，本机 ≥ 10:15 ＋ G 才交。
func TestLiveNetSegmentEndByTicker(t *testing.T) {
	m := at2(2026, 9, 4, 10, 14, 0, 0)
	r := newNetRig(t, m, LiveOptions{})
	r.clock.Store(at2(2026, 9, 4, 10, 15, 3, 0))
	r.send([]int64{r.id(t, m)}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	b, err := r.live.Next(ctx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("10:15:03：Next 给了 %s · %v；应等到 ctx 到期（G ＝ 4 秒）", fmtTs(b.Ts), err)
	}
	r.clock.Store(at2(2026, 9, 4, 10, 15, 4, 0))
	r.next(t, m)
}

// guard: L8 —— 断线后重连，新连接的窗口盖住断点（重发了已交出的 i＋1，值不变）⇒ 从 i＋2 紧接着交，不重不漏。
func TestLiveNetReconnectResumes(t *testing.T) {
	m := at2(2026, 9, 4, 9, 30, 0, 0)
	r := newNetRig(t, m, LiveOptions{})
	i := r.id(t, m)
	r.clock.Store(r.ts(i+2) + 10000)
	r.send(span(i, i+2), nil)
	r.drop()
	r.next(t, r.ts(i), r.ts(i+1))
	r.clock.Store(r.ts(i+4) + 10000)
	r.send(span(i+1, i+4), nil)
	r.next(t, r.ts(i+2), r.ts(i+3))
	if p, _ := r.counts(); p != 2 {
		t.Errorf("推送连接 %d 条，应为 2（断一次、重连一次）", p)
	}
}

// guard: L8 ＋ L12 —— 重连后新窗口从 i＋6 起（断开期间收盘的 i＋2 … i＋5 不在推送里）⇒ 走历史通道补上、紧接着交。
// （FreezeN 放到 1 小时：假时钟在断开后一步跳 6 分钟，冻结判定不是这一格要验的）
func TestLiveNetReconnectBackfills(t *testing.T) {
	m := at2(2026, 9, 4, 9, 30, 0, 0)
	r := newNetRig(t, m, LiveOptions{FreezeN: time.Hour})
	i := r.id(t, m)
	r.clock.Store(r.ts(i+2) + 10000)
	r.send(span(i, i+2), nil)
	r.drop()
	r.next(t, r.ts(i), r.ts(i+1))
	r.clock.Store(r.ts(i+8) + 10000)
	r.send(span(i+6, i+8), nil)
	var want []int64
	for k := int64(2); k <= 7; k++ {
		want = append(want, r.ts(i+k))
	}
	r.next(t, want...)
}

// guard: L5 跨重连 —— 新连接重发了已交出的 i＋1、值变了 ⇒ ErrCorrectedAfterDelivery（交出过的根在重连时留着，才查得到）；
// 报错之后 Next 一直返回同一个错。
func TestLiveNetCorrectionAcrossReconnect(t *testing.T) {
	m := at2(2026, 9, 4, 9, 30, 0, 0)
	r := newNetRig(t, m, LiveOptions{})
	i := r.id(t, m)
	r.clock.Store(r.ts(i+2) + 10000)
	r.send(span(i, i+2), nil)
	r.drop()
	r.next(t, r.ts(i), r.ts(i+1))
	r.clock.Store(r.ts(i+3) + 10000)
	r.send(span(i+1, i+3), map[int64]float64{i + 1: 9999})
	err := r.nextErr(t)
	if !errors.Is(err, ErrCorrectedAfterDelivery) || !strings.Contains(err.Error(), "C 9999") {
		t.Fatalf("重连后 i＋1 改值：%v，应 Is ErrCorrectedAfterDelivery、报文带 C 9999", err)
	}
	if again := r.nextErr(t); again != err {
		t.Errorf("再调 Next：%v，应返回同一个错", again)
	}
}

// guard: L8 重连失败 —— 断开后行情握手一律 503 ⇒ 重连 Reconnects 次后 ErrDisconnected；已交出的根照常拿到。
func TestLiveNetReconnectGivesUp(t *testing.T) {
	m := at2(2026, 9, 4, 9, 30, 0, 0)
	r := newNetRig(t, m, LiveOptions{Reconnects: 2})
	i := r.id(t, m)
	r.clock.Store(r.ts(i+2) + 10000)
	r.send(span(i, i+2), nil)
	r.next(t, r.ts(i), r.ts(i+1))
	r.fs.mu.Lock()
	r.fs.mdDown = true
	r.fs.mu.Unlock()
	_, h0 := r.counts()
	r.drop()
	err := r.nextErr(t)
	if !errors.Is(err, ErrDisconnected) || !strings.Contains(err.Error(), "重连 2 次") {
		t.Fatalf("重连都失败：%v，应 Is ErrDisconnected、报文带「重连 2 次」", err)
	}
	if _, h := r.counts(); h-h0 != 2 {
		t.Errorf("断开后握手 %d 次，应为 2（Reconnects）", h-h0)
	}
}

// guard: 帧来得比 liveTick 密时时间照样走 —— 冻结判定只在 tick 里做（feed 只顺带交根）：9:30 那根之后
// K 线再没动过、只连着来只带报价的帧（间隔远小于 liveTick），本机已到 9:33（> N）⇒ 照样报 ErrSuspectedFreeze。
// 「只在计时器响时 tick ＋ 每轮重置一个 Timer」的写法在这里一直等不到它响 ⇒ 冻结永远不判（这一格就是为它写的；
// 现在每次醒来都 tick，计时器换成每轮一个 Timer 已是等价写法 ⇒ 突变改为「醒来之后不 tick」）。
// （段末根不靠这一条：帧里的 feed 会拿收到时刻去 deliver，帧再密也交得出去。）
func TestLiveNetTicksUnderFrameFlood(t *testing.T) {
	m := at2(2026, 9, 4, 9, 30, 0, 0)
	r := newNetRig(t, m, LiveOptions{})
	liveTick = 50 * time.Millisecond
	r.clock.Store(m + 10000)
	r.send([]int64{r.id(t, m)}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	if b, err := r.live.Next(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("前提：9:30 那根是当前根、不交；Next 给了 %s · %v", fmtTs(b.Ts), err)
	}
	cancel()
	now := at2(2026, 9, 4, 9, 33, 0, 0)
	r.clock.Store(now)
	q := map[string]any{"quotes": map[string]any{liveSym.Native(): map[string]any{"datetime": fmtMs(now) + "000"}}}
	// 帧一直来到这一格结束（不能先停：停了之后哪种写法的计时都会响 —— 第一版发 900 帧就停，Timer 写法照样绿，突变实测）
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			select {
			case <-done:
				return
			case r.fs.push <- pushCmd{data: q}:
			}
			time.Sleep(time.Millisecond)
		}
	}()
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := r.live.Next(ctx); !errors.Is(err, ErrSuspectedFreeze) {
		t.Fatalf("帧不断、K 线不动、本机已过 N：1 秒内 %v，应 ErrSuspectedFreeze（liveTick 50 ms）", err)
	}
}

// —— ctx 到期与背压（评审方 09-21 读代码推出的三处，先红后修）——

// guard: ctx 在重连退避期间到期 ⇒ 不算 Live 的错；换一个 ctx 再调 Next，照样按「换了连接」接上 lastID＋1
// （liveCore 得知道换了连接 —— 这件事绑在连接上，不绑在重连循环里：cff2188 绑在 redial 的循环里，退避时 ctx 一到期就跳过了）。
func TestLiveNetCtxExpiresDuringBackoff(t *testing.T) {
	m := at2(2026, 9, 4, 9, 30, 0, 0)
	r := newNetRig(t, m, LiveOptions{FreezeN: time.Hour})
	liveBackoff = func(int) time.Duration { return 300 * time.Millisecond }
	i := r.id(t, m)
	r.clock.Store(r.ts(i+2) + 10000)
	r.send(span(i, i+2), nil)
	r.drop()
	r.next(t, r.ts(i), r.ts(i+1))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	_, err := r.live.Next(ctx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("退避期间 ctx 到期：%v，应返回 ctx 的错", err)
	}
	r.clock.Store(r.ts(i+8) + 10000)
	r.send(span(i+6, i+8), nil)
	var want []int64
	for k := int64(2); k <= 7; k++ {
		want = append(want, r.ts(i+k))
	}
	r.next(t, want...)
}

// guard: ctx 在补齐（历史通道）期间到期 ⇒ 返回 ctx 的错、不粘住；换一个 ctx 再调 Next，补齐照常完成、不重不漏。
func TestLiveNetCtxExpiresDuringFill(t *testing.T) {
	m := at2(2026, 9, 4, 9, 30, 0, 0)
	r := newNetRig(t, m, LiveOptions{})
	i := r.id(t, m)
	r.fs.mu.Lock()
	r.fs.histDelay = 400 * time.Millisecond
	r.fs.mu.Unlock()
	r.clock.Store(r.ts(i+7) + 10000)
	r.send(span(i+5, i+7), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	b, err := r.live.Next(ctx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("补齐期间 ctx 到期：%s · %v，应返回 ctx 的错", fmtTs(b.Ts), err)
	}
	r.fs.mu.Lock()
	r.fs.histDelay = 0
	r.fs.mu.Unlock()
	var want []int64
	for k := int64(0); k <= 6; k++ {
		want = append(want, r.ts(i+k))
	}
	r.next(t, want...)
}

// guard: 调用方一段时间不取（策略在算）⇒ 读协程照读、照发 peek，帧上的本机收到时刻是真的收到时刻。
// 本机在 T1 收到 200 帧报价（quote.datetime ＝ T1，D ≈ 0），调用方到 T1 ＋ 3 秒才来取：
// 时刻若被推迟到取的那一刻，D 整段抬到 3000 ms ＞ G/2 ⇒ 一次假的 ErrClockSkew（cff2188：通道容量 64 满了读协程就停）。
func TestLiveNetSlowConsumerKeepsRecvTime(t *testing.T) {
	m := at2(2026, 9, 4, 9, 30, 0, 0)
	r := newNetRig(t, m, LiveOptions{})
	i := r.id(t, m)
	t1 := m + 60500
	r.clock.Store(t1)
	r.send(span(i, i+1), nil)
	r.next(t, m) // 连上、交出 9:30；之后调用方「去算别的」：不调 Next
	q := map[string]any{"quotes": map[string]any{liveSym.Native(): map[string]any{"datetime": fmtMs(t1) + "000"}}}
	for k := 0; k < 200; k++ {
		r.fs.push <- pushCmd{data: q}
	}
	r.send([]int64{i + 2}, nil)
	time.Sleep(300 * time.Millisecond) // 服务端在 T1 这段时间里把帧全写出去了
	r.clock.Store(t1 + 3000)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if b, err := r.live.Next(ctx); err != nil || b.Ts != r.ts(i+1) {
		t.Fatalf("调用方晚 3 秒来取：%s · %v，应交出 9:31、不报（帧上的时刻是收到时刻，不是取的时刻）", fmtTs(b.Ts), err)
	}
}

// guard: 读协程不阻塞的代价是内存 ⇒ 收下没处理的帧超过 liveQueueMax ⇒ ErrConsumerStalled（明确报，不静默丢帧），并粘住。
func TestLiveNetStalledConsumerIsAnError(t *testing.T) {
	m := at2(2026, 9, 4, 9, 30, 0, 0)
	r := newNetRig(t, m, LiveOptions{})
	old := liveQueueMax
	liveQueueMax = 20
	t.Cleanup(func() { liveQueueMax = old })
	i := r.id(t, m)
	t1 := m + 60500
	r.clock.Store(t1)
	r.send(span(i, i+1), nil)
	r.next(t, m)
	q := map[string]any{"quotes": map[string]any{liveSym.Native(): map[string]any{"datetime": fmtMs(t1) + "000"}}}
	for k := 0; k < 50; k++ {
		r.fs.push <- pushCmd{data: q}
	}
	time.Sleep(300 * time.Millisecond)
	err := r.nextErr(t)
	if !errors.Is(err, ErrConsumerStalled) || !strings.Contains(err.Error(), "已收下 20 帧") {
		t.Fatalf("调用方不取、帧超过上限 20：%v，应 Is ErrConsumerStalled、报文带「已收下 20 帧」", err)
	}
	if again := r.nextErr(t); again != err {
		t.Errorf("再调 Next：%v，应返回同一个错", again)
	}
}
