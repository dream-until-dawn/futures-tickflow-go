package batch

import (
	"fmt"
	"sort"
)

// PartitionReport 按事前计划的市场/年/品种列出原件获取状态。
// Received只代表指定内容验证器通过；本报告永不自行认证历史制度或挂牌覆盖。
type PartitionReport struct {
	Exchange       string `json:"exchange"`
	Product        string `json:"product"`
	Year           int    `json:"year"`
	Planned        int    `json:"planned"`
	Received       int    `json:"received"`
	Failed         int    `json:"failed"`
	Blocked        int    `json:"blocked"`
	Unknown        int    `json:"unknown"`
	NotRun         int    `json:"not_run"`
	CoverageStatus string `json:"coverage_status"`
}

// Summarize 只统计同一冻结计划里的结果，拒绝重复和计划外成功。
// 没有结果记not_run，in_progress记unknown，不转为失败或成功。
func Summarize(jobs []Job, results []Result) ([]PartitionReport, error) {
	planned := map[string]Job{}
	by := map[string]*PartitionReport{}
	for _, j := range jobs {
		if _, ok := planned[j.ID]; ok {
			return nil, fmt.Errorf("batch: 重复计划ID")
		}
		planned[j.ID] = j
	}
	state := map[string]string{}
	for _, r := range results {
		if _, ok := planned[r.ID]; !ok {
			return nil, fmt.Errorf("batch: 计划外结果")
		}
		if _, ok := state[r.ID]; ok {
			return nil, fmt.Errorf("batch: 重复结果")
		}
		state[r.ID] = r.Receipt.State
	}
	for _, j := range jobs {
		k := fmt.Sprintf("%s/%04d/%s", j.Exchange, j.Year, j.Product)
		p := by[k]
		if p == nil {
			p = &PartitionReport{Exchange: j.Exchange, Product: j.Product, Year: j.Year, CoverageStatus: "unverified_calendar_and_roster"}
			by[k] = p
		}
		p.Planned++
		switch state[j.ID] {
		case "received":
			p.Received++
		case "failed":
			p.Failed++
		case "blocked":
			p.Blocked++
		case "in_progress":
			p.Unknown++
		case "":
			p.NotRun++
		default:
			return nil, fmt.Errorf("batch: 未知状态")
		}
	}
	keys := make([]string, 0, len(by))
	for k := range by {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]PartitionReport, 0, len(keys))
	for _, k := range keys {
		out = append(out, *by[k])
	}
	return out, nil
}
