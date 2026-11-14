package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dream-until-dawn/futures-tickflow-go/archive/batch"
	"github.com/dream-until-dawn/futures-tickflow-go/refdata/evidence"
	"github.com/dream-until-dawn/futures-tickflow-go/source/sinavendor"
)

type dailyFetcher interface {
	FetchDetailed(context.Context, string) (batch.Download, error)
}

type isolatedReceipt struct {
	ID                string          `json:"id"`
	State             string          `json:"state"`
	FailureClass      string          `json:"failure_class,omitempty"`
	Error             string          `json:"error,omitempty"`
	Evidence          *evidence.Ref   `json:"evidence,omitempty"`
	HTTP              *batch.Download `json:"http,omitempty"`
	Bytes             int             `json:"bytes"`
	Records           int             `json:"records"`
	Covered           bool            `json:"covered"`
	FinancialEligible bool            `json:"financial_eligible"`
}

type isolationReport struct {
	Policy            string            `json:"policy"`
	ProfileSHA        string            `json:"profile_sha256"`
	State             string            `json:"state"`
	Requests          int               `json:"requests"`
	Bytes             int               `json:"bytes"`
	Received          int               `json:"received"`
	Quarantined       int               `json:"quarantined"`
	HostStopped       bool              `json:"host_stopped"`
	Covered           bool              `json:"covered"`
	FinancialEligible bool              `json:"financial_eligible"`
	Results           []isolatedReceipt `json:"results"`
}

func writeOnceJSON(path string, value any) error {
	b, e := json.MarshalIndent(value, "", "  ")
	if e != nil {
		return e
	}
	return writeOnceBytes(path, append(b, '\n'))
}
func writeOnceBytes(path string, b []byte) error {
	f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return e
	}
	_, e = f.Write(b)
	closeErr := f.Close()
	if e != nil {
		return e
	}
	return closeErr
}

func validToolSinaHTTP(d batch.Download, address string) bool {
	return d.Attempted && d.ResponseReceived && d.Complete && d.Status == 200 && d.Failure == "" && d.FinalURL == address && d.Unauthenticated && d.DataClass == batch.SinaVendorVersion && d.Transport == "" && !d.RequestedAt.IsZero()
}

// isExactNullResponse 不执行包装；全部HTTP/身份守卫与准确null必须同时成立。
func isExactNullResponse(d batch.Download, doc sinavendor.Document) bool {
	if !validToolSinaHTTP(d, doc.Evidence.URL) || !bytes.Equal(d.Body, doc.Raw) || !d.RequestedAt.Equal(doc.Evidence.RetrievedAt) {
		return false
	}
	if doc.Evidence.Verify(doc.Raw) != nil {
		return false
	}
	b := bytes.TrimSpace(doc.Raw)
	b = bytes.TrimSpace(bytes.TrimPrefix(b, []byte(`/*<script>location.href='//sina.com';</script>*/`)))
	if !bytes.Equal(b, []byte(`var _V21052021_4_12=(null);`)) {
		return false
	}
	// 由未改动的公开解析器再次核请求URL/合约/callback，错误必须是数组不可得。
	rows, e := sinavendor.Parse(doc)
	return rows == nil && e != nil && strings.Contains(e.Error(), "未取得数组")
}

func runNullIsolation(ctx context.Context, root string, jobs []batch.Job, l batch.Limits, profilePath string) (*isolationReport, error) {
	p, e := loadNullProfile(profilePath)
	if e != nil {
		return nil, e
	}
	addresses, e := validateNullJobs(jobs, l, p)
	if e != nil {
		return nil, e
	}
	d, e := batch.NewSinaVendorDownloader(l, addresses)
	if e != nil {
		return nil, e
	}
	return archiveNullBatch(ctx, root, jobs, l, p, d)
}

