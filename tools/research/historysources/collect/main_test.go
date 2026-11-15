package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/dream-until-dawn/futures-tickflow-go/archive/batch"
	"github.com/dream-until-dawn/futures-tickflow-go/refdata/evidence"
)

func TestHistorySourcesHostFailureAndUnknownDoNotRetry(t *testing.T) {
	for _, mode := range []string{"success", "failed", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "capture")
			calls := []string{}
			err := collect(root, func(j batch.Job) (batch.Result, error) {
				calls = append(calls, j.ID)
				if j.ID == "pk-release" && mode == "unknown" {
					return batch.Result{}, errors.New("synthetic unknown launch")
				}
				if j.ID == "pk-release" && mode == "failed" {
					return batch.Result{Receipt: batch.Receipt{State: "failed"}}, nil
				}
				return batch.Result{Receipt: batch.Receipt{State: "received"}}, nil
			})
			if (err == nil) != (mode == "success") {
				t.Fatalf("失败未报告: %v", err)
			}
			if mode == "success" && len(calls) != 6 {
				t.Fatal("正控没有全部执行", calls)
			}
			if mode != "success" && (len(calls) != 4 || calls[1] != "czce-year2024-reprint") {
				t.Fatal("失败主机仍被请求或无关主机错误停止", calls)
			}
			if err = collect(root, func(batch.Job) (batch.Result, error) { t.Fatal("重发既有计划"); return batch.Result{}, nil }); err == nil {
				t.Fatal("既有目录未拒绝")
			}
		})
	}
}

func TestHistorySourcesFormatAndDigestAreBothRequired(t *testing.T) {
	for _, format := range []string{"pdf", "html"} {
		body := []byte("%PDF-1.7 synthetic")
		if format == "html" {
			body = []byte("<!doctype html><html>花生期货合约及业务规则 synthetic</html>")
		}
		sum := sha256.Sum256(body)
		ref := evidence.Ref{ID: "synthetic", URL: candidates()[0].URL, SHA256: hex.EncodeToString(sum[:]), RetrievedAt: time.Now().UTC()}
		j := batch.Job{ID: "pk-release", Format: format}
		if err := validate(j, body, ref); err != nil {
			t.Fatal("正控失败", err)
		}
		if err := validate(j, append(append([]byte{}, body...), 'x'), ref); err == nil {
			t.Fatal("篡改原件未拒绝")
		}
		bad := []byte("not a PDF or HTML")
		sum = sha256.Sum256(bad)
		ref.SHA256 = hex.EncodeToString(sum[:])
		if err := validate(j, bad, ref); err == nil {
			t.Fatal("重算摘要后的坏格式未拒绝")
		}
		if format == "html" {
			bad = []byte("<html>robot verification reload</html>")
			sum = sha256.Sum256(bad)
			ref.SHA256 = hex.EncodeToString(sum[:])
			if err := validate(j, bad, ref); err == nil {
				t.Fatal("200挑战正文未拒绝")
			}
		}
	}
}

func TestVendorAccessModeOnlyCollectsThreePublicDocuments(t *testing.T) {
	root := filepath.Join(t.TempDir(), "vendor-docs")
	calls := []string{}
	err := collectJobs(root, vendorAccessCandidates(), func(j batch.Job) (batch.Result, error) {
		calls = append(calls, j.URL)
		if j.Format != "html" || j.Version != "vendor-public-doc-unqualified" {
			t.Fatal("公开文档身份被改成市场数据或授权", j)
		}
		return batch.Result{Receipt: batch.Receipt{State: "received"}}, nil
	})
	if err != nil || len(calls) != 3 || calls[0] != "https://zhundata.cn/" || calls[1] != "https://zhundata.cn/hist-quotes-api.html" || calls[2] != "https://zhundata.cn/realtime-quotes-api.html" {
		t.Fatal("公开文档模式范围不符", calls, err)
	}
	// 同主机首项未知就停；不能靠后面的另一个文档继续探测它。
	calls = nil
	err = collectJobs(filepath.Join(t.TempDir(), "unknown"), vendorAccessCandidates(), func(j batch.Job) (batch.Result, error) {
		calls = append(calls, j.URL)
		return batch.Result{}, errors.New("synthetic unknown")
	})
	if err == nil || len(calls) != 1 {
		t.Fatal("同一供应商未知后仍继续", calls, err)
	}
}

func TestEDBDocumentPhaseDoesNotIncludeMarketOrToken(t *testing.T) {
	j := edbDocumentCandidate()
	calls := 0
	err := collectJobs(filepath.Join(t.TempDir(), "edb-doc"), []batch.Job{j}, func(job batch.Job) (batch.Result, error) {
		calls++
		if job.URL != "https://doc.shinnytech.com/edb/latest/md_server.html" || responseLimit(job) != 2<<20 {
			t.Fatal("EDB文档范围或预算被扩展", job)
		}
		return batch.Result{Receipt: batch.Receipt{State: "received"}}, nil
	})
	if err != nil || calls != 1 {
		t.Fatal(err, calls)
	}
}
