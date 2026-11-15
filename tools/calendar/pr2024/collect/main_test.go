package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dream-until-dawn/futures-tickflow-go/archive/batch"
	"github.com/dream-until-dawn/futures-tickflow-go/refdata/evidence"
)

func TestPRCandidateFailureStopsBeforeLaterURL(t *testing.T) {
	for _, mode := range []string{"failed", "error"} {
		root := filepath.Join(t.TempDir(), "capture")
		calls := 0
		err := collect(root, func(batch.Job) (batch.Result, error) {
			calls++
			if mode == "error" {
				return batch.Result{}, errors.New("synthetic failure")
			}
			return batch.Result{Receipt: batch.Receipt{State: "failed"}}, nil
		})
		if err == nil || calls != 1 {
			t.Fatal("失败未停止", err, calls)
		}
		if err = collect(root, func(batch.Job) (batch.Result, error) { t.Fatal("既有目录重取"); return batch.Result{}, nil }); err == nil {
			t.Fatal("既有目录未拒绝")
		}
	}
	root := filepath.Join(t.TempDir(), "positive")
	calls := 0
	if err := collect(root, func(job batch.Job) (batch.Result, error) {
		calls++
		return batch.Result{ID: job.ID, Receipt: batch.Receipt{State: "received"}}, nil
	}); err != nil || calls != 2 {
		t.Fatal(err, calls)
	}
	if _, err := os.Stat(filepath.Join(root, "plan.json")); err != nil {
		t.Fatal(err)
	}
}

func TestPRContinuationRequiresCompleteResource404(t *testing.T) {
	for _, status := range []int{404, 403, 412, 429, 0} {
		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, "archive"), 0700); err != nil {
			t.Fatal(err)
		}
		body := []byte("synthetic unavailable resource")
		sum := sha256.Sum256(body)
		r := batch.Receipt{State: "failed", BodyState: "complete", Bytes: int64(len(body)), Evidence: evidence.Ref{ID: "synthetic", URL: candidates()[0].URL, SHA256: hex.EncodeToString(sum[:]), RetrievedAt: time.Now().UTC()}, HTTP: &batch.Download{Status: status, Complete: true, Attempted: true, ResponseReceived: true, Failure: "http_status", FinalURL: candidates()[0].URL}}
		raw, _ := json.Marshal(r)
		os.WriteFile(filepath.Join(root, "archive/pr-first-rules.json"), raw, 0600)
		os.WriteFile(filepath.Join(root, "archive/pr-first-rules.body"), body, 0600)
		if err := continuation404(root); (err == nil) != (status == 404) {
			t.Fatal("404/挑战门禁错误", status, err)
		}
		if status == 404 {
			os.WriteFile(filepath.Join(root, "archive/pr-first-rules.body"), []byte("changed"), 0600)
			if err := continuation404(root); err == nil {
				t.Fatal("坏404原件通过")
			}
		}
	}
	calls := 0
	root := filepath.Join(t.TempDir(), "separate-batch")
	if err := collectSelected(root, candidates()[1:], func(job batch.Job) (batch.Result, error) {
		calls++
		if job.ID != "year-end-2025-candidate" {
			t.Fatal("重复旧URL")
		}
		return batch.Result{Receipt: batch.Receipt{State: "received"}}, nil
	}); err != nil || calls != 1 {
		t.Fatal(err, calls)
	}
}

func TestPRCandidateHTMLCannotMasqueradeAsPDF(t *testing.T) {
	for _, data := range [][]byte{[]byte("%PDF-1.7 fixture"), []byte("<html>challenge</html>")} {
		sum := sha256.Sum256(data)
		ref := evidence.Ref{ID: "synthetic", URL: candidates()[0].URL, SHA256: hex.EncodeToString(sum[:]), RetrievedAt: time.Now().UTC()}
		err := validate(candidates()[0], data, ref)
		if (err == nil) != (data[0] == '%') {
			t.Fatal("PDF格式反例失效", err)
		}
		ref.SHA256 = "bad"
		if err := validate(candidates()[0], data, ref); err == nil {
			t.Fatal("坏SHA竟通过")
		}
	}
}