// archiveNullBatch 是独立工具归档器：不改batch.Run，也不把null验证错误返回nil洗成received。
// 目录只创建一次；任何未知落盘/旧目录均不可自动续发。
func archiveNullBatch(ctx context.Context, root string, jobs []batch.Job, l batch.Limits, p *verifiedNullProfile, fetch dailyFetcher) (*isolationReport, error) {
	if _, e := validateNullJobs(jobs, l, p); e != nil {
		return nil, e
	}
	if e := os.Mkdir(root, 0700); e != nil {
		return nil, fmt.Errorf("null工具结果目录已存在或未知启动，拒绝重发: %w", e)
	}
	for _, folder := range []string{"raw", "receipts", "started"} {
		if e := os.Mkdir(filepath.Join(root, folder), 0700); e != nil {
			return nil, e
		}
	}
	if e := writeOnceJSON(filepath.Join(root, "plan.json"), jobs); e != nil {
		return nil, e
	}
	report := &isolationReport{Policy: nullProfileVersion, ProfileSHA: p.SHA, State: "in_progress"}
	var failure error
	for _, j := range jobs {
		if report.HostStopped {
			report.Results = append(report.Results, isolatedReceipt{ID: j.ID, State: "not-attempted-after-global-stop"})
			continue
		}
		if e := ctx.Err(); e != nil {
			failure = e
			report.HostStopped = true
			report.Results = append(report.Results, isolatedReceipt{ID: j.ID, State: "not-attempted-after-global-stop", Error: e.Error()})
			continue
		}
		if e := writeOnceJSON(filepath.Join(root, "started", j.ID+".json"), map[string]any{"state": "in_progress", "job": j, "profile_sha256": p.SHA, "marker_created_at": time.Now().UTC()}); e != nil {
			return nil, e
		}
		download, fetchErr := fetch.FetchDetailed(ctx, j.URL)
		meta := download
		meta.Body = nil
		r := isolatedReceipt{ID: j.ID, State: "failed", HTTP: &meta, Bytes: len(download.Body)}
		if download.Attempted {
			report.Requests++
		}
		report.Bytes += len(download.Body)
		var doc sinavendor.Document
		if download.ResponseReceived {
			ref := evidence.Ref{ID: j.ID, URL: j.URL, SHA256: bytesDigest(download.Body), RetrievedAt: download.RequestedAt, Version: nullProfileVersion}
			r.Evidence = &ref
			doc = sinavendor.Document{Symbol: p.Symbols[j.URL], Evidence: ref, Raw: download.Body}
			if e := writeOnceBytes(filepath.Join(root, "raw", j.ID+".jsonp"), download.Body); e != nil {
				return nil, e
			}
		}
		switch {
		case fetchErr != nil:
			failure = fetchErr
			r.FailureClass = "transport-or-http"
			r.Error = fetchErr.Error()
			report.HostStopped = true
		case !validToolSinaHTTP(download, j.URL):
			failure = fmt.Errorf("下载元数据不符")
			r.FailureClass = "unexpected-metadata"
			r.Error = failure.Error()
			report.HostStopped = true
		default:
			rows, e := sinavendor.Parse(doc)
			if e == nil {
				r.State = "received"
				r.Records = len(rows)
				report.Received++
			} else if isExactNullResponse(download, doc) && report.Quarantined < p.Profile.MaxQuarantined {
				r.State = "failed-quarantined"
				r.FailureClass = "exact-null-unavailable"
				r.Error = e.Error()
				report.Quarantined++
			} else {
				failure = e
				r.FailureClass = "business-or-quarantine-limit"
				r.Error = e.Error()
				report.HostStopped = true
			}
		}
		if e := writeOnceJSON(filepath.Join(root, "receipts", j.ID+".json"), r); e != nil {
			return nil, e
		}
		report.Results = append(report.Results, r)
	}
	report.State = "completed-raw-only"
	if report.Quarantined > 0 {
		report.State = "completed-with-quarantined-unavailable"
	}
	if report.HostStopped {
		report.State = "blocked-with-diagnostics"
	}
	if e := writeOnceJSON(filepath.Join(root, "report.json"), report); e != nil {
		return nil, e
	}
	return report, failure
}
