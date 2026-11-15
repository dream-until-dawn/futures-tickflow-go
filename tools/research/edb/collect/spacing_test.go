package main

import (
	"testing"
	"time"

	"github.com/dream-until-dawn/futures-tickflow-go/archive/batch"
)

func TestEDBSpacingStartsAfterSlowPreviousRequestCompletes(t *testing.T) {
	const interval = 20 * time.Millisecond
	var priorEnd time.Time
	calls := 0
	fetch := completionPacedFetch(interval, func(batch.Job) (batch.Result, error) {
		if calls > 0 && time.Since(priorEnd) < interval {
			t.Fatal("上次慢请求已花尽起点间隔，下一请求仍须从完成点等待")
		}
		calls++
		time.Sleep(2 * interval)
		priorEnd = time.Now()
		return batch.Result{}, nil
	})
	for i := 0; i < 3; i++ {
		if _, e := fetch(batch.Job{}); e != nil {
			t.Fatal(e)
		}
	}
	if calls != 3 {
		t.Fatal(calls)
	}
}
