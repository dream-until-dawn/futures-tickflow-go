package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/dream-until-dawn/futures-tickflow-go/archive/batch"
)

const czceContinuationSHA = "deef876d72d5368cc7dbecb3df1f46d8a65b1d5633dc0cab28669d7ed298683a"
const czcePriorSealSHA = "507d60ecce19ec79598509c4bf73f7f2df339811cd9968e2e1ba13fc3a029a9e"
const czceContinuationMarker = "C:/Temp/tickflow-czce-unattempted-deef876d72d5368c-20261115.started.json"

// 所有可用或不可用判断都先绑定真实完整200、任务身份、原字节和已归档收据。
func completeBoundCSV(job batch.Job, result batch.Result, body []byte) bool {
	r := result.Receipt
	h := r.HTTP
	j, _ := json.Marshal(job)
	return result.ID == job.ID && !result.Resumed && r.BodyState == "complete" && len(body) <= 2<<20 &&
		r.JobSHA == digest(j) && r.Evidence.ID == job.ID &&
		r.Evidence.URL == job.URL && r.Evidence.Verify(body) == nil && r.Bytes == int64(len(body)) &&
		h != nil && h.Status == 200 && h.Complete && h.Attempted && h.ResponseReceived &&
		h.FinalURL == job.URL && h.Failure == ""
}

// 空表头只是不可用证据；账户错误、超时、部分正文及未知错误不能在此继续。
func completeEmptyCSV(job batch.Job, result batch.Result, body []byte) bool {
	header := "datetime_nano,open,high,low,close,volume,open_oi,close_oi"
	return completeBoundCSV(job, result, body) && result.Receipt.State == "failed" &&
		result.Receipt.Error == "CSV错误或没有样本行: <nil>" &&
		(bytes.Equal(body, []byte(header+"\r\n")) || bytes.Equal(body, []byte(header+"\n")))
}

func validateCzceRemaining(parent, remaining []batch.Job, first []outcome, master []chunkState) error {
	if len(parent) != 183 || len(remaining) != 164 || !reflect.DeepEqual(parent[19:], remaining) || len(first) != 128 ||
		len(master) != 2 || master[0].Name != "batch-001" || master[0].Jobs != 128 || master[0].State != "failed" ||
		master[1].Name != "batch-002" || master[1].Jobs != 55 || master[1].State != "not_run" {
		return fmt.Errorf("旧停止位置/未请求集合不符")
	}
	for i, row := range first {
		want := "not_run"
		if i < 18 {
			want = "received"
		} else if i == 18 {
			want = "failed"
		}
		if row.ID != parent[i].ID || row.State != want {
			return fmt.Errorf("旧首批状态不符")
		}
	}
	return nil
}

// 对照外部可信SHA的旧封存清单逐件检查，不能自行重写收据和SHA来伪造“未请求”。
func verifyCzcePrior(root string, raw []byte, expected string) error {
	if digest(raw) != expected {
		return fmt.Errorf("旧封存manifest SHA不符")
	}
	var manifest struct {
		Files []struct {
			Path  string `json:"path"`
			Bytes int64  `json:"bytes"`
			SHA   string `json:"sha256"`
		} `json:"files"`
	}
	if e := json.Unmarshal(raw, &manifest); e != nil {
		return e
	}
	files := map[string]bool{}
	for _, row := range manifest.Files {
		if !strings.HasPrefix(row.Path, "acquisition/") {
			continue
		}
		rel := strings.TrimPrefix(row.Path, "acquisition/")
		if files[rel] || strings.Contains("/"+rel+"/", "/../") || filepath.IsAbs(rel) {
			return fmt.Errorf("旧清单路径不符")
		}
		files[rel] = true
		b, e := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if e != nil || int64(len(b)) != row.Bytes || digest(b) != row.SHA {
			return fmt.Errorf("旧任务原件SHA不符: %s", rel)
		}
	}
	if len(files) == 0 {
		return fmt.Errorf("旧清单没有采集原件")
	}
	return filepath.WalkDir(root, func(path string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("旧目录含符号链接")
		}
		if d.IsDir() {
			return nil
		}
		rel, e := filepath.Rel(root, path)
		if e != nil {
			return e
		}
		if !files[filepath.ToSlash(rel)] {
			return fmt.Errorf("旧目录出现未知文件")
		}
		return nil
	})
}

