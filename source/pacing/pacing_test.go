package pacing

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestNoDefaultPacer 本包**不设「不限流」的默认值** —— 这是它存在的第一条理由。
//
// ⛔ 忘了配的调用方会满速打上游 ≈2671 次（全量回补的请求数），而没有任何东西会拦。
func TestNoDefaultPacer(t *testing.T) {
	if _, err := Transport(nil, nil); !errors.Is(err, ErrNoPacer) {
		t.Fatalf("Transport(_, nil) 应当报 ErrNoPacer，实得 %v", err)
	}
	if _, err := Client(nil); !errors.Is(err, ErrNoPacer) {
		t.Fatalf("Client(nil) 应当报 ErrNoPacer，实得 %v", err)
	}
	// 对照：给了 Pacer 就该成 —— 否则上面两条也可能是「它对谁都报错」。
	if _, err := Transport(nil, NoPacing()); err != nil {
		t.Fatalf("给了 NoPacing() 应当成功，实得 %v", err)
	}
}

// TestFixedDelayRejectsZero `FixedDelay(0)` 被拒，而不是当成「不限流」。
//
// ⛔ **一个算出来的 0 与一个想好了的「不限流」不可分辨** ——
// 要不限流请写具名的 NoPacing()。
func TestFixedDelayRejectsZero(t *testing.T) {
	for _, d := range []time.Duration{0, -time.Second} {
		if _, err := FixedDelay(d); err == nil {
			t.Errorf("FixedDelay(%v) 应当被拒", d)
		}
	}
	if _, err := FixedDelay(time.Millisecond); err != nil {
		t.Fatalf("FixedDelay(1ms) 应当成功，实得 %v", err)
	}
}

// fakeClock 让「等了多久」可测，而不必真的 sleep。
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

