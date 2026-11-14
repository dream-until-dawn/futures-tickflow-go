package batch

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	syncatomic "sync/atomic"
	"testing"
	"time"

	"github.com/dream-until-dawn/futures-tickflow-go/refdata/evidence"
)

func gfexEnglishGrant(t *testing.T) GFEXEnglishHomeGrant {
	t.Helper()
	f, err := os.Open("testdata/gfex-english-launch-report.html.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	z, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	raw, err := io.ReadAll(io.LimitReader(z, (2<<20)+1))
	// 与生产常量分开钉住真实夹具，不能改生产常量时顺带放松测试期望。
	if err != nil || len(raw) != 22243 || digest(raw) != "5ec71426dfec7fa421e1a7ff131a5269eff5317b0ddfd198f33e2b47f6c95d6c" {
		t.Fatalf("冻结报道原件不符: bytes=%d err=%v", len(raw), err)
	}
	return GFEXEnglishHomeGrant{
		URL: "http://www.gfex.com.cn/en/index.shtml", Method: "GET", SourceBody: raw,
		Source: evidence.Ref{ID: "gfex-en-launch-report", URL: "https://www.cs.com.cn/zzqh2020/202601/t20260113_6532950.html",
			SHA256: "5ec71426dfec7fa421e1a7ff131a5269eff5317b0ddfd198f33e2b47f6c95d6c", RetrievedAt: time.Date(2026, 11, 14, 10, 55, 26, 358024400, time.UTC)},
	}
}

func gfexEnglishLimits() Limits {
	return Limits{Requests: 1, Bytes: 2 << 20, Timeout: 3 * time.Second, Interval: time.Second}
}

// 返回真实构造策略配合合成响应；不得发交易所或新闻站请求。
func gfexEnglishFixture(t *testing.T, status int, body string) (*Downloader, *int) {
	t.Helper()
	d, err := NewGFEXEnglishHomeDownloader(gfexEnglishLimits(), gfexEnglishGrant(t))
	if err != nil {
		t.Fatal(err)
	}
	calls := new(int)
	d.client.Transport = nativeRoundTrip(func(r *http.Request) (*http.Response, error) {
		*calls++
		if r.URL.String() != "http://www.gfex.com.cn/en/index.shtml" || r.Method != "GET" || r.Body != nil || r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("Proxy-Authorization") != "" {
			t.Fatal("精确公开GET被改变或携带账户信息")
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	return d, calls
}

func TestGFEXEnglishGrantRejectsChangedEvidenceAndScope(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(*GFEXEnglishHomeGrant)
		want   string
	}{
		"POST":                   {func(g *GFEXEnglishHomeGrant) { g.Method = "POST" }, "精确GET"},
		"empty-method":           {func(g *GFEXEnglishHomeGrant) { g.Method = "" }, "精确GET"},
		"foreign-report":         {func(g *GFEXEnglishHomeGrant) { g.Source.URL = "https://example.org/" }, "报道身份"},
		"same-host-other-report": {func(g *GFEXEnglishHomeGrant) { g.Source.URL += "?x=1" }, "报道身份"},
		"changed-sha":            {func(g *GFEXEnglishHomeGrant) { g.Source.SHA256 = strings.Repeat("0", 64) }, "报道身份"},
		"body-only":              {func(g *GFEXEnglishHomeGrant) { g.SourceBody = append(g.SourceBody, 'X') }, "原件字节或Ref"},
		"body-and-sha": {func(g *GFEXEnglishHomeGrant) {
			g.SourceBody = append(g.SourceBody, 'X')
			g.Source.SHA256 = digest(g.SourceBody)
		}, "报道身份"},
		"missing-literal-rehashed": {func(g *GFEXEnglishHomeGrant) {
			g.SourceBody = []byte("http://www.gfex.com.cn/other")
			g.Source.SHA256 = digest(g.SourceBody)
		}, "报道身份"},
		"missing-body":     {func(g *GFEXEnglishHomeGrant) { g.SourceBody = nil }, "原件字节或Ref"},
		"missing-time":     {func(g *GFEXEnglishHomeGrant) { g.Source.RetrievedAt = time.Time{} }, "原件字节或Ref"},
		"missing-id":       {func(g *GFEXEnglishHomeGrant) { g.Source.ID = "" }, "原件字节或Ref"},
		"oversized-source": {func(g *GFEXEnglishHomeGrant) { g.SourceBody = make([]byte, (2<<20)+1) }, "原件字节或Ref"},
	} {
		t.Run(name, func(t *testing.T) {
			g := gfexEnglishGrant(t)
			tc.mutate(&g)
			if _, err := NewGFEXEnglishHomeDownloader(gfexEnglishLimits(), g); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("应因%s拒绝，got %v", tc.want, err)
			}
		})
	}
	for _, address := range []string{"http://www.gfex.com.cn/", "http://www.gfex.com.cn/en/WebsiteMap.shtml", "https://www.gfex.com.cn/en/index.shtml", "http://www.dce.com.cn/en/index.shtml", "http://user:secret@www.gfex.com.cn/en/index.shtml", "http://www.gfex.com.cn:80/en/index.shtml", "http://www.gfex.com.cn/en/index.shtml?", "http://www.gfex.com.cn/en/index.shtml#", "http://www.gfex.com.cn/en/%69ndex.shtml", "http://www.gfex.com.cn/en/../en/index.shtml"} {
		t.Run(address, func(t *testing.T) {
			g := gfexEnglishGrant(t)
			g.URL = address
			if _, err := NewGFEXEnglishHomeDownloader(gfexEnglishLimits(), g); err == nil || !strings.Contains(err.Error(), "精确GET") {
				t.Fatalf("URL边界未拒绝: %v", err)
			}
		})
	}
}

func TestGFEXEnglishBudgetAndOldConstructors(t *testing.T) {
	g := gfexEnglishGrant(t)
	d, err := NewGFEXEnglishHomeDownloader(gfexEnglishLimits(), g)
	if err != nil {
		t.Fatal("真实冻结报道正向构造", err)
	}
	if n, b := d.Usage(); n != 0 || b != 0 {
		t.Fatal("构造器不应联网或计为实际获取")
	}
	for _, mutate := range []func(*Limits){func(l *Limits) { l.Requests = 0 }, func(l *Limits) { l.Requests = 2 }, func(l *Limits) { l.Bytes = 0 }, func(l *Limits) { l.Bytes++ }, func(l *Limits) { l.Interval = time.Millisecond }, func(l *Limits) { l.Timeout = 16 * time.Second }, func(l *Limits) { l.Timeout = l.Interval }} {
		l := gfexEnglishLimits()
		mutate(&l)
		if _, err := NewGFEXEnglishHomeDownloader(l, g); err == nil {
			t.Fatal("非法预算被接受")
		}
	}
	old, err := NewDownloader(&http.Client{Transport: nativeRoundTrip(func(*http.Request) (*http.Response, error) { t.Fatal("默认HTTP竟联网"); return nil, nil })}, gfexEnglishLimits())
	if err != nil {
		t.Fatal(err)
	}
	if r, err := old.FetchDetailed(context.Background(), g.URL); err == nil || r.Attempted || r.Failure != "invalid_url" {
		t.Fatal("默认HTTPS边界变化", r, err)
	}
	if _, err := NewNativeHTTPDownloader(nativeLimits(), []NativeHTTPGrant{{URL: g.URL, Method: g.Method, LinkedURL: g.URL, Source: g.Source, SourceBody: g.SourceBody}}); err == nil {
		t.Fatal("旧协会Grant不能接受新路径/报道")
	}
	oldGrant := testNativeGrant()
	oldGrant.Source, oldGrant.SourceBody = g.Source, g.SourceBody
	if _, err := NewNativeHTTPDownloader(nativeLimits(), []NativeHTTPGrant{oldGrant}); err == nil || !strings.Contains(err.Error(), "协会出处") {
		t.Fatal("旧合法业务路径也不得接受新闻出处", err)
	}
}

func TestGFEXEnglishExactFetchAndSingleAttempt(t *testing.T) {
	d, calls := gfexEnglishFixture(t, 200, "synthetic home")
	for _, address := range []string{"https://www.gfex.com.cn/en/index.shtml", "http://www.gfex.com.cn/", "http://www.gfex.com.cn/en/index.shtml?x=1", "http://localhost:1234/"} {
		if r, err := d.FetchDetailed(context.Background(), address); err == nil || r.Attempted || r.Failure != "invalid_url" || *calls != 0 {
			t.Fatalf("实例精确清单失效: %+v %v", r, err)
		}
	}
	r, err := d.FetchDetailed(context.Background(), "http://www.gfex.com.cn/en/index.shtml")
	g := gfexEnglishGrant(t)
	if err != nil || !r.Complete || r.Transport != "native-http" || !r.Unauthenticated || r.SourceURL != g.Source.URL || r.SourceSHA != g.Source.SHA256 || *calls != 1 {
		t.Fatalf("明文身份/正向请求不符: %+v %v", r, err)
	}
	if d.nativeHTTP[g.URL].SourceBody != nil {
		t.Fatal("不应持有可变的来源正文")
	}
	if r, err := d.FetchDetailed(context.Background(), g.URL); err == nil || r.Failure != "budget" || r.Attempted || *calls != 1 {
		t.Fatal("单次请求预算被绕过", r, err)
	}
}

func TestGFEXEnglishFailuresNeverRetry(t *testing.T) {
	for _, mode := range []string{"redirect", "challenge412", "oversize", "network", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			d, calls := gfexEnglishFixture(t, 200, "body")
			want := ""
			switch mode {
			case "redirect":
				d, calls = gfexEnglishFixture(t, 302, "redirect original")
				want = "http_status"
			case "challenge412":
				d, calls = gfexEnglishFixture(t, 412, "challenge original")
				want = "http_status"
			case "oversize":
				d, calls = gfexEnglishFixture(t, 200, strings.Repeat("x", (2<<20)+100))
				want = "byte_limit"
			case "network", "timeout":
				if mode == "timeout" {
					d.limits.Timeout = 10 * time.Millisecond // 只压缩合成Transport等待时间。
				}
				d.client.Transport = nativeRoundTrip(func(r *http.Request) (*http.Response, error) {
					*calls++
					if mode == "timeout" {
						<-r.Context().Done()
						return nil, r.Context().Err()
					}
					return nil, fmt.Errorf("synthetic connection failure")
				})
				want = "network"
			}
			r, err := d.FetchDetailed(context.Background(), GFEXEnglishHomeURL)
			if err == nil || r.Failure != want || *calls != 1 || !r.Attempted || r.Transport != "native-http" || !r.Unauthenticated {
				t.Fatalf("失败分类/身份/次数不符: %+v %v", r, err)
			}
			if mode == "oversize" && (len(r.Body) != (2<<20)+1 || r.Complete) {
				t.Fatal("超限正文必须有界且不完整")
			}
			if again, err := d.FetchDetailed(context.Background(), GFEXEnglishHomeURL); err == nil || again.Failure != "budget" || *calls != 1 {
				t.Fatal("失败后预算没有封口", again, err)
			}
		})
	}
}

