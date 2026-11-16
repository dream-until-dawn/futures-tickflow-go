package main

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/dream-until-dawn/futures-tickflow-go/archive/batch"
)

func TestRetainedStopOnlyAllowsNeverRequestedTail(t *testing.T) {
	jobs := make([]batch.Job, 419)
	first := make([]outcome, 128)
	for i := range jobs {
		jobs[i].ID = fmt.Sprintf("case-%d", i)
	}
	jobs[82].ID = "JR2401-retained-history"
	for i := range first {
		state := "not_run"
		if i < 82 {
			state = "received"
		}
		if i == 82 {
			state = "failed"
		}
		first[i] = outcome{ID: jobs[i].ID, State: state}
	}
	master := []chunkState{{Name: "batch-001", Jobs: 128, State: "failed"}, {Name: "batch-002", Jobs: 128, State: "not_run"}, {Name: "batch-003", Jobs: 128, State: "not_run"}, {Name: "batch-004", Jobs: 35, State: "not_run"}}
	if e := validateRetainedStop(jobs, master, first); e != nil {
		t.Fatal(e)
	}
	first[83].State = "received"
	if e := validateRetainedStop(jobs, master, first); e == nil {
		t.Fatal("已尝试尾项未拒绝")
	}
	first[83].State = "not_run"
	master[1].State = "running"
	if e := validateRetainedStop(jobs, master, first); e == nil {
		t.Fatal("未知启动未拒绝")
	}
	calls := 0
	e := collectEmptyIsolated(filepath.Join(t.TempDir(), "out"), make([]batch.Job, 337), 336, retainedHistoryPlanSHA, func(j batch.Job) (batch.Result, error) { calls++; return batch.Result{}, nil })
	if e == nil || calls != 0 {
		t.Fatal("超预算不应请求")
	}
}
