package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dream-until-dawn/futures-tickflow-go/archive/batch"
	"github.com/dream-until-dawn/futures-tickflow-go/refdata/evidence"
)

func TestUnobservedCSVRejectsOutsideFrozenWindow(t *testing.T) {
	header := "datetime_nano,open,high,low,close,volume,open_oi,close_oi\n"
	for _, sample := range []struct {
		day   string
		valid bool
	}{{"2021-01-04", true}, {"2026-11-13", true}, {"2020-12-31", false}, {"2026-11-16", false}} {
		day, err := time.Parse("2006-01-02", sample.day)
		if err != nil {
			t.Fatal(err)
		}
		body := []byte(header + fmt.Sprintf("%d,1,2,1,2,3,4,5\n", day.UnixNano()))
		ref := evidence.Ref{ID: "synthetic", URL: docURL, SHA256: digest(body), RetrievedAt: time.Now().UTC()}
		err = validateCSV(batch.Job{ID: "a2201-unobserved-research"}, body, ref)
		if sample.valid && err != nil {
			t.Fatal(err)
		}
		if !sample.valid && (err == nil || !strings.Contains(err.Error(), "冻结查询窗口")) {
			t.Fatalf("窗口越界未被目标门禁拒绝: %s %v", sample.day, err)
		}
	}
}

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
	if err := reserveContinuation(root, target, jobs()[1:]); err != nil {
		t.Fatal(err)
	}
	if err := continuationEmpty(root); err == nil {
		t.Fatal("持久旧启动被重置")
	}
	if err := reserveContinuation(root, filepath.Join(t.TempDir(), "other"), jobs()[1:]); err == nil {
		t.Fatal("换目录重启后续")
	}
}

func TestEDBFinalMinuteRequiresLockedSuccessAndKnownEmpty(t *testing.T) {
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, "archive"), 0700)
	write := func(name string, value any) {
		raw, _ := json.Marshal(value)
		if e := os.WriteFile(filepath.Join(root, name), raw, 0600); e != nil {
			t.Fatal(e)
		}
	}
	write("plan.json", map[string]any{"jobs": jobs()[1:]})
	results := []outcome{{ID: jobs()[1].ID, State: "received"}, {ID: jobs()[2].ID, State: "failed"}, {ID: jobs()[3].ID, State: "not_run"}}
	write("results.json", results)
	bodies := [][]byte{[]byte("datetime_nano,open,high,low,close,volume,open_oi,close_oi\n1763427600000000000,1,2,1,2,3,4,5\n"), []byte("datetime_nano,open,high,low,close,volume,open_oi,close_oi\r\n")}
	var last batch.Receipt
	for i, j := range jobs()[1:3] {
		body := bodies[i]
		os.WriteFile(filepath.Join(root, "archive", j.ID+".body"), body, 0600)
		jobRaw, _ := json.Marshal(j)
		r := batch.Receipt{JobSHA: digest(jobRaw), State: results[i].State, BodyState: "complete", Bytes: int64(len(body)), Evidence: evidence.Ref{ID: j.ID, URL: j.URL, SHA256: digest(body), RetrievedAt: time.Now().UTC()}, HTTP: &batch.Download{Status: 200, Attempted: true, ResponseReceived: true, Complete: true, FinalURL: j.URL}}
		write("archive/"+j.ID+".json", r)
		last = r
	}
	if e := continuationLastBRWithEC(root, digest(bodies[0])); e != nil {
		t.Fatal("正控失败", e)
	}
	if e := continuationLastBRWithEC(root, digest([]byte("wrong"))); e == nil {
		t.Fatal("换成功原件后仍允许续取")
	}
	last.HTTP.Status = 403
	write("archive/br2601-86400.json", last)
	if e := continuationLastBRWithEC(root, digest(bodies[0])); e == nil {
		t.Fatal("权限错误允许续取")
	}
	last.HTTP.Status = 200
	write("archive/br2601-86400.json", last)
	results[2].State = "received"
	write("results.json", results)
	if e := continuationLastBRWithEC(root, digest(bodies[0])); e == nil {
		t.Fatal("已请求最后一项允许重复")
	}
}

func TestEDBDailyAnchorExperimentIsOneDifferentPublicQuery(t *testing.T) {
	j := dailyAnchorJob()
	if j.URL == jobs()[0].URL || j.Exchange != "INE" || j.Product != "ec" || j.Format != "edb-csv" {
		t.Fatal("实验身份错误", j)
	}
	calls := 0
	err := collectSelected(filepath.Join(t.TempDir(), "anchor"), []batch.Job{j}, func(job batch.Job) (batch.Result, error) {
		calls++
		if job.URL != j.URL {
			t.Fatal("超出唯一URL")
		}
		return batch.Result{Receipt: batch.Receipt{State: "received"}}, nil
	})
	if err != nil || calls != 1 {
		t.Fatal(err, calls)
	}
}

func TestEDBDailyResearchPilotUsesOnlyPriorObservedCodes(t *testing.T) {
	planned := dailyResearchJobs()
	if len(planned) != 3 {
		t.Fatal("超出三项")
	}
	for _, j := range planned {
		if j.Format != "edb-csv" || j.Day != 0 || (j.Exchange != "DCE" && j.Exchange != "GFEX") {
			t.Fatal("研究身份冒充官方合约日", j)
		}
	}
	if err := requirePriorResearchFiles(t.TempDir()); err == nil {
		t.Fatal("缺旧原件仍放行")
	}
	if err := collectSelected(filepath.Join(t.TempDir(), "daily-only"), planned, func(j batch.Job) (batch.Result, error) {
		return batch.Result{Receipt: batch.Receipt{State: "received"}}, nil
	}); err != nil {
		t.Fatal(err)
	}
}
