package batch

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	syncatomic "sync/atomic"
	"testing"
	"time"
)

// 持续有字节到达也不能重置整次请求预算；只有socket空闲超时会漏掉这种滴流。
func TestDownloaderStreamingBodyDeadline(t *testing.T) {
	for _, mode := range []string{"complete-control", "drip"} {
		t.Run(mode, func(t *testing.T) {
			var calls, chunks syncatomic.Int32
			budget := 2 * time.Second
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				fmt.Fprint(w, "start-")
				w.(http.Flusher).Flush()
				if mode == "complete-control" {
					fmt.Fprint(w, "end")
					return
				}
				ticker := time.NewTicker(20 * time.Millisecond)
				defer ticker.Stop()
				// 即便破坏两层超时守卫，本地服务也会结束，故意破坏测试不会无限挂住。
				finish := time.NewTimer(4 * budget)
				defer finish.Stop()
				for {
					select {
					case <-r.Context().Done():
						return
					case <-finish.C:
						fmt.Fprint(w, "end")
						return
					case <-ticker.C:
						if _, err := fmt.Fprint(w, "."); err != nil {
							return
						}
						w.(http.Flusher).Flush()
						chunks.Add(1)
					}
				}
			}))
			defer server.Close()
			l := limits()
			l.Timeout = budget
			l.Bytes = 1 << 20
			d, err := NewDownloader(server.Client(), l)
			if err != nil {
				t.Fatal(err)
			}
			r, err := d.FetchDetailed(context.Background(), server.URL)
			if mode == "complete-control" {
				if err != nil || !r.Complete || string(r.Body) != "start-end" {
					t.Fatalf("complete control %+v %v", r, err)
				}
			} else {
				if err == nil || !errors.Is(err, context.DeadlineExceeded) || r.Complete || !r.ResponseReceived || r.Failure != "read_error" || !strings.HasPrefix(string(r.Body), "start-") {
					t.Fatalf("want bounded partial read, got complete=%t failure=%s err=%v body=%q", r.Complete, r.Failure, err, r.Body)
				}
				if chunks.Load() < 2 {
					t.Fatal("未证明持续滴流，不能把无响应超时算成此例通过")
				}
			}
			n, b := d.Usage()
			if n != 1 || calls.Load() != 1 || b != int64(len(r.Body)) {
				t.Fatalf("重试或部分字节漏记: requests=%d calls=%d bytes=%d actual=%d", n, calls.Load(), b, len(r.Body))
			}
		})
	}
}
