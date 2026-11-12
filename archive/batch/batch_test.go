package batch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	syncatomic "sync/atomic"
	"testing"
	"time"

	"github.com/dream-until-dawn/futures-tickflow-go/refdata/evidence"
)

func TestLocalReceiptCannotRebindFrozenSHA(t *testing.T) {
	root := t.TempDir()
	original := filepath.Join(root, "original.body")
	raw := []byte("real-market-data")
	if e := os.WriteFile(original, raw, 0600); e != nil {
		t.Fatal(e)
	}
	job := Job{ID: "frozen", URL: "https://example.org/daily", Local: original, ExpectedSHA: digest(raw), RetrievedAt: time.Now().UTC(), Version: "decoder-revision-1"}
	destination := filepath.Join(root, "archive")
	accept := func(Job, []byte, evidence.Ref) error { return nil }
	if result, e := Run(context.Background(), destination, []Job{job}, nil, 1024, accept); e != nil || result[0].Receipt.Evidence.Version != job.Version {
		t.Fatalf("产物版本未完整保留: %+v %v", result, e)
	}
	data, e := os.ReadFile(filepath.Join(destination, "frozen.json"))
	if e != nil {
		t.Fatal(e)
	}
	var r Receipt
	if e = json.Unmarshal(data, &r); e != nil {
		t.Fatal(e)
	}
	changed := []byte("another-parseable-document")
	r.Evidence.SHA256 = digest(changed)
	r.Bytes = int64(len(changed))
	if e = os.WriteFile(filepath.Join(destination, "frozen.body"), changed, 0600); e != nil {
		t.Fatal(e)
	}
	if e = atomic(filepath.Join(destination, "frozen.json"), r); e != nil {
		t.Fatal(e)
	}
	if _, e = Run(context.Background(), destination, []Job{job}, nil, 1024, accept); e == nil {
		t.Fatal("原件与收据一起改写绕过冻结SHA")
	}
}

func limits() Limits {
	return Limits{Requests: 2, Bytes: 1024, Interval: time.Millisecond, Timeout: time.Second}
}
func validator(_ Job, raw []byte, _ evidence.Ref) error {
	if string(raw) != "real-market-data" {
		return fmt.Errorf("挑战页或无业务内容")
	}
	return nil
}
func TestBatchBudgetResumeAndTamper(t *testing.T) {
	var requests syncatomic.Int32 // HTTP响应不为测试与服务端协程的共享变量提供Go内存同步。
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); fmt.Fprint(w, "real-market-data") }))
	defer server.Close()
	d, e := NewDownloader(server.Client(), limits())
	if e != nil {
		t.Fatal(e)
	}
	job := Job{ID: "official-day", URL: server.URL, Format: "test", Day: 20261104}
	root := t.TempDir()
	result, e := Run(context.Background(), root, []Job{job}, d, 1024, validator)
	if e != nil || result[0].Receipt.State != "received" || requests.Load() != 1 {
		t.Fatalf("%+v %v %d", result, e, requests.Load())
	}
	result, e = Run(context.Background(), root, []Job{job}, d, 1024, validator)
	if e != nil || !result[0].Resumed || requests.Load() != 1 {
		t.Fatalf("恢复错误 %v", e)
	}
	if e = os.WriteFile(filepath.Join(root, "official-day.body"), []byte("changed"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = Run(context.Background(), root, []Job{job}, d, 1024, validator); e == nil {
		t.Fatal("篡改恢复通过")
	}
	for i := 0; i < 2; i++ {
		_, e = d.Fetch(context.Background(), server.URL)
		if (i == 0 && e != nil) || (i == 1 && e == nil) {
			t.Fatalf("请求预算 %d %v", i, e)
		}
	}
	n, b := d.Usage()
	if n != 2 || b != 32 {
		t.Fatalf("实际请求/字节 %d %d", n, b)
	}
}
func TestBatchFailuresDoNotRetryOrMasqueradeAsSuccess(t *testing.T) {
	var requests syncatomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); fmt.Fprint(w, "<html>challenge</html>") }))
	defer server.Close()
	d, _ := NewDownloader(server.Client(), limits())
	root := t.TempDir()
	job := Job{ID: "challenge", URL: server.URL}
	for i := 0; i < 2; i++ {
		r, e := Run(context.Background(), root, []Job{job}, d, 1024, validator)
		if e != nil || r[0].Receipt.State != "failed" {
			t.Fatalf("%+v %v", r, e)
		}
	}
	if requests.Load() != 1 {
		t.Fatal("失败重复请求")
	}
	job.ID = "blocked"
	job.Blocked = "已知证书问题，禁止自动重试"
	r, e := Run(context.Background(), root, []Job{job}, d, 1024, validator)
	if e != nil || r[0].Receipt.State != "blocked" || requests.Load() != 1 {
		t.Fatal("阻塞源被请求")
	}
	job.ID = "../escape"
	if _, e = Run(context.Background(), root, []Job{job}, d, 1024, validator); e == nil {
		t.Fatal("目录穿越通过")
	}
	if e = os.WriteFile(filepath.Join(root, "writer.lock"), []byte("unknown earlier run"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = Run(context.Background(), root, nil, d, 1024, validator); e == nil {
		t.Fatal("未知启动重试")
	}
}
func TestDownloaderByteTimeoutAndRedirect(t *testing.T) {
	for _, mode := range []string{"bytes", "timeout", "redirect", "status"} {
		t.Run(mode, func(t *testing.T) {
			var calls syncatomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				switch mode {
				case "timeout":
					<-r.Context().Done()
				case "redirect":
					http.Redirect(w, r, "/target", http.StatusFound)
				case "status":
					w.WriteHeader(http.StatusServiceUnavailable)
				default:
					fmt.Fprint(w, "0123456789")
				}
			}))
			defer s.Close()
			l := limits()
			if mode == "bytes" {
				l.Bytes = 3
			}
			if mode == "timeout" {
				l.Timeout = 30 * time.Millisecond
			}
			d, e := NewDownloader(s.Client(), l)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = d.Fetch(context.Background(), s.URL); e == nil {
				t.Fatal("失败被吞")
			}
			n, b := d.Usage()
			if n != 1 || calls.Load() != 1 {
				t.Fatal("发生隐式重试/重定向")
			}
			if mode == "bytes" && b != 4 {
				t.Fatal("溢出探针字节未记账")
			}
		})
	}
}