func czceRemainingJobs(planFile, priorRoot string) ([]batch.Job, error) {
	raw, e := os.ReadFile(planFile)
	if e != nil {
		return nil, e
	}
	if digest(raw) != czceContinuationSHA {
		return nil, fmt.Errorf("164项候选SHA不符")
	}
	var p struct {
		Jobs      []batch.Job `json:"jobs"`
		ParentSHA string      `json:"parent_plan_sha256"`
		MasterSHA string      `json:"parent_master_results_sha256"`
		FirstSHA  string      `json:"parent_first_batch_results_sha256"`
	}
	if e = json.Unmarshal(raw, &p); e != nil {
		return nil, e
	}
	if p.ParentSHA != czceObservedPlanSHA {
		return nil, fmt.Errorf("父计划不符")
	}
	parent, e := directoryJobs("docs/evidence/historical-contract-directory-20261115/daily-czce-observed-plan.json")
	if e != nil {
		return nil, e
	}
	manifest, e := os.ReadFile("C:/Temp/tickflow-czce-observed-partial-20261115-v1-sealed/manifest.json")
	if e != nil {
		return nil, e
	}
	if e = verifyCzcePrior(priorRoot, manifest, czcePriorSealSHA); e != nil {
		return nil, e
	}
	load := func(name, expected string, target any) error {
		b, e := os.ReadFile(filepath.Join(priorRoot, name))
		if e != nil {
			return e
		}
		if digest(b) != expected {
			return fmt.Errorf("旧检查点SHA不符")
		}
		return json.Unmarshal(b, target)
	}
	var first []outcome
	var master []chunkState
	if e = load("batch-001/results.json", p.FirstSHA, &first); e != nil {
		return nil, e
	}
	if e = load("master-results.json", p.MasterSHA, &master); e != nil {
		return nil, e
	}
	if e = validateCzceRemaining(parent, p.Jobs, first, master); e != nil {
		return nil, e
	}
	job := parent[18]
	b, e := os.ReadFile(filepath.Join(priorRoot, "archive", job.ID+".body"))
	if e != nil {
		return nil, e
	}
	r, e := os.ReadFile(filepath.Join(priorRoot, "archive", job.ID+".json"))
	if e != nil {
		return nil, e
	}
	var receipt batch.Receipt
	if e = json.Unmarshal(r, &receipt); e != nil {
		return nil, e
	}
	if !completeEmptyCSV(job, batch.Result{ID: job.ID, Receipt: receipt}, b) {
		return nil, fmt.Errorf("旧失败不是完整空表头")
	}
	return p.Jobs, nil
}

type isolationReport struct {
	State             string    `json:"state"`
	Received          int       `json:"received"`
	Unavailable       int       `json:"unavailable_complete_empty"`
	Failed            int       `json:"failed"`
	NotRun            int       `json:"not_run"`
	FinancialEligible bool      `json:"financial_eligible"`
	Results           []outcome `json:"results"`
}

func collectCzceIsolated(root string, jobs []batch.Job, fetch func(batch.Job) (batch.Result, error)) error {
	return collectEmptyIsolated(root, jobs, 164, czceContinuationSHA, fetch)
}

func collectEmptyIsolated(root string, jobs []batch.Job, limit int, candidate string, fetch func(batch.Job) (batch.Result, error)) error {
	if len(jobs) == 0 || len(jobs) > limit {
		return fmt.Errorf("隔离续取数量越界")
	}
	if e := os.Mkdir(root, 0700); e != nil {
		return fmt.Errorf("续取已有/未知目录禁止重发: %w", e)
	}
	plan, _ := json.MarshalIndent(map[string]any{"jobs": jobs, "candidate_sha256": candidate, "requests_max": len(jobs), "bytes_per_response_max": 2 << 20, "timeout_seconds": 15, "interval_after_completion_seconds": 2, "empty_not_coverage": true, "financial_eligible": false}, "", "  ")
	if e := os.WriteFile(filepath.Join(root, "plan.json"), append(plan, '\n'), 0600); e != nil {
		return e
	}
	report := isolationReport{State: "running", NotRun: len(jobs)}
	for _, j := range jobs {
		report.Results = append(report.Results, outcome{ID: j.ID, State: "not_run"})
	}
	save := func() error {
		b, e := json.MarshalIndent(report, "", "  ")
		if e != nil {
			return e
		}
		return os.WriteFile(filepath.Join(root, "report.json"), append(b, '\n'), 0600)
	}
	if e := save(); e != nil {
		return e
	}
	for i, j := range jobs {
		report.Results[i].State = "running"
		report.NotRun--
		if e := save(); e != nil {
			return e
		}
		r, e := fetch(j)
		body, readErr := os.ReadFile(filepath.Join(root, "archive", j.ID+".body"))
		switch {
		case e == nil && readErr == nil && completeBoundCSV(j, r, body) && r.Receipt.State == "received" && r.Receipt.Error == "" && validateCSV(j, body, r.Receipt.Evidence) == nil:
			report.Results[i].State = "received"
			report.Received++
		case e == nil && readErr == nil && completeEmptyCSV(j, r, body):
			report.Results[i].State = "unavailable-complete-empty"
			report.Results[i].Error = r.Receipt.Error
			report.Unavailable++
		default:
			report.Results[i].State = "failed"
			report.Results[i].Error = r.Receipt.Error
			if e != nil {
				report.Results[i].Error = e.Error()
			} else if readErr != nil {
				report.Results[i].Error = readErr.Error()
			}
			report.Failed++
			report.State = "stopped-on-failure"
			if e := save(); e != nil {
				return e
			}
			return fmt.Errorf("非完整空表头失败，全部后续停止")
		}
		if e := save(); e != nil {
			return e
		}
	}
	report.State = "completed"
	if report.Unavailable > 0 {
		report.State = "completed-with-unavailable"
	}
	return save()
}
