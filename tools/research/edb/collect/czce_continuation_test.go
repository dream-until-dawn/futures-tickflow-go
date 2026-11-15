package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dream-until-dawn/futures-tickflow-go/archive/batch"
	"github.com/dream-until-dawn/futures-tickflow-go/refdata/evidence"
)

const emptyFixture = "datetime_nano,open,high,low,close,volume,open_oi,close_oi\r\n"

func isolationFixture(job batch.Job, body []byte, empty bool) batch.Result {
	j, _ := json.Marshal(job)
	r := batch.Result{ID: job.ID, Receipt: batch.Receipt{JobSHA: digest(j), State: "received", BodyState: "complete", Bytes: int64(len(body)),
		Evidence: evidence.Ref{ID: job.ID, URL: job.URL, SHA256: digest(body), RetrievedAt: time.Now().UTC()},
		HTTP:     &batch.Download{Status: 200, Attempted: true, ResponseReceived: true, Complete: true, FinalURL: job.URL}}}
	if empty {
		r.Receipt.State = "failed"
		r.Receipt.Error = "CSV错误或没有样本行: <nil>"
	}
	return r
}

func TestCzceEmptyIsolationRejectsPartialErrorAndReboundReceipt(t *testing.T) {
	j := batch.Job{ID: "fixture", URL: "https://edb.shinnytech.com/md/kline?symbol=CZCE.CY103"}
	for _, body := range [][]byte{[]byte(emptyFixture), []byte("datetime_nano,open,high,low,close,volume,open_oi,close_oi\n")} {
		r := isolationFixture(j, body, true)
		if !completeEmptyCSV(j, r, body) {
			t.Fatal("完整空表头正控失败")
		}
	}
	mutations := []func(*batch.Result){
		func(r *batch.Result) { r.Receipt.HTTP.Status = 403 }, func(r *batch.Result) { r.Receipt.BodyState = "partial" },
		func(r *batch.Result) { r.Receipt.HTTP.Complete = false }, func(r *batch.Result) { r.Receipt.HTTP.Attempted = false },
		func(r *batch.Result) { r.Receipt.HTTP.ResponseReceived = false }, func(r *batch.Result) { r.Receipt.HTTP.Failure = "timeout" },
		func(r *batch.Result) { r.Receipt.State = "received" }, func(r *batch.Result) { r.Receipt.JobSHA = "wrong" },
		func(r *batch.Result) { r.Receipt.Evidence.URL += "other" }, func(r *batch.Result) { r.Receipt.Bytes++ },
		func(r *batch.Result) { r.Receipt.HTTP.FinalURL += "other" }, func(r *batch.Result) { r.Resumed = true },
		func(r *batch.Result) { r.Receipt.Error = "unknown" },
	}
	for _, mutate := range mutations {
		r := isolationFixture(j, []byte(emptyFixture), true)
		mutate(&r)
		if completeEmptyCSV(j, r, []byte(emptyFixture)) {
			t.Fatal("错误/截断/重绑定被当空数据隔离")
		}
	}
	for _, body := range [][]byte{[]byte("403 subscription required"), []byte(emptyFixture + "\r\n"), []byte(emptyFixture + "1\n")} {
		r := isolationFixture(j, body, true)
		if completeEmptyCSV(j, r, body) {
			t.Fatal("非精确空表头绕过失败停止")
		}
	}
}

