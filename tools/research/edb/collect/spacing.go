package main

import (
	"time"

	"github.com/dream-until-dawn/futures-tickflow-go/archive/batch"
)

// 研究任务始终串行；以上一次实际请求及归档返回为锚点，避免不同归档开销侵蚀间隔。
func completionPacedFetch(interval time.Duration, fetch func(batch.Job) (batch.Result, error)) func(batch.Job) (batch.Result, error) {
	var completed time.Time
	return func(job batch.Job) (batch.Result, error) {
		if !completed.IsZero() {
			if delay := time.Until(completed.Add(interval)); delay > 0 {
				time.Sleep(delay)
			}
		}
		defer func() { completed = time.Now() }()
		return fetch(job)
	}
}
