package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dream-until-dawn/futures-tickflow-go/archive/batch"
	"github.com/dream-until-dawn/futures-tickflow-go/refdata/evidence"
	"github.com/dream-until-dawn/futures-tickflow-go/source/sinavendor"
)

const nullProfileTestPath = "../../../docs/evidence/sina-null-isolation-20261114/profile.json"
const exactNull = `/*<script>location.href='//sina.com';</script>*/
var _V21052021_4_12=(null);`
const oneDaily = `var _V21052021_4_12=([{"d":"2026-11-13","o":"1","h":"2","l":"1","c":"1","v":"1","p":"1","s":"1"}]);`

type fakeDailyFetcher struct {
	Calls int
	Next  func(int, string) (batch.Download, error)
}

func (f *fakeDailyFetcher) FetchDetailed(_ context.Context, u string) (batch.Download, error) {
	f.Calls++
	return f.Next(f.Calls, u)
}

func toolDownload(address, body string) batch.Download {
	return batch.Download{Body: []byte(body), Attempted: true, ResponseReceived: true, Complete: true, Status: 200, FinalURL: address, Unauthenticated: true, DataClass: batch.SinaVendorVersion, RequestedAt: time.Date(2026, 11, 14, 14, 0, 0, 0, time.UTC)}
}
func toolDoc(p *verifiedNullProfile, d batch.Download) sinavendor.Document {
	return sinavendor.Document{Symbol: p.Symbols[d.FinalURL], Raw: d.Body, Evidence: evidence.Ref{ID: "fake-unit-test", URL: d.FinalURL, SHA256: bytesDigest(d.Body), RetrievedAt: d.RequestedAt, Version: nullProfileVersion}}
}
func nullTestPlan(t *testing.T) (*verifiedNullProfile, []batch.Job, batch.Limits) {
	t.Helper()
	p, e := loadNullProfile(nullProfileTestPath)
	if e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile("../../../docs/evidence/sina-null-isolation-20261114/batch-01.json")
	if e != nil {
		t.Fatal(e)
	}
	var jobs []batch.Job
	if e = json.Unmarshal(b, &jobs); e != nil {
		t.Fatal(e)
	}
	jobs = jobs[:2]
	return p, jobs, batch.Limits{Requests: 2, Bytes: 3 << 20, Interval: time.Second, Timeout: 15 * time.Second}
}

func TestToolExactNullGuardRejectsEveryOtherForm(t *testing.T) {
	p, jobs, _ := nullTestPlan(t)
	d := toolDownload(jobs[0].URL, exactNull)
	doc := toolDoc(p, d)
	if !isExactNullResponse(d, doc) {
		t.Fatal("精确null正控失败")
	}
	if rows, e := sinavendor.Parse(doc); e == nil || rows != nil {
		t.Fatal("公开Source必须仍拒绝null")
	}
	for _, tc := range []struct {
		name   string
		change func(*batch.Download, *sinavendor.Document)
	}{
		{"non200", func(d *batch.Download, _ *sinavendor.Document) { d.Status = 403 }},
		{"redirect", func(d *batch.Download, _ *sinavendor.Document) { d.Status = 302 }},
		{"incomplete", func(d *batch.Download, _ *sinavendor.Document) { d.Complete = false }},
		{"no-response", func(d *batch.Download, _ *sinavendor.Document) { d.ResponseReceived = false }},
		{"not-attempted", func(d *batch.Download, _ *sinavendor.Document) { d.Attempted = false }},
		{"failure", func(d *batch.Download, _ *sinavendor.Document) { d.Failure = "read_error" }},
		{"wrong-final", func(d *batch.Download, _ *sinavendor.Document) { d.FinalURL += "&x=1" }},
		{"wrong-sha", func(_ *batch.Download, doc *sinavendor.Document) { doc.Evidence.SHA256 = strings.Repeat("0", 64) }},
		{"wrong-class", func(d *batch.Download, _ *sinavendor.Document) { d.DataClass = "official" }},
		{"account-unknown", func(d *batch.Download, _ *sinavendor.Document) { d.Unauthenticated = false }},
		{"native-http-label", func(d *batch.Download, _ *sinavendor.Document) { d.Transport = "native-http" }},
		{"wrong-time", func(_ *batch.Download, doc *sinavendor.Document) {
			doc.Evidence.RetrievedAt = doc.Evidence.RetrievedAt.Add(time.Second)
		}},
		{"wrong-request", func(d *batch.Download, doc *sinavendor.Document) {
			doc.Evidence.URL = strings.Replace(doc.Evidence.URL, "symbol=B2612", "symbol=C2612", 1)
			d.FinalURL = doc.Evidence.URL
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad, copyDoc := d, doc
			tc.change(&bad, &copyDoc)
			if isExactNullResponse(bad, copyDoc) {
				t.Fatal("坏元数据/身份可隔离")
			}
		})
	}
	for _, body := range []string{`var _V21052021_4_12=(false);`, `var _V21052021_4_12=([]);`, `var _V21052021_4_12=("null");`, `var _V21052021_4_12=(null);evil();`, `var _other=(null);`, `<html>var _V21052021_4_12=(null);</html>`, `var _V21052021_4_12=(null)`} {
		bad := toolDownload(jobs[0].URL, body)
		if isExactNullResponse(bad, toolDoc(p, bad)) {
			t.Fatalf("非精确null可隔离 %s", body)
		}
	}
}

