package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/dream-until-dawn/futures-tickflow-go/archive/batch"
)

type chunkState struct {
	Name  string `json:"name"`
	Jobs  int    `json:"jobs"`
	State string `json:"state"`
	Error string `json:"error,omitempty"`
}

// 分批仅拆执行与检查点；任一批失败，全部后续保持not_run，绝不自动恢复旧任务。
func collectChunks(root string, jobs []batch.Job, size int, fetch func(batch.Job) (batch.Result, error)) error {
	if size <= 0 || size > 128 || len(jobs) == 0 {
		return fmt.Errorf("分批边界非法")
	}
	if err := os.Mkdir(root, 0700); err != nil {
		return fmt.Errorf("既有/未知主任务禁止重发: %w", err)
	}
	// 实际冻结SHA在目录单标记中记录；通用分批器不能冒称始终执行SHFE/INE旧单。
	planned, err := json.MarshalIndent(map[string]any{"jobs": jobs, "batch_size_max": size, "requests_max": len(jobs), "stop_all_on_first_failure": true}, "", "  ")
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(root, "master-plan.json"), append(planned, '\n'), 0600); err != nil {
		return err
	}
	states := []chunkState{}
	for start := 0; start < len(jobs); start += size {
		end := min(start+size, len(jobs))
		states = append(states, chunkState{Name: fmt.Sprintf("batch-%03d", len(states)+1), Jobs: end - start, State: "not_run"})
	}
	checkpoint := func() error {
		b, e := json.MarshalIndent(states, "", "  ")
		if e != nil {
			return e
		}
		return os.WriteFile(filepath.Join(root, "master-results.json"), append(b, '\n'), 0600)
	}
	if err = checkpoint(); err != nil {
		return err
	}
	for index := range states {
		states[index].State = "running"
		if err = checkpoint(); err != nil {
			return err
		}
		start := index * size
		end := min(start+size, len(jobs))
		err = collectSelected(filepath.Join(root, states[index].Name), jobs[start:end], fetch)
		if err != nil {
			states[index].State, states[index].Error = "failed", err.Error()
			if e := checkpoint(); e != nil {
				return e
			}
			return err
		}
		states[index].State = "completed"
		if err = checkpoint(); err != nil {
			return err
		}
	}
	return nil
}