func TestGFEXEnglishReceiptAndChallengeControls(t *testing.T) {
	for _, mode := range []string{"positive", "transport", "authentication", "source-url", "source-sha", "final-url", "no-downloader", "default-downloader", "challenge200"} {
		t.Run(mode, func(t *testing.T) {
			body := "synthetic business home"
			if mode == "challenge200" {
				body = "<html><script>synthetic challenge</script></html>"
			}
			d, calls := gfexEnglishFixture(t, 200, body)
			root := t.TempDir()
			jobs := []Job{{ID: "gfex-home", URL: GFEXEnglishHomeURL}}
			// 合成内容验证器，不声称已知真实GFEX首页的全部挑战形态。
			validate := func(_ Job, raw []byte, _ evidence.Ref) error {
				if string(raw) != "synthetic business home" {
					return fmt.Errorf("synthetic challenge or non-business content")
				}
				return nil
			}
			results, err := Run(context.Background(), root, jobs, d, 2<<20, validate)
			if err != nil || len(results) != 1 {
				t.Fatal("首次落盘", results, err)
			}
			r := results[0].Receipt
			want := "received"
			if mode == "challenge200" {
				want = "failed"
				if !strings.Contains(r.Error, "synthetic challenge") || !r.HTTP.Complete || r.Evidence.SHA256 != digest([]byte(body)) {
					t.Fatal("200挑战必须保留完整失败原件", r)
				}
			}
			if r.State != want {
				t.Fatal("内容状态错误", r)
			}
			// 明文身份在成功/失败原件上均需保留，恢复不得发第二次请求。
			if !r.HTTP.Unauthenticated || r.HTTP.Transport != "native-http" {
				t.Fatal("明文标签丢失")
			}
			if mode == "positive" || mode == "challenge200" {
				results, err = Run(context.Background(), root, jobs, d, 2<<20, validate)
				if err != nil || !results[0].Resumed || results[0].Receipt.State != want || *calls != 1 {
					t.Fatal("恢复重复请求或改变状态", err)
				}
				return
			}
			switch mode {
			case "transport":
				r.HTTP.Transport = "https"
			case "authentication":
				r.HTTP.Unauthenticated = false
			case "source-url":
				r.HTTP.SourceURL = "https://example.org/"
			case "source-sha":
				r.HTTP.SourceSHA = strings.Repeat("0", 64)
			case "final-url":
				r.HTTP.FinalURL = "http://www.gfex.com.cn/"
			case "no-downloader":
				d = nil
			case "default-downloader":
				d, _ = NewDownloader(&http.Client{}, gfexEnglishLimits())
			}
			raw, _ := json.Marshal(r)
			if err = os.WriteFile(filepath.Join(root, "gfex-home.json"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err = Run(context.Background(), root, jobs, d, 2<<20, validate); err == nil || !strings.Contains(err.Error(), "native-http") || *calls != 1 {
				t.Fatal("收据洗掉明文身份竟通过", err)
			}
		})
	}
}

func TestGFEXEnglishRealTransportStaysLocal(t *testing.T) {
	var calls syncatomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "GET" || r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("Proxy-Authorization") != "" {
			t.Error("真实Transport携密或非GET")
		}
		w.Header().Set("Location", "http://www.gfex.com.cn/other")
		w.Header().Set("Set-Cookie", "do-not-reuse=1")
		w.WriteHeader(302)
		fmt.Fprint(w, "local redirect original")
	}))
	defer server.Close()
	d, err := NewGFEXEnglishHomeDownloader(gfexEnglishLimits(), gfexEnglishGrant(t))
	if err != nil {
		t.Fatal(err)
	}
	transport := d.client.Transport.(*http.Transport)
	if !transport.DisableKeepAlives || d.client.Jar != nil || transport.TLSClientConfig != nil {
		t.Fatal("继承了账户状态或更改了TLS")
	}
	u, _ := url.Parse(server.URL)
	transport.Proxy = func(*http.Request) (*url.URL, error) { return u, nil }
	r, err := d.FetchDetailed(context.Background(), GFEXEnglishHomeURL)
	if err == nil || r.Status != 302 || r.Failure != "http_status" || string(r.Body) != "local redirect original" || calls.Load() != 1 {
		t.Fatal("本机重定向必须停止", r, err)
	}
}

