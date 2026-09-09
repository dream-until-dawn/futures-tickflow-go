// Package pacing 是【限流的闸门】。对应 docs/design.md §七之十一「丙二」。
//
// ⛔ **为什么闸门在这一层，而不在编排层** ——
//
//	Source.Bars(ctx, req) 是【一次调用】，而它内部发几次请求随源而变：
//	  sinasource   一个合约的全部历史 = **1 次**
//	  cffexsource  要 N 个交易日      = **N 次**
//	⇒ 站在编排上，这两者**长得一模一样** ⇒ 编排按不到那个闸门
//
// ⇒ 一个词（「限流」）盖住了两件事：**谁定【多快】，谁按【那个闸门】。**
// 策略确实属于调用方；**而闸门只能装在发请求的那一层。**
// ⇒ 用法：编排造好一个 Transport，经 `WithHTTPClient` 交给源 ——
// 那正是 `cffexsource` 注释里已经指的那条路。
//
// ⛔ **本包不设「不限流」的零值默认。** 忘了配的调用方会满速打中金所官网
// ≈2671 次（全量回补的请求数），而**没有任何东西会拦**。
// ⇒ 构造必须显式给；「我不限流」写成具名的 `NoPacing()` ——
// **一个具名的「我不限流」是一个看得见的选择；一个零值不是。**
package pacing

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Pacer 控制两次上游请求之间的最小间隔。
type Pacer interface {
	// Wait 在允许发下一个请求时返回。ctx 取消时返回 ctx.Err()。
	//
	// ⛔ 它收 ctx —— 全量回补是 2671 次请求、可能跑很久，
	// **一个不能被取消的等待，会把「用户按了 Ctrl-C」变成「再等十分钟」。**
	Wait(ctx context.Context) error
}

// ErrNoPacer 构造时没给 Pacer。
//
// ⚠️ 它是一个**导出的哨兵**，因为「忘了配限流」这件事调用方需要认得出来 ——
// 而不是收到一个泛泛的 "invalid argument"。
var ErrNoPacer = errors.New("pacing: 没给 Pacer——本包不设「不限流」的默认值；" +
	"真的不想限流请显式传 NoPacing()")

// fixed 是固定间隔。
type fixed struct {
	mu   sync.Mutex
	d    time.Duration
	next time.Time
	now  func() time.Time
}

// FixedDelay 造一个「两次请求至少隔 d」的 Pacer。
//
// ⛔ **d <= 0 被拒**，而不是当成「不限流」：
// 那正是本包要防的那件事 —— **一个算出来的 0 与一个想好了的「不限流」不可分辨**。
// 要不限流，写 `NoPacing()`。
func FixedDelay(d time.Duration) (Pacer, error) {
	if d <= 0 {
		return nil, fmt.Errorf("pacing: FixedDelay(%v) 不合法——"+
			"间隔必须为正；【真的不想限流请写 NoPacing()】，"+
			"因为一个算出来的 0 与一个想好了的「不限流」在这里不可分辨", d)
	}
	return &fixed{d: d, now: time.Now}, nil
}

func (f *fixed) Wait(ctx context.Context) error {
	f.mu.Lock()
	now := f.now()
	wait := time.Duration(0)
	if now.Before(f.next) {
		wait = f.next.Sub(now)
	}
	f.next = now.Add(wait + f.d)
	f.mu.Unlock()

	if wait <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

type noPacing struct{}

// NoPacing 是**具名的**「我不限流」。
//
// ⚠️ 它存在的全部理由是让那个决定**看得见**：
// 没有它，「不限流」只能靠传 nil 或传 0 来表达，而那两者与「忘了配」不可分辨。
func NoPacing() Pacer { return noPacing{} }

func (noPacing) Wait(ctx context.Context) error { return ctx.Err() }

// transport 把 Pacer 装在 http.RoundTripper 上。
type transport struct {
	next http.RoundTripper
	p    Pacer
}

// Transport 造一个会限流的 RoundTripper。
//
// next 为 nil 时用 http.DefaultTransport。
// ⛔ p 为 nil ⇒ **ErrNoPacer**，不是「默认不限流」。
func Transport(next http.RoundTripper, p Pacer) (http.RoundTripper, error) {
	if p == nil {
		return nil, ErrNoPacer
	}
	if next == nil {
		next = http.DefaultTransport
	}
	return &transport{next: next, p: p}, nil
}

func (t *transport) RoundTrip(r *http.Request) (*http.Response, error) {
	// ⛔ 用请求自己的 ctx —— 而不是 context.Background()：
	// 调用方取消了这次同步，等待也必须跟着结束。
	if err := t.p.Wait(r.Context()); err != nil {
		return nil, fmt.Errorf("pacing: 等待被取消：%w", err)
	}
	return t.next.RoundTrip(r)
}

// Client 是给调用方的便利：一个已经装好闸门的 http.Client。
//
// ⚠️ 它**不设超时默认** —— 超时是调用方的事，而本包只管节奏。
// 写下来是因为「便利构造函数顺手塞一个默认值」是本仓记过的那一类：
// **替使用者做了一个他不知道的决定。**
func Client(p Pacer) (*http.Client, error) {
	rt, err := Transport(nil, p)
	if err != nil {
		return nil, err
	}
	return &http.Client{Transport: rt}, nil
}
