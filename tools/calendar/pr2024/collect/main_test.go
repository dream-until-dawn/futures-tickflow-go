package main

import (
	"crypto/sha256"
	"encoding/hex"
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