func TestCzceIsolationContinuesEmptyButStopsOtherFailure(t *testing.T) {
	for _, bad := range []bool{false, true} {
		root := filepath.Join(t.TempDir(), "capture")
		jobs := []batch.Job{}
		for i := 0; i < 3; i++ {
			jobs = append(jobs, batch.Job{ID: fmt.Sprint(i), URL: "https://edb.shinnytech.com/" + fmt.Sprint(i)})
		}
		calls := 0
		var emptyReceipt []byte
		err := collectCzceIsolated(root, jobs, func(j batch.Job) (batch.Result, error) {
			calls++
			body := []byte(emptyFixture)
			empty := j.ID == "0"
			if !empty {
				body = append(body, []byte("1763427600000000000,1,2,1,2,3,4,5\r\n")...)
			}
			r := isolationFixture(j, body, empty)
			if bad && j.ID == "1" {
				r.Receipt.State = "failed"
				r.Receipt.Error = "forbidden"
				r.Receipt.HTTP.Status = 403
			}
			if e := os.MkdirAll(filepath.Join(root, "archive"), 0700); e != nil {
				t.Fatal(e)
			}
			if e := os.WriteFile(filepath.Join(root, "archive", j.ID+".body"), body, 0600); e != nil {
				t.Fatal(e)
			}
			encoded, _ := json.Marshal(r.Receipt)
			if e := os.WriteFile(filepath.Join(root, "archive", j.ID+".json"), encoded, 0600); e != nil {
				t.Fatal(e)
			}
			if empty {
				emptyReceipt = encoded
			}
			return r, nil
		})
		want := 3
		if bad {
			want = 2
		}
		if calls != want || (err != nil) != bad {
			t.Fatal("续取或停止计数错误", calls, err)
		}
		b, _ := os.ReadFile(filepath.Join(root, "report.json"))
		var report isolationReport
		if e := json.Unmarshal(b, &report); e != nil {
			t.Fatal(e)
		}
		if report.Unavailable != 1 || report.FinancialEligible {
			t.Fatal("隔离被计成覆盖")
		}
		if bad {
			if report.Failed != 1 || report.NotRun != 1 || report.Results[2].State != "not_run" {
				t.Fatal(report)
			}
		} else if report.Received != 2 || report.State != "completed-with-unavailable" {
			t.Fatal(report)
		}
		b, _ = os.ReadFile(filepath.Join(root, "archive", "0.json"))
		if string(b) != string(emptyReceipt) {
			t.Fatal("原始失败收据被改写")
		}
		if e := collectCzceIsolated(root, jobs, func(batch.Job) (batch.Result, error) { t.Fatal("旧目录重发"); return batch.Result{}, nil }); e == nil {
			t.Fatal("旧目录未拒绝")
		}
	}
}

func TestCzceRemainingSetCannotIncludeAttemptedOrUnknownState(t *testing.T) {
	parent := []batch.Job{}
	for i := 0; i < 183; i++ {
		parent = append(parent, batch.Job{ID: fmt.Sprint(i)})
	}
	first := []outcome{}
	for i := 0; i < 128; i++ {
		state := "not_run"
		if i < 18 {
			state = "received"
		} else if i == 18 {
			state = "failed"
		}
		first = append(first, outcome{ID: parent[i].ID, State: state})
	}
	master := []chunkState{{Name: "batch-001", Jobs: 128, State: "failed"}, {Name: "batch-002", Jobs: 55, State: "not_run"}}
	remaining := append([]batch.Job(nil), parent[19:]...)
	if e := validateCzceRemaining(parent, remaining, first, master); e != nil {
		t.Fatal(e)
	}
	remaining[0] = parent[18]
	if validateCzceRemaining(parent, remaining, first, master) == nil {
		t.Fatal("重取失败URL")
	}
	remaining[0] = parent[19]
	first[19].State = "running"
	if validateCzceRemaining(parent, remaining, first, master) == nil {
		t.Fatal("未知启动被续取")
	}
	first[19].State = "not_run"
	master[1].State = "running"
	if validateCzceRemaining(parent, remaining, first, master) == nil {
		t.Fatal("后批未知启动被忽略")
	}
}

func TestCzcePriorFilesNeedExternalManifestAndExactSet(t *testing.T) {
	root := t.TempDir()
	body := []byte("prior")
	file := filepath.Join(root, "result.json")
	if e := os.WriteFile(file, body, 0600); e != nil {
		t.Fatal(e)
	}
	manifest, _ := json.Marshal(map[string]any{"files": []map[string]any{{"path": "acquisition/result.json", "bytes": len(body), "sha256": digest(body)}}})
	if e := verifyCzcePrior(root, manifest, digest(manifest)); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(file, []byte("altered"), 0600); e != nil {
		t.Fatal(e)
	}
	if verifyCzcePrior(root, manifest, digest(manifest)) == nil {
		t.Fatal("旧原件篡改未拦截")
	}
	if e := os.WriteFile(file, body, 0600); e != nil {
		t.Fatal(e)
	}
	if verifyCzcePrior(root, append(manifest, ' '), digest(manifest)) == nil {
		t.Fatal("新manifest冒充外部可信SHA")
	}
	if e := os.WriteFile(filepath.Join(root, "unknown.json"), body, 0600); e != nil {
		t.Fatal(e)
	}
	if verifyCzcePrior(root, manifest, digest(manifest)) == nil {
		t.Fatal("未知旧启动文件被忽略")
	}
}
