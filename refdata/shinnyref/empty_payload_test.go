package shinnyref

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestCloseAcceptsFullyReadEmptyPayload 钉的是 `bodyReader` 那条「读到 EOF 了没有」
// 判据的**空载荷边界**。
//
// ⛔ 它挡的是这一族改动：**把 `sawEOF` 的条件收窄成「这次读到了数据」**，
// 例如 `if n > 0 && err == io.EOF`。
// 🔴 而它挡得住的原因只有一条：**明文为空时，那一次 EOF 上没有数据。**
//
// —— 这一格的由来（两个人各错一次，形状相同）——
//
// 我先量了 28 组（明文 31 / 350 / 1000 / 4096 × 缓冲 6 档 ＋ 逐字节喂），
// **每一组的读序列末尾都是 `(n>0, EOF)`** ⇒ 我据此写下
// 「`n > 0 &&` 与 `err ==` 在这条路上**等价**」。评审方收下了它，还写成「不可判别」。
//
// ⛔ **两句都是假的。** 他随后造了第 29 格 —— **明文长度 0**，我复现：
//
//	明文 0 · 缓冲 1/7/512 · 逐字节喂开与关 ⇒ **六格的读序列都是 `(0, EOF)`**
//	明文 1 起 ⇒ 永远 `(n>0, EOF)`
//	⇒ **边界恰好在 0 与 1 之间**
//
// 🔴 而两个人错的形状是同一个，而且是我自己上一颗刚写下的那条：
// **一句「等价／不可能」是全称断言，而我量到的是一个入口** —— 这次那个入口是【非空明文】。
// ⚠️ 更实的一句：**我铺档位时只往【大】的方向铺**（31 → 4096），
// 而缺的那一格在【小】的那一头。⇒ **铺档位要两个方向都铺，
// 而「更大」是默认想到的那个方向。**
// 📎 且这一格坐实了本仓那条：**判据相同 ⇒ 跨人复核失效** ——
// 他第一次是照着我的输入集判的，换了维度才看见。
func TestCloseAcceptsFullyReadEmptyPayload(t *testing.T) {
	// 明文 0 字节的**合法** gzip 流（gz 在 fetch_test.go，同包）。
	body := gz(t, "")
	if len(body) == 0 {
		t.Fatal("前提不成立：空明文压出来的 gzip 流不该是 0 字节（它有头有尾）")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		w.Write(body)
	}))
	defer srv.Close()

	rc, err := Fetch(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("一个合法的（空的）gzip 响应被 Fetch 拒了：%v", err)
	}
	n, err := io.Copy(io.Discard, rc)
	if err != nil {
		t.Fatalf("读一个空载荷时出错：%v", err)
	}
	if n != 0 {
		t.Fatalf("空明文却读出 %d 字节 —— 这个构造不是我以为的那个", n)
	}
	// ⇒ 这里已经读到 EOF 了。判据说「读到 EOF ⇒ Close 不报」。
	if err := rc.Close(); err != nil {
		t.Fatalf("把一个空载荷【读到底】之后 Close 却报了：%v\n"+
			"  ⇒ 判据把「读到 EOF」错当成了「读到过数据」。\n"+
			"  ⇒ 而它的代价是：一个合法的上游响应会被诬告成"+
			"「你没读完，这份数据没被校验过」。", err)
	}
}
