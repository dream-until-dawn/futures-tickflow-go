package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dream-until-dawn/futures-tickflow-go/archive/batch"
	"github.com/dream-until-dawn/futures-tickflow-go/refdata/evidence"
)

func TestEDBSampleFailureAndPriorRootDoNotRetry(t *testing.T) {
	for _, failure := range []bool{false, true} {
		root := filepath.Join(t.TempDir(), "capture")
		calls := 0
		err := collect(root, func(batch.Job) (batch.Result, error) {
			calls++
			state := "received"
			if failure {
				state = "failed"
			}
			return batch.Result{Receipt: batch.Receipt{State: state}}, nil
		})
		want := 4
		if failure {
			want = 1
		}
		if calls != want || (err != nil) != failure {
			t.Fatal("停止/正控错误", calls, err)
		}
		if err = collect(root, func(batch.Job) (batch.Result, error) { t.Fatal("重发旧目录"); return batch.Result{}, nil }); err == nil {
			t.Fatal("既有目录未拒绝")
		}
		if failure {
			raw, _ := os.ReadFile(filepath.Join(root, "results.json"))
			var results []outcome
			json.Unmarshal(raw, &results)
			if len(results) != 4 || results[1].State != "not_run" {
				t.Fatal("未请求伪装成功", results)
			}
		}
	}
}

func TestEDBCSVRejectsErrorEmptyAndBadInteger(t *testing.T) {
	header := "datetime_nano,open,high,low,close,volume,open_oi,close_oi\n"
	for _, example := range []struct {
		body  string
		valid bool
	}{{header + "1763427600000000000,1,2,1,2,3,4,5\n", true}, {header, false}, {"403,subscription required", false}, {header + "1763427600000000000,1,2,1,2,0.5,4,5\n", false}, {header + "1763427600000000000,NaN,2,1,2,3,4,5\n", false}} {
		body := []byte(example.body)
		ref := evidence.Ref{ID: "synthetic", URL: docURL, SHA256: digest(body), RetrievedAt: time.Now().UTC()}
		if err := validateCSV(batch.Job{}, body, ref); (err == nil) != example.valid {
			t.Fatal("CSV正反控制错误", example, err)
		}
	}
}

func TestEDBDataRequiresLockedCompleteDocument(t *testing.T) {
	body := []byte("synthetic public document")
	baseline := []byte("synthetic independently reviewed baseline")
	r := batch.Receipt{State: "received", BodyState: "complete", Bytes: int64(len(body)), Evidence: evidence.Ref{ID: "synthetic", URL: docURL, SHA256: digest(body), RetrievedAt: time.Now().UTC()}, HTTP: &batch.Download{Status: 200, Attempted: true, ResponseReceived: true, Complete: true, FinalURL: docURL}}
	if err := checkEvidence(body, r, baseline, digest(body), digest(baseline)); err != nil {
		t.Fatal(err)
	}
	r.BodyState = "partial"
	if err := checkEvidence(body, r, baseline, digest(body), digest(baseline)); err == nil {
		t.Fatal("不完整文档放行取数")
	}
	r.BodyState = "complete"
	if err := checkEvidence(append(body, 'x'), r, baseline, digest(body), digest(baseline)); err == nil {
		t.Fatal("换文档放行取数")
	}
}

func TestEDBContinuationOnlyAcceptsCompleteEmptyAndReservesOnce(t *testing.T) {
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, "archive"), 0700)
	write := func(name string, value any) {
		raw, _ := json.Marshal(value)
		if err := os.WriteFile(filepath.Join(root, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("plan.json", map[string]any{"jobs": jobs()})
	results := make([]outcome, 4)
	for i, j := range jobs() {
		state := "not_run"
		if i == 0 {
			state = "failed"
		}
		results[i] = outcome{ID: j.ID, State: state}
	}
	write("results.json", results)
	body := []byte("datetime_nano,open,high,low,close,volume,open_oi,close_oi\r\n")
	os.WriteFile(filepath.Join(root, "archive/ec2602-86400.body"), body, 0600)
	jobRaw, _ := json.Marshal(jobs()[0])
	receipt := batch.Receipt{JobSHA: digest(jobRaw), State: "failed", BodyState: "complete", Bytes: int64(len(body)), Evidence: evidence.Ref{ID: jobs()[0].ID, URL: jobs()[0].URL, SHA256: digest(body), RetrievedAt: time.Now().UTC()}, HTTP: &batch.Download{Status: 200, Attempted: true, ResponseReceived: true, Complete: true, FinalURL: jobs()[0].URL}}
	for _, status := range []int{200, 403, 412, 500, 0} {
		receipt.HTTP.Status = status
		write("archive/ec2602-86400.json", receipt)
		if err := continuationEmpty(root); (err == nil) != (status == 200) {
			t.Fatal("完整空资源/权限门禁失效", status, err)
		}
	}
	receipt.HTTP.Status = 200
	receipt.BodyState = "partial"
	write("archive/ec2602-86400.json", receipt)
	if err := continuationEmpty(root); err == nil {
		t.Fatal("截断原件允许后续")
	}
	receipt.BodyState = "complete"
	write("archive/ec2602-86400.json", receipt)
	target := filepath.Join(t.TempDir(), "new")
	if err := reserveContinuation(root, target); err != nil {
		t.Fatal(err)
	}
	if err := continuationEmpty(root); err == nil {
		t.Fatal("持久旧启动被重置")
	}
	if err := reserveContinuation(root, filepath.Join(t.TempDir(), "other")); err == nil {
		t.Fatal("换目录重启后续")
	}
}