func TestGFEXEnglishProxyCredentials(t *testing.T) {
	if os.Getenv("TICKFLOW_GFEX_PROXY_CONTROL") != "1" {
		// 独立子进程避免http.ProxyFromEnvironment缓存受到其他测试影响；不联网。
		cmd := exec.Command(os.Args[0], "-test.run=^TestGFEXEnglishProxyCredentials$")
		cmd.Env = append(os.Environ(), "TICKFLOW_GFEX_PROXY_CONTROL=1", "HTTP_PROXY=http://dummy:secret@127.0.0.1:9", "http_proxy=http://dummy:secret@127.0.0.1:9", "NO_PROXY=", "no_proxy=")
		if raw, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("合成代理凭据守卫失败: %s %v", raw, err)
		}
		return
	}
	d, err := NewGFEXEnglishHomeDownloader(gfexEnglishLimits(), gfexEnglishGrant(t))
	if err != nil {
		t.Fatal(err)
	}
	r, _ := http.NewRequest("GET", GFEXEnglishHomeURL, nil)
	proxy, err := d.client.Transport.(*http.Transport).Proxy(r)
	if err == nil || proxy != nil || !strings.Contains(err.Error(), "不继承代理账户凭据") {
		t.Fatal("含账户凭据的代理必须在连接前拒绝")
	}
}
