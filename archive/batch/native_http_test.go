package batch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	syncatomic "sync/atomic"
	"testing"
	"time"

	"github.com/dream-until-dawn/futures-tickflow-go/refdata/evidence"
)

const nativeTestURL = "http://www.dce.com.cn/dce/channel/list/180.html"

func nativeLimits() Limits {
	return Limits{Requests: 2, Bytes: 4 << 20, Interval: time.Second, Timeout: 3 * time.Second}
}

// 合成协会文本仅检验策略，不冒充取得了真实协会或交易所原件。
func testNativeGrant() NativeHTTPGrant {
	body := []byte(`<a href="` + nativeTestURL + `">公开业务参数</a>`)
	return NativeHTTPGrant{URL: nativeTestURL, Method: "GET", LinkedURL: nativeTestURL, Source: evidence.Ref{ID: "synthetic-source", URL: "https://www.cfachina.org/servicesupport/sspz/", SHA256: digest(body), RetrievedAt: time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)}, SourceBody: body}
}

type nativeRoundTrip func(*http.Request) (*http.Response, error)

func (f nativeRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func nativeFixture(t *testing.T, status int, body string) (*Downloader, *int) {
	t.Helper()
	d, err := NewNativeHTTPDownloader(nativeLimits(), []NativeHTTPGrant{testNativeGrant()})
	if err != nil {
		t.Fatal(err)
	}
	calls := new(int)
	d.client.Transport = nativeRoundTrip(func(r *http.Request) (*http.Response, error) {
		*calls++
		if r.Method != "GET" || r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" || r.URL.User != nil {
			t.Fatal("公开请求携密/方法错误")
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r, ContentLength: int64(len(body))}, nil
	})
	return d, calls
}

func TestNativeHTTPDefaultRejectsBeforeNetwork(t *testing.T) {
	d, _ := NewDownloader(&http.Client{Transport: nativeRoundTrip(func(*http.Request) (*http.Response, error) { t.Fatal("默认远程HTTP竟联网"); return nil, nil })}, limits())
	r, err := d.FetchDetailed(context.Background(), nativeTestURL)
	n, b := d.Usage()
	if err == nil || r.Failure != "invalid_url" || r.Attempted || n != 0 || b != 0 {
		t.Fatalf("默认门禁: %+v %v", r, err)
	}
}

func TestNativeHTTPGrantAndBoundaryRejections(t *testing.T) {
	valid := testNativeGrant()
	for name, mutate := range map[string]func(*NativeHTTPGrant){
		"POST":           func(g *NativeHTTPGrant) { g.Method = "POST" },
		"empty-method":   func(g *NativeHTTPGrant) { g.Method = "" },
		"missing-source": func(g *NativeHTTPGrant) { g.Source = evidence.Ref{} },
		"changed-source": func(g *NativeHTTPGrant) { g.SourceBody = []byte("changed") },
		"foreign-source": func(g *NativeHTTPGrant) { g.Source.URL = "https://example.org/" },
		"no-href":        func(g *NativeHTTPGrant) { g.SourceBody = []byte(nativeTestURL); g.Source.SHA256 = digest(g.SourceBody) },
		"unlisted-page": func(g *NativeHTTPGrant) {
			g.URL = "http://www.dce.com.cn/dce/channel/list/181.html"
			g.LinkedURL = g.URL
		},
		"credentials":     func(g *NativeHTTPGrant) { g.URL = "http://user:secret@www.dce.com.cn/" },
		"foreign-host":    func(g *NativeHTTPGrant) { g.URL = "http://www.dce.cn/" },
		"port":            func(g *NativeHTTPGrant) { g.URL = "http://www.dce.com.cn:80/" },
		"fragment":        func(g *NativeHTTPGrant) { g.URL = nativeTestURL + "#x" },
		"query":           func(g *NativeHTTPGrant) { g.URL = nativeTestURL + "?token=secret" },
		"empty-query":     func(g *NativeHTTPGrant) { g.URL = nativeTestURL + "?" },
		"path":            func(g *NativeHTTPGrant) { g.URL = "http://www.dce.com.cn/admin" },
		"cross-host-path": func(g *NativeHTTPGrant) { g.URL = "http://www.gfex.com.cn/dce/channel/list/180.html" },
		"encoded-path":    func(g *NativeHTTPGrant) { g.URL = "http://www.dce.com.cn/dce/channel/list/%31%38%30.html" },
		"traversal":       func(g *NativeHTTPGrant) { g.URL = "http://www.dce.com.cn/dce/../admin" },
	} {
		t.Run(name, func(t *testing.T) {
			g := valid
			mutate(&g)
			if _, err := NewNativeHTTPDownloader(nativeLimits(), []NativeHTTPGrant{g}); err == nil {
				t.Fatal("无效授权竟通过")
			}
		})
	}
	for _, grants := range [][]NativeHTTPGrant{nil, {valid, valid}} {
		if _, err := NewNativeHTTPDownloader(nativeLimits(), grants); err == nil {
			t.Fatal("空/重复清单竟通过")
		}
	}
	for _, mutate := range []func(*Limits){func(l *Limits) { l.Requests = 13 }, func(l *Limits) { l.Bytes = 16<<20 + 1 }, func(l *Limits) { l.Interval = time.Millisecond }, func(l *Limits) { l.Timeout = 16 * time.Second }, func(l *Limits) { l.Requests = 0 }} {
		l := nativeLimits()
		mutate(&l)
		if _, err := NewNativeHTTPDownloader(l, []NativeHTTPGrant{valid}); err == nil {
			t.Fatal("越界预算竟通过")
		}
	}
	g := valid
	g.URL = "http://www.dce.com.cn/"
	if _, err := NewNativeHTTPDownloader(nativeLimits(), []NativeHTTPGrant{g}); err != nil {
		t.Fatal("有明示业务链接的同站首页应通过", err)
	}
	g = valid
	g.SourceBody = []byte(`$('#qh-jycs').attr("href", "` + nativeTestURL + `")`)
	g.Source.SHA256 = digest(g.SourceBody)
	if _, err := NewNativeHTTPDownloader(nativeLimits(), []NativeHTTPGrant{g}); err != nil {
		t.Fatal("协会常量href应通过", err)
	}
}