func TestToolNullArchiveIsFailureThenContinuesWithoutCoverage(t *testing.T) {
	p, jobs, l := nullTestPlan(t)
	fake := &fakeDailyFetcher{Next: func(n int, u string) (batch.Download, error) {
		body := oneDaily
		if n == 1 {
			body = exactNull
		}
		return toolDownload(u, body), nil
	}}
	root := filepath.Join(t.TempDir(), "batch")
	report, e := archiveNullBatch(context.Background(), root, jobs, l, p, fake)
	if e != nil || fake.Calls != 2 || report.Quarantined != 1 || report.Received != 1 || report.HostStopped || report.Covered || report.FinancialEligible || report.Results[0].State != "failed-quarantined" || report.Results[0].Records != 0 || report.Results[0].Covered {
		t.Fatalf("%+v %v calls%d", report, e, fake.Calls)
	}
	b, e := os.ReadFile(filepath.Join(root, "raw", jobs[0].ID+".jsonp"))
	if e != nil || string(b) != exactNull {
		t.Fatal("原null字节未保留")
	}
	if _, e = archiveNullBatch(context.Background(), root, jobs, l, p, fake); e == nil || fake.Calls != 2 {
		t.Fatal("既有/未知目录重发")
	}
}

func TestToolNullOtherFailuresStopFollowingRequests(t *testing.T) {
	for _, tc := range []struct {
		name, body   string
		status       int
		transportErr bool
	}{{"http403", exactNull, 403, true}, {"network", exactNull, 0, true}, {"empty-array", `var _V21052021_4_12=([]);`, 200, false}, {"false", `var _V21052021_4_12=(false);`, 200, false}, {"script", exactNull + "evil();", 200, false}} {
		t.Run(tc.name, func(t *testing.T) {
			p, jobs, l := nullTestPlan(t)
			fake := &fakeDailyFetcher{Next: func(_ int, u string) (batch.Download, error) {
				d := toolDownload(u, tc.body)
				d.Status = tc.status
				if tc.transportErr {
					return d, fmt.Errorf("HTTP/连接失败")
				}
				return d, nil
			}}
			report, e := archiveNullBatch(context.Background(), filepath.Join(t.TempDir(), "batch"), jobs, l, p, fake)
			if e == nil || fake.Calls != 1 || !report.HostStopped || report.Quarantined != 0 || report.Results[1].State != "not-attempted-after-global-stop" {
				t.Fatalf("%+v %v calls%d", report, e, fake.Calls)
			}
		})
	}
}

func TestToolNullPlanRejectsOldRequestBeforeFetch(t *testing.T) {
	p, jobs, l := nullTestPlan(t)
	old := jobs[0]
	old.ID = "daily-A2711"
	old.Product = "a"
	old.URL = "https://stock2.finance.sina.com.cn/futures/api/jsonp.php/var%20_V21052021_4_12=/InnerFuturesNewService.getDailyKLine?symbol=A2711&type=2021_04_12"
	jobs[0] = old
	fake := &fakeDailyFetcher{Next: func(_ int, u string) (batch.Download, error) { return toolDownload(u, oneDaily), nil }}
	if _, e := archiveNullBatch(context.Background(), filepath.Join(t.TempDir(), "batch"), jobs, l, p, fake); e == nil || !strings.Contains(e.Error(), "175未尝试清单") || fake.Calls != 0 {
		t.Fatalf("旧请求防线 %v calls%d", e, fake.Calls)
	}
	_, jobs, l = nullTestPlan(t)
	jobs[1] = jobs[0]
	if _, e := archiveNullBatch(context.Background(), filepath.Join(t.TempDir(), "batch"), jobs, l, p, fake); e == nil || !strings.Contains(e.Error(), "重复URL") || fake.Calls != 0 {
		t.Fatal("重复计划未拒绝")
	}
}

func TestToolNullProfileRejectsShadowConfig(t *testing.T) {
	for _, body := range []string{`{"version":"x","version":"y"}`, `{"Version":"x"}`, `{"version":"x"} {}`, `[]`} {
		var p nullProfile
		if e := decodeNullProfile([]byte(body), &p); e == nil {
			t.Fatal("重复/影子/尾部配置通过")
		}
	}
}

func TestToolNullCancellationUnknownDirectoryAndBudget(t *testing.T) {
	p, jobs, l := nullTestPlan(t)
	fake := &fakeDailyFetcher{Next: func(_ int, u string) (batch.Download, error) { return toolDownload(u, oneDaily), nil }}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, e := archiveNullBatch(ctx, filepath.Join(t.TempDir(), "cancelled"), jobs, l, p, fake)
	if e != context.Canceled || fake.Calls != 0 || !r.HostStopped || r.Requests != 0 {
		t.Fatalf("取消后发请求 %+v %v", r, e)
	}
	unknown := t.TempDir()
	if e = os.WriteFile(filepath.Join(unknown, "in-progress.json"), []byte(`{"state":"in_progress"}`), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = archiveNullBatch(context.Background(), unknown, jobs, l, p, fake); e == nil || fake.Calls != 0 {
		t.Fatal("未知启动重发")
	}
	bad := l
	bad.Bytes = 20 << 20
	if _, e = archiveNullBatch(context.Background(), filepath.Join(t.TempDir(), "too-large"), jobs, bad, p, fake); e == nil || fake.Calls != 0 {
		t.Fatal("误用旧20MiB预算")
	}
}
