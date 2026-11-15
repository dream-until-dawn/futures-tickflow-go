package batch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dream-until-dawn/futures-tickflow-go/refdata/evidence"
)

type staticWaitingBody struct{ ctx context.Context }

func (b staticWaitingBody) Read([]byte) (int, error) { <-b.ctx.Done(); return 0, b.ctx.Err() }
func (b staticWaitingBody) Close() error             { return nil }

func staticHTTPGrant(t *testing.T) SinaStaticHTTPGrant {
	t.Helper()
	base := "../../docs/evidence/sina-history-html-20261115"
	body, err := os.ReadFile(filepath.Join(base, "history.jsonp"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(base, "receipt.json"))
	if err != nil {
		t.Fatal(err)
	}
	var r Receipt
	if err = json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	return SinaStaticHTTPGrant{URL: sinaStaticHTTPURL, Method: http.MethodGet, Source: r.Evidence, SourceBody: body}
}
func staticHTTPLimits() Limits {
	return Limits{Requests: 1, Bytes: (1 << 20) - 1, Interval: time.Second, Timeout: 3 * time.Second}
}
func staticHTTPFixture(t *testing.T) (*Downloader, *int) {
	t.Helper()
	d, err := NewSinaStaticHTTPDownloader(staticHTTPLimits(), staticHTTPGrant(t))
	if err != nil {
		t.Fatal(err)
	}
	calls := new(int)
	d.client.Transport = nativeRoundTrip(func(r *http.Request) (*http.Response, error) {
		*calls++
		if r.URL.String() != sinaStaticHTTPURL || r.Method != http.MethodGet || r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("Proxy-Authorization") != "" {
			t.Fatal("单URL匿名GET身份错误")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("var JYS = new Array;")), Request: r}, nil
	})
	return d, calls
}

func TestSinaStaticHTTPExactGrantAndOldDefaults(t *testing.T) {
	good := staticHTTPGrant(t)
	for _, url := range []string{sinaStaticHTTPURL + "?", sinaStaticHTTPURL + "#", strings.Replace(sinaStaticHTTPURL, "http:", "https:", 1), "http://finance.sina.com.cn:80/iframe/futures_info_cff.js", "http://user:secret@finance.sina.com.cn/iframe/futures_info_cff.js", "http://example.org/", "http://127.0.0.1/", strings.Replace(sinaStaticHTTPURL, "iframe/", "iframe/../iframe/", 1)} {
		g := good
		g.URL = url
		if _, err := NewSinaStaticHTTPDownloader(staticHTTPLimits(), g); err == nil {
			t.Fatalf("非精确URL通过 %s", url)
		}
	}
	for _, change := range []func(*SinaStaticHTTPGrant){func(g *SinaStaticHTTPGrant) { g.Method = "POST" }, func(g *SinaStaticHTTPGrant) { g.Source.URL = "https://example.org/" }, func(g *SinaStaticHTTPGrant) { g.Source.SHA256 = strings.Repeat("0", 64) }, func(g *SinaStaticHTTPGrant) { g.SourceBody = []byte("changed") }} {
		g := good
		change(&g)
		if _, err := NewSinaStaticHTTPDownloader(staticHTTPLimits(), g); err == nil {
			t.Fatal("坏GET/出处/SHA通过")
		}
	}
	for _, change := range []func(*Limits){func(l *Limits) { l.Requests = 2 }, func(l *Limits) { l.Bytes = 1 << 20 }, func(l *Limits) { l.Timeout = 16 * time.Second }, func(l *Limits) { l.Interval = time.Millisecond }, func(l *Limits) { l.Timeout = 0 }} {
		l := staticHTTPLimits()
		change(&l)
		if _, err := NewSinaStaticHTTPDownloader(l, good); err == nil {
			t.Fatal("越界预算通过")
		}
	}
	old, _ := NewDownloader(&http.Client{Transport: nativeRoundTrip(func(*http.Request) (*http.Response, error) { t.Fatal("旧默认竟联网"); return nil, nil })}, staticHTTPLimits())
	result, err := old.FetchDetailed(context.Background(), sinaStaticHTTPURL)
	if err == nil || result.Attempted || result.Failure != "invalid_url" {
		t.Fatalf("旧HTTP默认改变 %+v %v", result, err)
	}
	if _, err := NewNativeHTTPDownloader(nativeLimits(), []NativeHTTPGrant{{URL: good.URL, Method: good.Method, LinkedURL: good.URL, Source: good.Source, SourceBody: good.SourceBody}}); err == nil {
		t.Fatal("Sina混入CFA旧入口")
	}
}