func TestNativeHTTPExactListAndPlaintextLabels(t *testing.T) {
	d, calls := nativeFixture(t, 200, "business document")
	for _, address := range []string{"http://www.dce.com.cn/", "https://www.dce.com.cn/", "http://localhost:1234/"} {
		r, err := d.FetchDetailed(context.Background(), address)
		if err == nil || r.Attempted || *calls != 0 {
			t.Fatalf("逐URL清单失效: %s %+v %v", address, r, err)
		}
	}
	r, err := d.FetchDetailed(context.Background(), nativeTestURL)
	if err != nil || !r.Complete || r.Transport != "native-http" || !r.Unauthenticated || r.SourceSHA != testNativeGrant().Source.SHA256 || r.SourceURL != testNativeGrant().Source.URL || *calls != 1 {
		t.Fatalf("明文标注: %+v %v", r, err)
	}
}

func TestNativeHTTPReceiptCannotWashTransportOrSource(t *testing.T) {
	for _, mode := range []string{"transport", "authenticated", "source", "final-url", "no-downloader", "default-downloader"} {
		t.Run(mode, func(t *testing.T) {
			d, calls := nativeFixture(t, 200, "business document")
			root := t.TempDir()
			jobs := []Job{{ID: "native", URL: nativeTestURL}}
			accept := func(Job, []byte, evidence.Ref) error { return nil }
			r, err := Run(context.Background(), root, jobs, d, 2<<20, accept)
			if err != nil || r[0].Receipt.State != "received" {
				t.Fatalf("正向落盘: %+v %v", r, err)
			}
			if r, err = Run(context.Background(), root, jobs, d, 2<<20, accept); err != nil || !r[0].Resumed || *calls != 1 {
				t.Fatal("正向恢复失败", err)
			}
			p := filepath.Join(root, "native.json")
			raw, _ := os.ReadFile(p)
			var receipt Receipt
			if err = json.Unmarshal(raw, &receipt); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "transport":
				receipt.HTTP.Transport = "https"
			case "authenticated":
				receipt.HTTP.Unauthenticated = false
			case "source":
				receipt.HTTP.SourceSHA = strings.Repeat("0", 64)
			case "final-url":
				receipt.HTTP.FinalURL = "https://www.dce.com.cn/"
			case "no-downloader":
				d = nil
			case "default-downloader":
				d, _ = NewDownloader(http.DefaultClient, limits())
			}
			if err = atomic(p, receipt); err != nil {
				t.Fatal(err)
			}
			if _, err = Run(context.Background(), root, jobs, d, 2<<20, accept); err == nil || !strings.Contains(err.Error(), "native-http") {
				t.Fatalf("明文/出处洗白未命中目标: %v", err)
			}
			if *calls != 1 {
				t.Fatal("坏收据引发重试")
			}
		})
	}
}

