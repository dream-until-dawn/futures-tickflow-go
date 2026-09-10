package shinnyref

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// slowServer 起一个**分片慢速**回 gzip 的服务端。
//
// ⚠️ 明文用**不可压缩**的随机字节，而这一条是被一次假读数逼出来的：
// 评审方第一版探针用重复串 ⇒ 压完只剩几百字节 ⇒ 分片循环几乎不睡 ⇒ **根本没超时**
// ⇒ 那次两跑都 SKIP，而
// 🔴 **一次 SKIP 长得和「这个问题不存在」一模一样。**
// ⇒ 所以「这个构造真的慢下来了吗」在这里是一条**断言**，不是一句注释。
func slowServer(t *testing.T, plainLen int) *httptest.Server {
	t.Helper()
	plain := make([]byte, plainLen)
	if _, err := rand.Read(plain); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(plain); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	body := buf.Bytes()
	// 前提自检：压完必须仍然很大，否则这个构造慢不下来。
	if len(body) < plainLen*9/10 {
		t.Fatalf("压完只有 %d B（明文 %d B）—— 这个构造慢不下来，读数作废",
			len(body), plainLen)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		fl, _ := w.(http.Flusher)
		for i := 0; i < len(body); i += 512 {
			end := i + 512
			if end > len(body) {
				end = len(body)
			}
			if _, err := w.Write(body[i:end]); err != nil {
				return
			}
			if fl != nil {
				fl.Flush()
			}
			time.Sleep(3 * time.Millisecond)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// readUntilErr 读到第一个错误为止，把它交回来。
func readUntilErr(rc io.ReadCloser) error {
	p := make([]byte, 4096)
	for i := 0; i < 1000000; i++ {
		if _, e := rc.Read(p); e != nil {
			return e
		}
	}
	return nil
}

// TestCloseBlamesStreamForTransportTimeout 是【第三条必改】那一格，
// 而它的方向**比上一条贵**：**该重取的被判成不必重取 ⇒ 一次静默的不重试**。
//
// ⛔ `context.DeadlineExceeded` **不只来自调用方的 ctx** —— `http.Client{Timeout}`
// 也给出它，而那是一个**传输层**超时，处置恰恰是【重取】。
//
// ⚠️ 而 Go 自己那句错误文本就拒绝区分这两者：
// `context deadline exceeded (Client.Timeout **or** context cancellation while reading body)`
// ⇒ 「再挑一个哨兵」这条路走不通。
//
// ⭐ 判别符是**问调用方那个 ctx 自己**（三端各喂一次，两人各量一遍，逐格一致）：
//
//	A `client.Timeout`   Is(DeadlineExceeded)=true · **ctx.Err()=nil**
//	B 调用方 cancel       Is(Canceled)=true         · ctx.Err()=context canceled
//	C 调用方 WithTimeout   Is(DeadlineExceeded)=true · ctx.Err()=deadline exceeded
//
// ⚠️ 而这一格**恰好打在这个包最可能发生的那种失败上**：真上游整包实测
// **10.59 MiB / 712 秒**，任何一个像样的 `Client.Timeout` 都会在它身上触发
// —— 而本仓的源本来就设过 30s 超时。**这不是一个理论上的输入。**
//
// ⛔ 我这一轮量它时先踩了一格：`httptest.Server.Client()` **每次返回同一个指针**，
// 我在 A 上设的 `Timeout` 跟着进了 B 和 C ⇒ B 印出 `Is(DeadlineExceeded)=true`
// （一个「取消」报成了「超时」）。⇒ **每格一个新 client**，并断言那个共用 client 的
// Timeout 是 0。—— 本仓那条：**为了量一个东西而改了共用的那个，它就不再是我要量的那个。**
func TestCloseBlamesStreamForTransportTimeout(t *testing.T) {
	srv := slowServer(t, 400000)
	// 前提自检：共用的那个 client 不许已经带着 Timeout。
	if srv.Client().Timeout != 0 {
		t.Fatalf("共用 client 的 Timeout 是 %v —— 读数会被它污染", srv.Client().Timeout)
	}
	c := &http.Client{Transport: srv.Client().Transport, Timeout: 60 * time.Millisecond}

	// ⚠️ ctx 用 Background —— 这一格的要点正是「**没有人**中止过」。
	rc, err := Fetch(context.Background(), c, srv.URL)
	if err != nil {
		t.Fatalf("Fetch 失败：%v", err)
	}
	readErr := readUntilErr(rc)
	if !errors.Is(readErr, context.DeadlineExceeded) {
		t.Fatalf("这个构造要的是 client.Timeout 打断，实得 %v —— 构造不成立", readErr)
	}
	cerr := rc.Close()
	if !errors.Is(cerr, errStreamBroken) {
		t.Fatalf("传输层超时，而 Close 报的是 %v —— 要 errStreamBroken（该重取）。\n"+
			"  ⇒ 判成「调用方自己中止」的话，这是一次【静默的不重试】，"+
			"而真上游 712 秒的整包会天天撞它。", cerr)
	}
	if errors.Is(cerr, errNotReadToEOF) {
		t.Fatalf("没有人中止，而 Close 说「调用方自己中止的」：%v", cerr)
	}
}

// TestCloseBlamesStreamWhenCancelComesAfterTheBreak 关的是评审方**单列为「他没量的」**那一格。
//
// ⛔ 若判据在 `Close` 里才问 `ctx.Err()`，那么「**失败之后、Close 之前**调用方才 cancel」
// 会被误判成「调用方中止」——方向与第三条必改同向（少重取一次）。
// ⇒ 所以判据问的是 **`Read` 第一次失败【那一刻】** 的 ctx 状态。
//
// ⛔ **而我第一版这条测试是空的**：我用「流中段就断」当输入，
// 那个错误是 `unexpected EOF`，**不是 ctx 哨兵** ⇒ 两种写法都落到兜底那一支
// ⇒ 突变「在 Close 里问」⇒ **0 红**。
// 🔴 **一条测试要分开两个实现，它的输入必须走到那两个实现【不同】的那条路上。**
// ⇒ 换成能分开的那个输入：**传输层超时在先（ctx.Err() 那一刻是 nil），调用方取消在后。**
func TestCloseBlamesStreamWhenCancelComesAfterTheBreak(t *testing.T) {
	srv := slowServer(t, 400000)
	if srv.Client().Timeout != 0 {
		t.Fatalf("共用 client 的 Timeout 是 %v —— 读数会被它污染", srv.Client().Timeout)
	}
	c := &http.Client{Transport: srv.Client().Transport, Timeout: 60 * time.Millisecond}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rc, err := Fetch(ctx, c, srv.URL)
	if err != nil {
		t.Fatalf("Fetch 失败：%v", err)
	}
	readErr := readUntilErr(rc)
	// 前提自检：这一格要的是**传输层**超时（ctx 那一刻还没 done）。
	if !errors.Is(readErr, context.DeadlineExceeded) {
		t.Fatalf("这个构造要的是 client.Timeout 打断，实得 %v", readErr)
	}
	if ctx.Err() != nil {
		t.Fatalf("失败那一刻调用方的 ctx 就已经 done 了（%v）—— 这个构造不成立", ctx.Err())
	}

	// ⇒ 失败【之后】调用方才取消
	cancel()
	if ctx.Err() == nil {
		t.Fatal("cancel 之后 ctx.Err() 仍是 nil —— 构造不成立")
	}
	if cerr := rc.Close(); !errors.Is(cerr, errStreamBroken) {
		t.Fatalf("传输层超时在先、取消在后，而 Close 报的是 %v —— 要 errStreamBroken。"+
			"  ⇒ 在 Close 里才问 ctx.Err() 的话，这一格会被误判成「调用方中止」"+
			"⇒ 一次静默的不重试", cerr)
	}
}