func TestSinaStaticHTTPPinsOriginalSourceHashEvenWhenRehashed(t *testing.T) {
	g := staticHTTPGrant(t)
	g.SourceBody = append(append([]byte{}, g.SourceBody...), '\n')
	g.Source.SHA256 = digest(g.SourceBody)
	if _, err := NewSinaStaticHTTPDownloader(staticHTTPLimits(), g); err == nil || !strings.Contains(err.Error(), "出处身份/SHA") {
		t.Fatalf("重算SHA竟绕过冻结出处: %v", err)
	}
}

func TestSinaStaticHTTPIdentityAndSingleAttempt(t *testing.T) {
	d, calls := staticHTTPFixture(t)
	for _, url := range []string{"http://finance.sina.com.cn/", "https://finance.sina.com.cn/iframe/futures_info_cff.js", "http://localhost/"} {
		r, err := d.FetchDetailed(context.Background(), url)
		if err == nil || r.Attempted || *calls != 0 {
			t.Fatal("精确清单失效", r, err)
		}
	}
	r, err := d.FetchDetailed(context.Background(), sinaStaticHTTPURL)
	if err != nil || r.DataClass != SinaStaticHTTPVersion || r.Transport != "native-http" || !r.Unauthenticated || r.SourceSHA != sinaStaticSourceSHA || r.SourceURL != sinaStaticSourceURL || !r.Complete {
		t.Fatal("明文身份不完整", r, err)
	}
	r, err = d.FetchDetailed(context.Background(), sinaStaticHTTPURL)
	if err == nil || r.Attempted || r.Failure != "budget" || *calls != 1 {
		t.Fatal("一次预算失效", r, err)
	}
}

func TestSinaStaticHTTPReceiptCannotLoseVendorClass(t *testing.T) {
	d, calls := staticHTTPFixture(t)
	root := t.TempDir()
	job := Job{ID: "static", URL: sinaStaticHTTPURL, Version: SinaStaticHTTPVersion}
	accept := func(Job, []byte, evidence.Ref) error { return nil }
	results, err := Run(context.Background(), root, []Job{job}, d, 1<<20, accept)
	if err != nil || results[0].Receipt.State != "received" {
		t.Fatal(results, err)
	}
	receipt := results[0].Receipt
	receipt.HTTP.DataClass = ""
	if err = atomic(filepath.Join(root, "static.json"), receipt); err != nil {
		t.Fatal(err)
	}
	if _, err = Run(context.Background(), root, []Job{job}, d, 1<<20, accept); err == nil || !strings.Contains(err.Error(), "明文供应商身份") {
		t.Fatalf("供应商身份洗白未拒: %v", err)
	}
	if *calls != 1 {
		t.Fatal("坏恢复引发重试")
	}
}

func TestSinaStaticHTTPBoundedFailures(t *testing.T) {
	for _, mode := range []string{"oversize", "redirect", "challenge", "empty", "403", "412", "429", "network", "timeout", "body-timeout"} {
		t.Run(mode, func(t *testing.T) {
			d, calls := staticHTTPFixture(t)
			want := ""
			body := ""
			status := 200
			switch mode {
			case "oversize":
				body = strings.Repeat("x", (1<<20)+100)
				want = "byte_limit"
			case "redirect":
				body = "redirect"
				status = 302
				want = "http_status"
			case "challenge":
				body = "<script>document.cookie='x';</script>"
				want = "static_challenge"
			case "empty":
				want = "static_challenge"
			case "403", "412", "429":
				fmt.Sscanf(mode, "%d", &status)
				body = "denied"
				want = "http_status"
			case "network", "timeout":
				want = "network"
			case "body-timeout":
				want = "read_error"
			}
			d.client.Transport = nativeRoundTrip(func(req *http.Request) (*http.Response, error) {
				*calls++
				if mode == "network" {
					return nil, fmt.Errorf("synthetic connection failure")
				}
				if mode == "timeout" {
					<-req.Context().Done()
					return nil, req.Context().Err()
				}
				if mode == "body-timeout" {
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: staticWaitingBody{req.Context()}, Request: req}, nil
				}
				header := make(http.Header)
				if mode == "redirect" {
					header.Set("Location", "https://example.org/")
				}
				return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
			})
			if mode == "timeout" || mode == "body-timeout" {
				d.limits.Timeout = 20 * time.Millisecond
			}
			before := time.Now()
			r, err := d.FetchDetailed(context.Background(), sinaStaticHTTPURL)
			if err == nil || r.Failure != want || !r.Attempted || len(r.Body) > 1<<20 || *calls != 1 || time.Since(before) > time.Second {
				t.Fatalf("有界失败 %s: %+v %v", mode, r, err)
			}
			again, e := d.FetchDetailed(context.Background(), sinaStaticHTTPURL)
			if e == nil || again.Attempted || *calls != 1 {
				t.Fatal("失败后重试", again, e)
			}
		})
	}
}
