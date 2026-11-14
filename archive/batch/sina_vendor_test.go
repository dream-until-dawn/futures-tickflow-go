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

type sinaRoundTrip func(*http.Request) (*http.Response, error)

func (f sinaRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func sinaLimits() Limits {
	return Limits{Requests: 40, Bytes: 20 << 20, Interval: time.Second, Timeout: 15 * time.Second}
}

const sinaList = "https://vip.stock.finance.sina.com.cn/quotes_service/api/json_v2.php/Market_Center.getHQFuturesData?page=1&sort=position&asc=0&node=qh_c&base=futures"

func sinaDown(t *testing.T, urls []string, status int, body string, calls *int) *Downloader {
	t.Helper()
	d, e := NewSinaVendorDownloader(sinaLimits(), urls)
	if e != nil {
		t.Fatal(e)
	}
	d.client.Transport = sinaRoundTrip(func(r *http.Request) (*http.Response, error) {
		*calls++
		if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("Referer") != "https://finance.sina.com.cn" {
			t.Fatal("账户头/Referer异常")
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: r, ContentLength: int64(len(body))}, nil
	})
	return d
}

func TestSinaVendorNetworkBudgetAndStopHost(t *testing.T) {
	alias := strings.Replace(sinaList, "page=1&sort=position&asc=0&node=qh_c&base=futures", "base=futures&node=qh_c&asc=0&sort=position&page=1", 1)
	if _, e := NewSinaVendorDownloader(sinaLimits(), []string{sinaList, alias}); e == nil {
		t.Fatal("参数重排重复请求")
	}
	daily := "https://stock2.finance.sina.com.cn/futures/api/jsonp.php/var%20_=/InnerFuturesNewService.getDailyKLine?symbol=C2701&type=2021_04_12"
	if _, e := NewSinaVendorDownloader(sinaLimits(), []string{daily, strings.Replace(daily, "%20_=", "%20_other=", 1)}); e == nil {
		t.Fatal("换callback重复请求")
	}
	for _, status := range []int{403, 412, 429, 200} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			body := "denied"
			if status == 200 {
				body = "<html><script>challenge</script></html>"
			}
			d := sinaDown(t, []string{SinaProductMapURL, sinaList}, status, body, &calls)
			r, e := d.FetchDetailed(context.Background(), SinaProductMapURL)
			if e == nil || !r.Attempted || string(r.Body) != body || r.DataClass != SinaVendorVersion {
				t.Fatalf("%+v %v", r, e)
			}
			r, e = d.FetchDetailed(context.Background(), sinaList)
			if e == nil || r.Attempted || calls != 1 || r.Failure != "vendor_gate" {
				t.Fatalf("停主机失效 %+v %v calls%d", r, e, calls)
			}
		})
	}
	calls := 0
	d := sinaDown(t, []string{SinaProductMapURL}, 200, "data", &calls)
	r, e := d.FetchDetailed(context.Background(), SinaProductMapURL)
	if e != nil || !r.Complete || r.DataClass != SinaVendorVersion {
		t.Fatalf("%+v %v", r, e)
	}
	if _, e = d.FetchDetailed(context.Background(), SinaProductMapURL); e == nil || calls != 1 {
		t.Fatal("重复请求")
	}
	d = sinaDown(t, []string{SinaProductMapURL}, 200, strings.Repeat("x", (1<<20)+2), &calls)
	r, e = d.FetchDetailed(context.Background(), SinaProductMapURL)
	if e == nil || len(r.Body) != (1<<20)+1 || r.Complete || r.Failure != "byte_limit" {
		t.Fatalf("oversize %+v %v", r, e)
	}
	d = sinaDown(t, []string{SinaProductMapURL}, 200, "x", &calls)
	d.requests = 40
	r, e = d.FetchDetailed(context.Background(), SinaProductMapURL)
	if e == nil || r.Attempted || r.Failure != "budget" {
		t.Fatal("请求预算")
	}
	d = sinaDown(t, []string{SinaProductMapURL}, 200, "x", &calls)
	d.bytes = 20 << 20
	r, e = d.FetchDetailed(context.Background(), SinaProductMapURL)
	if e == nil || r.Attempted {
		t.Fatal("字节预算")
	}
	d = sinaDown(t, []string{SinaProductMapURL}, 200, "x", &calls)
	d.client.Transport = sinaRoundTrip(func(*http.Request) (*http.Response, error) { calls++; return nil, fmt.Errorf("connect failed") })
	r, e = d.FetchDetailed(context.Background(), SinaProductMapURL)
	if e == nil || !r.Attempted || r.ResponseReceived || r.Failure != "network" {
		t.Fatal("连接失败身份")
	}
	if _, e = d.FetchDetailed(context.Background(), SinaProductMapURL); e == nil {
		t.Fatal("网络失败重试")
	}
	d = sinaDown(t, []string{SinaProductMapURL}, 302, "redirect", &calls)
	r, e = d.FetchDetailed(context.Background(), SinaProductMapURL)
	if e == nil || r.Status != 302 {
		t.Fatal("跳转不能成功")
	}
	d = sinaDown(t, []string{SinaProductMapURL}, 200, "x", &calls)
	d.limits.Timeout = 25 * time.Millisecond
	d.client.Transport = sinaRoundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	start := time.Now()
	r, e = d.FetchDetailed(context.Background(), SinaProductMapURL)
	if e == nil || !r.Attempted || r.ResponseReceived || time.Since(start) > time.Second || r.Failure != "network" {
		t.Fatalf("超时诊断 %+v %v", r, e)
	}
	for _, a := range []string{"http://vip.stock.finance.sina.com.cn/quotes_service/view/js/qihuohangqing.js", SinaProductMapURL + "?x=1", "https://user:pass@vip.stock.finance.sina.com.cn/quotes_service/view/js/qihuohangqing.js", sinaList + "&page=2", strings.Replace(sinaList, "page=1", "page=2", 1), "https://www.gfex.com.cn/", SinaProductMapURL + "#x"} {
		if _, e := NewSinaVendorDownloader(sinaLimits(), []string{a}); e == nil {
			t.Fatalf("bad URL %s", a)
		}
	}
	for _, l := range []Limits{{41, 20 << 20, time.Second, 15 * time.Second}, {40, (20 << 20) + 1, time.Second, 15 * time.Second}, {40, 20 << 20, time.Millisecond, 15 * time.Second}, {40, 20 << 20, time.Second, 16 * time.Second}} {
		if _, e := NewSinaVendorDownloader(l, []string{SinaProductMapURL}); e == nil {
			t.Fatal("超限构造")
		}
	}
}

