package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/dream-until-dawn/futures-tickflow-go/archive/batch"
)

const retainedParentSealSHA = "245145b664ebfc400526711ce40e791c694691e95b3bf252074b2b4dbf2e4f4f"

func retainedRemainingJobs(root string) ([]batch.Job, error) {
	jobs, err := directoryJobs("docs/evidence/history-capability-20261116/daily-observed-plan.json")
	if err != nil {
		return nil, err
	}
	seal, err := os.ReadFile("docs/evidence/history-capability-20261116/prior-acquisition-manifest.json")
	if err != nil {
		return nil, err
	}
	if err = verifyCzcePrior(root, seal, retainedParentSealSHA); err != nil {
		return nil, err
	}
	var master []chunkState
	raw, err := os.ReadFile(filepath.Join(root, "master-results.json"))
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(raw, &master); err != nil {
		return nil, err
	}
	var first []outcome
	raw, err = os.ReadFile(filepath.Join(root, "batch-001/results.json"))
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(raw, &first); err != nil {
		return nil, err
	}
	if err = validateRetainedStop(jobs, master, first); err != nil {
		return nil, err
	}
	job := jobs[82]
	body, err := os.ReadFile(filepath.Join(root, "archive", job.ID+".body"))
	if err != nil {
		return nil, err
	}
	raw, err = os.ReadFile(filepath.Join(root, "archive", job.ID+".json"))
	if err != nil {
		return nil, err
	}
	var receipt batch.Receipt
	if err = json.Unmarshal(raw, &receipt); err != nil {
		return nil, err
	}
	if !completeEmptyCSV(job, batch.Result{ID: job.ID, Receipt: receipt}, body) {
		return nil, fmt.Errorf("旧失败不是完整空表头")
	}
	return jobs[83:], nil
}

// 固定原83次停止位置；已成功、失败和未知项都不能混入新336项。
func validateRetainedStop(jobs []batch.Job, master []chunkState, first []outcome) error {
	if len(jobs) != 419 || jobs[82].ID != "JR2401-retained-history" || len(master) != 4 || len(first) != 128 {
		return fmt.Errorf("旧任务范围不符")
	}
	for i, row := range master {
		want := "not_run"
		n := 128
		if i == 0 {
			want = "failed"
		}
		if i == 3 {
			n = 35
		}
		if row.Name != fmt.Sprintf("batch-%03d", i+1) || row.Jobs != n || row.State != want {
			return fmt.Errorf("旧总检查点不符")
		}
	}
	for i, row := range first {
		want := "not_run"
		if i < 82 {
			want = "received"
		}
		if i == 82 {
			want = "failed"
		}
		if row.ID != jobs[i].ID || row.State != want {
			return fmt.Errorf("旧首批检查点不符")
		}
	}
	return nil
}
