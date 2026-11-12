package batch

import (
	"context"
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

func TestLocalOverflowPrefixResumesWithoutReadingOriginalAgain(t *testing.T) {
	root := t.TempDir()
	original := filepath.Join(root, "original.txt")
	raw := []byte("0123456789")
	if err := os.WriteFile(original, raw, 0600); err != nil {
		t.Fatal(err)
	}
	job := Job{ID: "local-overflow", URL: "https://example.org/daily", Local: original, ExpectedSHA: digest(raw), RetrievedAt: time.Now().UTC()}
	destination := filepath.Join(root, "archive")
	validate := func(Job, []byte, evidence.Ref) error { t.Fatal("不完整原件不应进入验证"); return nil }
	for round := 0; round < 2; round++ {
		if round == 1 {
			if err := os.Remove(original); err != nil {
				t.Fatal(err)
			}
		}
		result, err := Run(context.Background(), destination, []Job{job}, nil, 4, validate)
		if err != nil || result[0].Receipt.State != "failed" || result[0].Receipt.BodyState != "partial" || result[0].Receipt.Bytes != 5 || (round == 1 && !result[0].Resumed) {
			t.Fatalf("失败前缀恢复: %+v %v", result, err)
		}
	}
}

func TestFailureEvidencePreserves412PartialAndNoResponse(t *testing.T) {
	for _, mode := range []string{"412", "truncated", "byte_limit", "network"} {
		t.Run(mode, func(t *testing.T) {
			var calls syncatomic.Int32 // 处理器与测试线程独立运行，保留精确计数且避免race。
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				switch mode {
				case "412":
					w.Header().Set("Content-Type", "text/html")
					w.WriteHeader(412)
					fmt.Fprint(w, "blocked response body")
				case "truncated":
					w.Header().Set("Content-Length", "100")
					fmt.Fprint(w, "short")
				default:
					fmt.Fprint(w, "too-large-body")
				}
			}))
			defer server.Close()
			if mode == "network" {
				server.Close()
			}
			l := limits()
			if mode == "byte_limit" {
				l.Bytes = 4
			}
			down, e := NewDownloader(server.Client(), l)
			if e != nil {
				t.Fatal(e)
			}
			root := t.TempDir()
			job := Job{ID: "failed-original", URL: server.URL}
			validations := 0
			validate := func(Job, []byte, evidence.Ref) error { validations++; return nil }
			for round := 0; round < 2; round++ {
				r, e := Run(context.Background(), root, []Job{job}, down, 1024, validate)
				if e != nil || r[0].Receipt.State != "failed" {
					t.Fatalf("%+v %v", r, e)
				}
				receipt := r[0].Receipt
				if receipt.HTTP == nil || !receipt.HTTP.Attempted {
					t.Fatal("缺实际尝试身份")
				}
				if mode == "network" {
					if receipt.BodyState != "none" || receipt.HTTP.ResponseReceived || receipt.Bytes != 0 {
						t.Fatalf("网络无响应被伪造正文: %+v", receipt)
					}
					if _, e = os.Stat(filepath.Join(root, "failed-original.body")); !os.IsNotExist(e) {
						t.Fatal("无响应竟有正文")
					}
					continue
				}
				raw, e := os.ReadFile(filepath.Join(root, "failed-original.body"))
				if e != nil {
					t.Fatal(e)
				}
				if e = receipt.Evidence.Verify(raw); e != nil {
					t.Fatal(e)
				}
				if receipt.HTTP.FinalURL != server.URL || !receipt.HTTP.ResponseReceived {
					t.Fatal("HTTP身份缺失")
				}
				switch mode {
				case "412":
					if receipt.HTTP.Status != 412 || !receipt.HTTP.Complete || receipt.BodyState != "complete" || string(raw) != "blocked response body" {
						t.Fatalf("412证据不符 %+v", receipt)
					}
				case "truncated":
					if receipt.HTTP.Complete || receipt.BodyState != "partial" || string(raw) != "short" {
						t.Fatal("截断被当完整")
					}
				case "byte_limit":
					if receipt.HTTP.Complete || receipt.BodyState != "partial" || len(raw) != 5 {
						t.Fatal("溢出前缀未保留或当完整")
					}
				}
			}
			n, _ := down.Usage()
			if n != 1 || validations != 0 || (mode != "network" && calls.Load() != 1) {
				t.Fatalf("失败重复请求/误验证: %d %d %d", n, validations, calls.Load())
			}
			if mode != "network" {
				if e = os.WriteFile(filepath.Join(root, "failed-original.body"), []byte("mutated failure evidence"), 0600); e != nil {
					t.Fatal(e)
				}
				if _, e = Run(context.Background(), root, []Job{job}, down, 1024, validate); e == nil {
					t.Fatal("失败取证恢复忽略篡改")
				}
				n, _ = down.Usage()
				if n != 1 {
					t.Fatal("篡改触发重取")
				}
			}
		})
	}
}
