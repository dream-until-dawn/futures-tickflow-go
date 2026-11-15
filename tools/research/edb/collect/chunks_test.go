package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/dream-until-dawn/futures-tickflow-go/archive/batch"
)

func TestEDBChunksStopAllAndPriorRootCannotRestart(t *testing.T) {
	for _, fail := range []bool{false, true} {
		root := filepath.Join(t.TempDir(), "new")
		jobs := []batch.Job{{ID: "1"}, {ID: "2"}, {ID: "3"}, {ID: "4"}, {ID: "5"}}
		calls := 0
		fetch := func(j batch.Job) (batch.Result, error) {
			calls++
			state := "received"
			if fail && calls == 3 {
				state = "failed"
			}
			return batch.Result{ID: j.ID, Receipt: batch.Receipt{State: state}}, nil
		}
		err := collectChunks(root, jobs, 2, fetch)
		want := 5
		if fail {
			want = 3
		}
		if (err != nil) != fail || calls != want {
			t.Fatal("请求账/失败状态不符", err, calls)
		}
		b, e := os.ReadFile(filepath.Join(root, "master-results.json"))
		if e != nil {
			t.Fatal(e)
		}
		var states []chunkState
		if e = json.Unmarshal(b, &states); e != nil {
			t.Fatal(e)
		}
		if len(states) != 3 || states[0].State != "completed" {
			t.Fatal(states)
		}
		if fail && (states[1].State != "failed" || states[2].State != "not_run") {
			t.Fatal("失败后其他批被推进", states)
		}
		if !fail && states[2].State != "completed" {
			t.Fatal(states)
		}
		if e = collectChunks(root, jobs, 2, fetch); e == nil || calls != want {
			t.Fatal("主目录旧启动被重试", e, calls)
		}
	}
}