// TestFixedDelayPacesSecondCall 第二次调用要被推到 d 之后。
//
// ⚠️ 用假时钟读**它算出来的下一次可发时刻**，而不是量墙钟 ——
// 量墙钟的测试会因为机器负载而偶发红，**而偶发红的测试迟早被加 skip**。
func TestFixedDelayPacesSecondCall(t *testing.T) {
	p, err := FixedDelay(100 * time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	f := p.(*fixed)
	clk := &fakeClock{t: time.Unix(0, 0)}
	f.now = clk.now

	// 第一次：不必等。
	start := time.Now()
	if err := f.Wait(context.Background()); err != nil {
		t.Fatalf("第一次不该出错：%v", err)
	}
	if el := time.Since(start); el > 50*time.Millisecond {
		t.Errorf("第一次等了 %v —— 它不该等", el)
	}
	if got := f.next.Sub(clk.t); got != 100*time.Millisecond {
		t.Fatalf("第一次之后，下一次可发时刻应当是 +100ms，实得 +%v", got)
	}

	// 第二次：时钟没动 ⇒ 它应当算出还要等 100ms。
	// 把时钟推到那一刻，等待就不必真的发生。
	clk.t = f.next
	if err := f.Wait(context.Background()); err != nil {
		t.Fatalf("第二次不该出错：%v", err)
	}
	if got := f.next.Sub(clk.t); got != 100*time.Millisecond {
		t.Errorf("第二次之后，下一次可发时刻应当再 +100ms，实得 +%v", got)
	}
}

// TestWaitIsCancellable 等待必须能被 ctx 取消。
//
// ⛔ 全量回补是 2671 次请求、可能跑很久 ——
// **一个不能被取消的等待，会把「用户按了 Ctrl-C」变成「再等十分钟」。**
func TestWaitIsCancellable(t *testing.T) {
	p, err := FixedDelay(time.Hour) // 足够长：真等就会挂住测试
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Wait(context.Background()); err != nil {
		t.Fatalf("第一次不该等，也不该出错：%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() { done <- p.Wait(ctx) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("期望 context.Canceled，实得 %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("等待没有被取消 —— 它挂住了")
	}
}

// TestTransportPassesThroughAndPaces 装了闸门之后请求照样到得了对端。
//
// ⛔ 少了这一条，上面那些「该拒的都拒了」也可能是「它把什么都拒了」。
func TestTransportPassesThroughAndPaces(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	c, err := Client(NoPacing())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		resp, err := c.Get(srv.URL)
		if err != nil {
			t.Fatalf("第 %d 次请求失败：%v", i, err)
		}
		resp.Body.Close()
	}
	if hits != 3 {
		t.Errorf("对端收到 %d 次，期望 3 次", hits)
	}
}

// TestTransportRespectsRequestContext 闸门用的是【请求自己的 ctx】，不是 Background。
//
// ⛔ 用 Background 的话，调用方取消了这次同步，**等待仍然会走完** ——
// 而那正是「不能被取消」的另一种长法。
func TestTransportRespectsRequestContext(t *testing.T) {
	p, err := FixedDelay(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	rt, err := Transport(http.DefaultTransport, p)
	if err != nil {
		t.Fatal(err)
	}
	// 先耗掉「第一次不必等」那一次。
	req0, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:1/", nil)
	_, _ = rt.RoundTrip(req0) // 连不上没关系，闸门已经过了

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:1/", nil)
	done := make(chan error, 1)
	go func() { _, e := rt.RoundTrip(req); done <- e }()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("期望 context.Canceled，实得 %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RoundTrip 没有跟着请求的 ctx 结束 —— 闸门用的多半是 Background")
	}
}

// TestFixedDelayHonoursTheGivenDelay 间隔必须等于**调用方给的那个 d** —— 两个值各验一次。
//
// 🔴 **它补的是一个真的、今天开着的洞**（评审方 2026-09-09 量出，我复现，读数一致）：
//
//	把 `f.next = now.Add(wait + f.d)` 里的 `f.d` 换成写死的 100ms
//	（`f.d` 存下来但再没人读，Go 不报错，编译过）
//	⇒ **全仓 0 红** —— 也就是 `FixedDelay(1*time.Second)` 实际按 100ms 发，七个包全绿
//
// ⇒ 成因就在上面那条测试里：`TestFixedDelayPacesSecondCall` **只用了一个 d = 100ms**，
// 于是它停在阶梯第三级 —— **「写死成 100ms」的实现恰好相等，照绿。**
//
// ⛔ 而这一格是本仓自己早就记过的：`tools/probe/README.md` §2 ——
// **「1 个已知值挡不住『恰好返回那个值』；2 个不同的已知值挡得住任何常量。」**
// ⇒ 判据：**每次觉得「这条断言还能更强」，先去仓里搜有没有记过那一格。**
//
// ⚠️ 两个值**都不是 100ms**：否则这条测试自己也会被那个突变绕过去。
// ⚠️ 而它留在**本包**里，理由是「够不到」是一句带地址的话 ——
// 根包的测试够不到 `f.now`，**而本包的测试够得到**（注入口就在上面那条测试里）。
// ⇒ 不必改 API，也不必量墙钟。
func TestFixedDelayHonoursTheGivenDelay(t *testing.T) {
	for _, d := range []time.Duration{70 * time.Millisecond, 230 * time.Millisecond} {
		t.Run(d.String(), func(t *testing.T) {
			p, err := FixedDelay(d)
			if err != nil {
				t.Fatal(err)
			}
			f := p.(*fixed)
			clk := &fakeClock{t: time.Unix(0, 0)}
			f.now = clk.now

			// 第一次：不必等，而它把「下一次可发时刻」推到 +d。
			if err := f.Wait(context.Background()); err != nil {
				t.Fatal(err)
			}
			if got := f.next.Sub(clk.t); got != d {
				t.Fatalf("第一次之后，下一次可发时刻应当是 +%v，实得 +%v\n"+
					"  ⇒ 它用的不是调用方给的那个 d", d, got)
			}
			// 第二次：把时钟推到那一刻，它应当再推 +d。
			clk.t = f.next
			if err := f.Wait(context.Background()); err != nil {
				t.Fatal(err)
			}
			if got := f.next.Sub(clk.t); got != d {
				t.Errorf("第二次之后应当再 +%v，实得 +%v", d, got)
			}
		})
	}
}