func TestSinaVendorResumeIdentityBudgetAndUnknown(t *testing.T) {
	accept := func(Job, []byte, evidence.Ref) error { return nil }
	for _, status := range []int{200, 403} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			root := t.TempDir()
			calls := 0
			jobs := []Job{{ID: "map", URL: SinaProductMapURL, Version: SinaVendorVersion}}
			d := sinaDown(t, []string{SinaProductMapURL}, status, "data", &calls)
			rr, e := Run(context.Background(), root, jobs, d, 1<<20, accept)
			if e != nil || len(rr) != 1 {
				t.Fatal(e)
			}
			jobs = append(jobs, Job{ID: "list", URL: sinaList, Version: SinaVendorVersion})
			d = sinaDown(t, []string{SinaProductMapURL, sinaList}, 200, "data", &calls)
			d.limits.Interval = time.Millisecond
			rr, e = Run(context.Background(), root, jobs, d, 1<<20, accept)
			if e != nil || !rr[0].Resumed {
				t.Fatal(e)
			}
			n, _ := d.Usage()
			if status == 403 {
				if calls != 1 || n != 1 || rr[1].Receipt.State != "failed" || rr[1].Receipt.HTTP.Attempted {
					t.Fatalf("stop恢复 %+v calls%d n%d", rr, calls, n)
				}
			} else {
				if calls != 2 || n != 2 || rr[1].Receipt.State != "received" {
					t.Fatalf("%+v %d %d", rr, calls, n)
				}
			}
			if _, e = Run(context.Background(), root, jobs, nil, 1<<20, accept); e == nil {
				t.Fatal("无显式策略恢复")
			}
			b, e := os.ReadFile(filepath.Join(root, "map.json"))
			if e != nil {
				t.Fatal(e)
			}
			// 全批恢复先核所有收据；新项排在未知状态之前也不得先发请求。
			if status == 200 {
				var badState Receipt
				if e = json.Unmarshal(b, &badState); e != nil {
					t.Fatal(e)
				}
				badState.State = "alien"
				if e = atomic(filepath.Join(root, "map.json"), badState); e != nil {
					t.Fatal(e)
				}
				before := calls
				down := sinaDown(t, []string{SinaProductMapURL, sinaList}, 200, "data", &calls)
				if _, e = Run(context.Background(), root, []Job{jobs[1], jobs[0]}, down, 1<<20, accept); e == nil || calls != before || !strings.Contains(e.Error(), "状态/长度") {
					t.Fatal(e)
				}
				if e = os.WriteFile(filepath.Join(root, "map.json"), b, 0600); e != nil {
					t.Fatal(e)
				}
			}
			bad := strings.Replace(string(b), `"data_class": "sina-vendor-v1"`, `"data_class": "official"`, 1)
			if e = os.WriteFile(filepath.Join(root, "map.json"), []byte(bad), 0600); e != nil {
				t.Fatal(e)
			}
			d = sinaDown(t, []string{SinaProductMapURL, sinaList}, 200, "data", &calls)
			if _, e = Run(context.Background(), root, jobs, d, 1<<20, accept); e == nil || !strings.Contains(e.Error(), "身份") {
				t.Fatal(e)
			}
		})
	}
	root := t.TempDir()
	j := Job{ID: "unknown", URL: SinaProductMapURL, Version: SinaVendorVersion}
	b, _ := jsonJob(j)
	if e := atomic(filepath.Join(root, "unknown.json"), Receipt{JobSHA: digest(b), State: "in_progress", BodyState: "none"}); e != nil {
		t.Fatal(e)
	}
	calls := 0
	d := sinaDown(t, []string{SinaProductMapURL}, 200, "data", &calls)
	if _, e := Run(context.Background(), root, []Job{j}, d, 1<<20, accept); e == nil || calls != 0 || !strings.Contains(e.Error(), "未知旧启动") {
		t.Fatal(e)
	}
}

func jsonJob(j Job) ([]byte, error) { return json.Marshal(j) }