func TestNativeHTTPBoundedFailuresAndCancellation(t *testing.T) {
	for _, mode := range []string{"redirect", "non200", "oversize", "network", "timeout", "budget"} {
		t.Run(mode, func(t *testing.T) {
			d, calls := nativeFixture(t, 200, "doc")
			want := ""
			switch mode {
			case "redirect":
				d.client.Transport = nativeRoundTrip(func(r *http.Request) (*http.Response, error) {
					*calls++
					h := make(http.Header)
					h.Set("Location", "https://example.org/")
					return &http.Response{StatusCode: 302, Header: h, Body: io.NopCloser(strings.NewReader("redirect body")), Request: r}, nil
				})
				want = "http_status"
			case "non200":
				d, calls = nativeFixture(t, 412, "challenge body")
				want = "http_status"
			case "oversize":
				d, calls = nativeFixture(t, 200, strings.Repeat("x", (2<<20)+100))
				want = "byte_limit"
			case "network":
				d.client.Transport = nativeRoundTrip(func(*http.Request) (*http.Response, error) {
					*calls++
					return nil, fmt.Errorf("synthetic connection failed")
				})
				want = "network"
			case "timeout":
				d.limits.Timeout = 10 * time.Millisecond
				d.client.Transport = nativeRoundTrip(func(r *http.Request) (*http.Response, error) {
					*calls++
					<-r.Context().Done()
					return nil, r.Context().Err()
				})
				want = "network"
			case "budget":
				d.requests = d.limits.Requests
				want = "budget"
			}
			r, err := d.FetchDetailed(context.Background(), nativeTestURL)
			if err == nil || r.Failure != want {
				t.Fatalf("失败分类: %+v %v", r, err)
			}
			if mode == "budget" {
				if *calls != 0 || r.Attempted {
					t.Fatal("预算耗尽竟请求")
				}
			} else if *calls != 1 {
				t.Fatal("失败重试或跟随跳转")
			}
			if mode == "oversize" && len(r.Body) != (2<<20)+1 {
				t.Fatal("每件上限未按探针字节保留")
			}
			if (mode == "non200" || mode == "redirect") && (!r.Complete || len(r.Body) == 0) {
				t.Fatal("完整失败正文丢失")
			}
		})
	}
	d, calls := nativeFixture(t, 200, "doc")
	d.last = time.Now()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, err := d.FetchDetailed(ctx, nativeTestURL)
	if err == nil || r.Attempted || r.Failure != "cancelled_before_request" || *calls != 0 {
		t.Fatal("间隔取消仍联网", r, err)
	}
}

func TestNativeHTTPNoCookiesAndNoRedirectWithRealTransport(t *testing.T) {
	// 请求走本机HTTP代理形态，保持原目标URL；这是合成服务器，不是现场资料。
	var calls syncatomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
			t.Error("公开请求携密")
		}
		w.Header().Set("Location", "http://www.dce.com.cn/")
		w.Header().Set("Set-Cookie", "secret=do-not-reuse")
		w.WriteHeader(302)
		fmt.Fprint(w, "stop here")
	}))
	defer server.Close()
	d, _ := NewNativeHTTPDownloader(nativeLimits(), []NativeHTTPGrant{testNativeGrant()})
	transport := d.client.Transport.(*http.Transport)
	if !transport.DisableKeepAlives || d.client.Jar != nil || transport.TLSClientConfig != nil {
		t.Fatal("公开client继承状态或TLS被改")
	}
	proxyRequest, _ := http.NewRequest("GET", server.URL, nil)
	transport.Proxy = func(*http.Request) (*url.URL, error) { return proxyRequest.URL, nil }
	r, err := d.FetchDetailed(context.Background(), nativeTestURL)
	if err == nil || r.Status != 302 || calls.Load() != 1 || string(r.Body) != "stop here" {
		t.Fatalf("真实Transport跳转: %+v %v", r, err)
	}
}

func TestNativeHTTPOldJobIdentityUnchanged(t *testing.T) {
	raw, err := json.Marshal(Job{ID: "frozen", URL: "https://example.org/daily", Format: "csv", Day: 20261113})
	want := `{"id":"frozen","url":"https://example.org/daily","format":"csv","day":20261113,"retrieved_at":"0001-01-01T00:00:00Z"}`
	if err != nil || string(raw) != want {
		t.Fatalf("旧Job字段/顺序变化导致旧SHA失效: %s %v", raw, err)
	}
}
