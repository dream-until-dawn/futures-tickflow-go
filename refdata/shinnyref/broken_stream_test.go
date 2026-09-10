package shinnyref

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCloseReportsChecksumFailure 钉住两件事，而它们是同一个错误的两半：
//
//	① `sawEOF` **只认 `io.EOF`**，不认「任何非 nil 的错误」
//	② 一份**读到底而校验失败**的下载，报的是 `errStreamBroken`，不是 `errNotReadToEOF`
//
// —— 这一格的由来 ——
//
// 评审方 2026-09-10 换了个维度打（他铺的不是载荷档位，是**判据的松紧**），
// 发现把条件放松成 `err != nil` ⇒ **全仓 0 红，突变活着**。
// 于是他去造分开这两个判据的输入，造出来了：**CRC 坏掉的 gzip 流**。
//
// 我复现，并且多量到一格他没报的 —— **三种坏法都走同一条路**：
//
//	完整        最后一次 Read=(30, EOF)                    · zr.Close()=nil
//	CRC 翻一位   最后一次 Read=(30, gzip: invalid checksum)  · zr.Close()=**nil**
//	ISIZE 翻一位 最后一次 Read=(30, gzip: invalid checksum)  · zr.Close()=**nil**
//	砍掉末尾 8B  最后一次 Read=(30, unexpected EOF)          · zr.Close()=**nil**
//
// 🔴 ⇒ 那句假成因**不止在 CRC 那一格，截断那一格也是** ——
// 而它们的共同点是：**调用方读到底了，校验发生了并且失败了**，
// 而原来的留声说「你没读完、校验没发生」。
// ⇒ 本仓那条（我自己写的）在这里被打了一次：
// **会误报的留声，措辞里不要含成因 —— 读数错一格，成因错两格。**
func TestCloseReportsChecksumFailure(t *testing.T) {
	plain := strings.Repeat("Z", 30)
	good := gz(t, plain)

	// 前提自检：完整的那份必须一路干净 —— 否则下面的「坏了」可能只是「总是坏」。
	t.Run("前提_完整的一份", func(t *testing.T) {
		rc := fetchBody(t, good)
		n, err := io.Copy(io.Discard, rc)
		if err != nil || int(n) != len(plain) {
			t.Fatalf("完整的一份读不干净：n=%d err=%v", n, err)
		}
		if err := rc.Close(); err != nil {
			t.Fatalf("完整的一份 Close 却报了：%v", err)
		}
	})

	for _, c := range []struct {
		what   string
		break_ func([]byte) []byte
	}{
		// 尾 8 字节 = CRC32(4) ＋ ISIZE(4)
		{"CRC 翻一位", func(b []byte) []byte { b[len(b)-8] ^= 0x01; return b }},
		{"ISIZE 翻一位", func(b []byte) []byte { b[len(b)-4] ^= 0x01; return b }},
		{"砍掉末尾 8 字节", func(b []byte) []byte { return b[:len(b)-8] }},
	} {
		t.Run(c.what, func(t *testing.T) {
			rc := fetchBody(t, c.break_(append([]byte(nil), good...)))
			n, rerr := io.Copy(io.Discard, rc)
			// ⚠️ 这一步是承重的前提：**调用方确实读到底了**（拿到了全部明文），
			// 否则「说他没读完」就不算诬告。
			if int(n) != len(plain) {
				t.Fatalf("这个构造要的是「读到底才发现坏」，实得 %d 字节", n)
			}
			if rerr == nil {
				t.Fatalf("流坏了而 Read 没报错 —— 那么这一格什么也没在测")
			}

			cerr := rc.Close()
			if !errors.Is(cerr, errStreamBroken) {
				t.Fatalf("Close 报的是 %v，要 errStreamBroken —— "+
					"把「读到底而校验失败」和「调用方提前放手」合成一个哨兵，"+
					"读的人分不出【该重取】和【不必重取】", cerr)
			}
			if errors.Is(cerr, errNotReadToEOF) {
				t.Fatalf("Close 把一份【读到底】的下载报成了「没读到 EOF」：%v", cerr)
			}
			// ⛔ 而承重的是这一句：**报文里不许再出现那个假成因**。
			if strings.Contains(cerr.Error(), "校验【没有发生】") {
				t.Fatalf("报文里还写着「校验没有发生」，而它【发生了并且失败了】：%v", cerr)
			}
		})
	}
}

// fetchBody 起一个只回这一份 body 的 httptest，然后走真 Fetch。
func fetchBody(t *testing.T, body []byte) io.ReadCloser {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	rc, err := Fetch(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("Fetch 失败：%v", err)
	}
	return rc
}
