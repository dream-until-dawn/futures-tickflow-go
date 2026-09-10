package cffexsource

import (
	"context"
	"net/http"
	"testing"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
)

// countingRT 数「经过它的请求有几次」。
type countingRT struct {
	next http.RoundTripper
	n    int
}

func (r *countingRT) RoundTrip(q *http.Request) (*http.Response, error) {
	r.n++
	return r.next.RoundTrip(q)
}

// TestDeclaredClientUseMatchesBehaviour 把 `Caps().ClientUse` 这句**声明**，
// 和「`Bars` 到底走不走那个 client」这个**行为**绑在一起。
//
// 🔴 **它存在的理由是一处【承诺搬家】**：
// 编排那一层为了把「不适用」与「被绕开」分开，改成由**源自己**声明 `ClientUse`。
// ⇒ 而那一步把一件可核的事换成了**源的一句话**：
//
//	一个声明 `ClientUseNone` 却照样用自己的 client 发 HTTP 的源，
//	绕过编排的闸门而**报告一个字不说**（编排侧实测：闸门 0 次 · Complete()=true）
//
// ⛔ **而那件事在编排那一层查不了** —— 它看得见的只有「计数是 0」，
// 而 `ClientUseNone` 正好说这是预期：**「说谎」与「不适用」在那一层同形。**
//
// ⇒ 于是又是那条：**「够不到」是一句带地址的话。**
// 不是不可核，是**核的位置在别处** —— 而别处就是这里：**源自己的测试**。
// 这一层看得见「这次请求是谁发的、为了什么」，编排那一层看不见。
//
// ⚠️ 它的射程照写：**它只证得了「本源今天说的是实话」。**
// 一个刻意说谎的第三方源不会来写这条测试 —— 那一格由 `ClientUse` 的注释声明为射程。
func TestDeclaredClientUseMatchesBehaviour(t *testing.T) {
	days := []tickflow.TradingDay{20260904, 20260907, 20260908}
	ds := newDayServer(t)

	// 前提一：本源**声明**的是 ClientUseHTTP。
	// ⛔ 不立这一条，下面那个「走了几次」就不知道该拿去和什么对。
	base := newTestClient(t, ds, days)
	if got := base.Caps(icSym().ProductKey()).ClientUse; got != tickflow.ClientUseHTTP {
		t.Fatalf("本源声明的是 %v，而这条测试是按 ClientUseHTTP 写的 —— 前提变了", got)
	}

	rt := &countingRT{next: http.DefaultTransport}
	c := newTestClientWithHTTP(t, ds, days, &http.Client{Transport: rt})

	if _, err := c.Bars(context.Background(), icReq(20260904, 20260908)); err != nil {
		t.Fatalf("Bars：%v", err)
	}

	// 前提二：对端**确实收到过**请求 —— 否则下面那个相等在 0 == 0 上照样绿。
	if len(ds.paths) == 0 {
		t.Fatal("对端一次请求都没收到 —— 前提没成立，下面那条相等什么也不证明")
	}
	// 断言：**每一次请求都经过了注入的那个 client。**
	if rt.n != len(ds.paths) {
		t.Errorf("注入的 client 被用了 %d 次，而对端收到 %d 次请求\n"+
			"  ⇒ 本源声明了 ClientUseHTTP，而它有请求没走那个 client；\n"+
			"    编排那一层【查不出这件事】—— 所以它只能在这儿被查", rt.n, len(ds.paths))
	}
}
