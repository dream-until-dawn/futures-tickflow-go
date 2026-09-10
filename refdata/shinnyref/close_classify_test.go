package shinnyref

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestCloseDoesNotBlameStreamForCallerCancel 是【必改】那一格：**谁造成的**。
//
// ⛔ 合并两个错误来源之后，`readErr` 的取值**不封闭** —— 它是 transport / OS / ctx
// 交回来的任何错误。于是**调用方自己 `cancel()`** 也落进了「这份流坏了 ⇒ 该重取」。
//
// 实测（分片慢速回，读 4096 字节后调用方自己取消）：
//
//	Read ⇒ **context canceled** · 而 `zr.Close()` 也是 **context canceled**
//	⇒ 判据要看合并之后的那一个，**不能只看 readErr**
//
// 🔴 而最难看的一格：**它与 `Close` 上方那段注释直接矛盾** ——
// 那段写着「提前放手的调用方（ctx 取消、回调喊停）会拿到一个 Close 错误，
// 而它说的是『这份下载没有被校验过』」。
// **同一个函数里代码与注释各说各的，而注释那一版是对的。**
// ⇒ 留声含成因的第四次：「这份流坏了」假 ·「不是调用方少读了」也假。
//
// ⚠️ 射程：`readErr` 不封闭 ⇒ **只能兜底不能枚举** ⇒ 默认仍落 errStreamBroken
// （保守方向：宁可多说一次「该重取」）；而 ctx 那两个是封闭且判得了的，单独摘出去。
func TestCloseDoesNotBlameStreamForCallerCancel(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(strings.Repeat("Z", 4<<20))); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	body := buf.Bytes()

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
			time.Sleep(time.Millisecond)
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	rc, err := Fetch(ctx, srv.Client(), srv.URL)
	if err != nil {
		cancel()
		t.Fatalf("Fetch 失败：%v", err)
	}
	p := make([]byte, 4096)
	if _, err := io.ReadFull(rc, p); err != nil {
		cancel()
		t.Fatalf("前 4096 字节都读不出来：%v —— 这个构造不是我以为的那个", err)
	}
	cancel()

	// 取消之后接着读，直到拿到那个错误（这正是调用方会做的事）。
	var readErr error
	for i := 0; i < 1000000; i++ {
		if _, e := rc.Read(p); e != nil {
			readErr = e
			break
		}
	}
	if !errors.Is(readErr, context.Canceled) {
		t.Fatalf("取消之后 Read 交回的不是 context.Canceled，而是 %v —— "+
			"这个构造不是我以为的那个", readErr)
	}

	cerr := rc.Close()
	if errors.Is(cerr, errStreamBroken) {
		t.Fatalf("调用方自己取消，而 Close 报「这份流坏了、该重取」：%v\n"+
			"  ⇒ 照这套分类走的调用方会对一份【用户主动取消】的下载报「上游数据损坏」。\n"+
			"  ⇒ 而本仓记过：一个长期误报的告警最终会关掉它自己。", cerr)
	}
	if !errors.Is(cerr, errNotReadToEOF) {
		t.Fatalf("调用方自己取消 ⇒ 要 errNotReadToEOF（没验过，但流没坏），实得 %v", cerr)
	}
	if strings.Contains(cerr.Error(), "这份流坏了") {
		t.Fatalf("报文说「这份流坏了」，而它没坏：%v", cerr)
	}
}

// TestCloseStillBlamesStreamForRealBreak 是上一条的**对照组**。
//
// ⚠️ 没有它，「把 ctx 摘出去」这个改动可能只是把**所有**东西都摘出去了。
func TestCloseStillBlamesStreamForRealBreak(t *testing.T) {
	full := gz(t, strings.Repeat("Z", 800))
	rc := fetchBody(t, full[:len(full)/2])
	io.Copy(io.Discard, rc)
	if cerr := rc.Close(); !errors.Is(cerr, errStreamBroken) {
		t.Fatalf("真截断而 Close 报的是 %v —— 要 errStreamBroken", cerr)
	}
}

// failCloser 的 Close 一定报错 —— 用来钉住「底层的关闭错误不许被静默丢掉」。
type failCloser struct {
	io.Reader
	err error
}

func (f failCloser) Close() error { return f.err }

// TestCloseKeepsUnderlyingCloseError —— `raw.Close()` 的错误**不许消失**。
//
// ⛔ 评审方 2026-09-10 报的建议格：突变「把最后那行 `return rerr` 换成 `return nil`」
// ⇒ **0 红** —— 它不是「一个被守着的行为被丢了」，是**一个从没被断言过的返回值**。
// 而在两个出错分支里它此前被**整个丢掉**。
// ⇒ 现在用 `errors.Join` 并上去：两个哨兵的 `errors.Is` 照旧成立，而 rerr 不再静默消失。
//
// 三格：干净地读完 · 提前放手 · 流坏了 —— **每一格都要能看见那个底层错误**。
func TestCloseKeepsUnderlyingCloseError(t *testing.T) {
	sentinel := errors.New("底层关不掉")
	good := gz(t, strings.Repeat("Z", 800))

	newBody := func(t *testing.T, payload []byte) *bodyReader {
		t.Helper()
		raw := failCloser{Reader: bytes.NewReader(payload), err: sentinel}
		zr, err := gzip.NewReader(raw)
		if err != nil {
			t.Fatal(err)
		}
		return &bodyReader{zr: zr, raw: raw}
	}

	t.Run("干净地读完", func(t *testing.T) {
		b := newBody(t, good)
		if _, err := io.Copy(io.Discard, b); err != nil {
			t.Fatal(err)
		}
		if err := b.Close(); !errors.Is(err, sentinel) {
			t.Fatalf("底层的关闭错误消失了：%v", err)
		}
	})
	t.Run("提前放手", func(t *testing.T) {
		b := newBody(t, good)
		if _, err := io.ReadFull(b, make([]byte, 4)); err != nil {
			t.Fatal(err)
		}
		err := b.Close()
		if !errors.Is(err, errNotReadToEOF) {
			t.Fatalf("要 errNotReadToEOF，实得 %v", err)
		}
		if !errors.Is(err, sentinel) {
			t.Fatalf("底层的关闭错误被那一支丢掉了：%v", err)
		}
	})
	t.Run("流坏了", func(t *testing.T) {
		b := newBody(t, good[:len(good)/2])
		io.Copy(io.Discard, b)
		err := b.Close()
		if !errors.Is(err, errStreamBroken) {
			t.Fatalf("要 errStreamBroken，实得 %v", err)
		}
		if !errors.Is(err, sentinel) {
			t.Fatalf("底层的关闭错误被那一支丢掉了：%v", err)
		}
	})
}
