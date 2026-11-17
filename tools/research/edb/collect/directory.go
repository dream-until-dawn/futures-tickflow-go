package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"github.com/dream-until-dawn/futures-tickflow-go/archive/batch"
)

const directoryPlanSHA = "f10b77e067fdbe543a31c49e1aee278b192de1375a5818aef5de7c3ce6bfa334"
const officialInventoryPlanSHA = "924f7c2159ffbf48a22cc1dfc93cd2f625ac75dcb069deb7a7560f5c4d078291"
const officialHistoryPlanSHA = "b04c0e8f8178832be71efadc76226199fba8142966643b9658ccf2903892e239"
const additionalMarketsPlanSHA = "fccf6b372e1c53fe6d2254d524d9ae3f3d677926684fbffd084c8a0810fec200"
const cffexObservedPlanSHA = "ea5e467a2c15fdaebef9e85dc8e6209695c508bbae007581503fe6ae3eecd233"
const czceObservedPlanSHA = "ab25149d2a1f6e9fdaf83b42590827d0527404282038ddd68d7a06748dfaa659"
const retainedHistoryPlanSHA = "9990b4b3ef8bafb715eb8a883c23188ca19dde5602b29f2a7e72b1341eca9eee"

func approvedDirectoryPlanSHA(raw []byte) (string, error) {
	sha := digest(raw)
	if sha != directoryPlanSHA && sha != officialInventoryPlanSHA && sha != officialHistoryPlanSHA && sha != additionalMarketsPlanSHA && sha != cffexObservedPlanSHA && sha != czceObservedPlanSHA && sha != retainedHistoryPlanSHA {
		return "", fmt.Errorf("日线执行单SHA不符")
	}
	return sha, nil
}

type directoryPlan struct {
	PriorCatalog struct {
		File string `json:"file"`
		SHA  string `json:"sha256"`
	} `json:"prior_catalog"`
	PriorRequests struct {
		File string `json:"file"`
		SHA  string `json:"sha256"`
	} `json:"prior_requests"`
	Jobs          []batch.Job `json:"jobs"`
	DirectoryRoot string      `json:"directory_root"`
	InventoryRoot string      `json:"inventory_root"`
	DirectorySHA  string      `json:"directory_sha256"`
	InventorySHA  string      `json:"inventory_sha256"`
	Inputs        []struct {
		File string `json:"file"`
		SHA  string `json:"sha256"`
	} `json:"official_inputs"`
}

// 先锁原件/目录/官方观察身份，再生成HTTP请求；复制执行目录不授权同计划重发。
func directoryJobs(planFile string) ([]batch.Job, error) {
	raw, err := os.ReadFile(planFile)
	if err != nil {
		return nil, err
	}
	sha, err := approvedDirectoryPlanSHA(raw)
	if err != nil {
		return nil, err
	}
	return directoryJobsChecked(raw, sha)
}

func directoryJobsChecked(raw []byte, expectedSHA string) ([]batch.Job, error) {
	return directoryJobsPolicy(raw, expectedSHA, true)
}

// 官方观察路线保持原规则；仅按SHA冻结的有限目录研究计划可用独立入口。
func directoryJobsPolicy(raw []byte, expectedSHA string, requireOfficial bool) ([]batch.Job, error) {
	if !requireOfficial && !approvedUnobservedSHA(expectedSHA) {
		return nil, fmt.Errorf("目录研究不是固定单次探针")
	}
	if digest(raw) != expectedSHA {
		return nil, fmt.Errorf("日线执行单SHA不符")
	}
	var p directoryPlan
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if expectedSHA == retainedHistoryPlanSHA && p.PriorCatalog.File == "" {
		return nil, fmt.Errorf("缺少旧请求状态锁")
	}
	limit := 8
	if isColdUnobserved(expectedSHA) {
		limit = 40
	}
	if !requireOfficial && (p.PriorCatalog.File == "" || len(p.Jobs) == 0 || len(p.Jobs) > limit || len(p.Inputs) != 0) {
		return nil, fmt.Errorf("目录研究必须有旧未请求状态且在冻结数量预算内，无伪造官方观察")
	}
	load := func(path, expected string) ([]byte, error) {
		b, e := os.ReadFile(path)
		if e != nil || digest(b) != expected {
			return nil, fmt.Errorf("目录/官方原件锁不符: %s", path)
		}
		return b, nil
	}
	if _, err := load(filepath.Join(p.DirectoryRoot, "symbols-response.gz"), p.DirectorySHA); err != nil {
		return nil, err
	}
	inventoryRaw, err := load(filepath.Join(p.InventoryRoot, "inventory.json"), p.InventorySHA)
	if err != nil {
		return nil, err
	}
	var inv struct {
		Contracts []struct {
			Key       string `json:"source_key"`
			Canonical string `json:"canonical"`
		} `json:"contracts"`
	}
	if err = json.Unmarshal(inventoryRaw, &inv); err != nil {
		return nil, err
	}
	keys := map[string]string{}
	for _, c := range inv.Contracts {
		keys[c.Key] = c.Canonical
	}
	// 新批必须在原封存目录中明确从未请求；空/失败/未知状态都不能续取。
	unrequested := map[string]bool{}
	if p.PriorCatalog.File != "" {
		b, e := load(p.PriorCatalog.File, p.PriorCatalog.SHA)
		if e != nil {
			return nil, e
		}
		var prior struct {
			Contracts []struct {
				Key          string `json:"source_key"`
				Availability struct {
					State string `json:"state"`
				} `json:"availability"`
			} `json:"contracts"`
		}
		if e = json.Unmarshal(b, &prior); e != nil {
			return nil, e
		}
		for _, c := range prior.Contracts {
			if c.Availability.State == "not_requested_on_directory_route" {
				unrequested[c.Key] = true
			}
		}
	}
	observed := map[string]bool{}
	excludedKeys := map[string][]string{}
	if !requireOfficial && expectedSHA != unobservedPilotSHA {
		b, e := load(p.PriorRequests.File, p.PriorRequests.SHA)
		if e != nil {
			return nil, fmt.Errorf("跨路线旧请求账本缺失或改变: %w", e)
		}
		var prior struct {
			Keys map[string][]string `json:"keys"`
		}
		if e = json.Unmarshal(b, &prior); e != nil {
			return nil, e
		}
		excludedKeys = prior.Keys
	}
	for _, input := range p.Inputs {
		b, e := load(input.File, input.SHA)
		if e != nil {
			return nil, e
		}
		var decoded struct {
			Records []struct{ Exchange, Native string }
		}
		if e = json.Unmarshal(b, &decoded); e != nil {
			return nil, e
		}
		for _, row := range decoded.Records {
			observed[row.Exchange+"."+row.Native] = true
		}
	}
	if len(p.Jobs) == 0 || len(p.Jobs) > 2048 {
		return nil, fmt.Errorf("日线冻结批次超范围")
	}
	seen := map[string]bool{}
	for _, job := range p.Jobs {
		u, e := url.Parse(job.URL)
		if e != nil {
			return nil, e
		}
		q := u.Query()
		key := q.Get("symbol")
		if p.PriorCatalog.File != "" && !unrequested[key] {
			return nil, fmt.Errorf("新批包含已请求或未知项")
		}
		canonical, exists := keys[key]
		if !requireOfficial {
			if e := checkUnobservedTarget(job, key, canonical, expectedSHA); e != nil {
				return nil, e
			}
			if _, exists := excludedKeys[key]; exists {
				return nil, fmt.Errorf("目录研究包含其他路线已请求或未知源键")
			}
		}
		if u.Scheme != "https" || u.Host != "edb.shinnytech.com" || u.Path != "/md/kline" || u.User != nil || u.Fragment != "" || len(q) != 4 || q.Get("period") != "86400" || q.Get("start_time") != "2021-01-01 00:00:00" || q.Get("end_time") != "2026-11-13 23:59:59" || !exists || (requireOfficial && !observed[canonical]) || seen[job.URL] {
			return nil, fmt.Errorf("日线URL/目录/官方观察不符")
		}
		seen[job.URL] = true
	}
	return p.Jobs, nil
}

func reserveDirectoryPlan(planFile, target string) error {
	raw, err := os.ReadFile(planFile)
	if err != nil {
		return err
	}
	sha, err := approvedDirectoryPlanSHA(raw)
	if err != nil {
		return fmt.Errorf("启动前执行单锁不符")
	}
	var p directoryPlan
	if err = json.Unmarshal(raw, &p); err != nil {
		return err
	}
	// 标记锚定在冻结的仓外目录，复制执行单到别处也不能再次启动。
	return reserveDirectoryPlanAt(filepath.Join(p.DirectoryRoot, "daily-plan-"+sha+".started.json"), target)
}

func reserveDirectoryPlanAt(marker, target string) error {
	f, err := os.OpenFile(marker, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("该日线执行单已有/未知启动，拒绝重发: %w", err)
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(map[string]string{"marker": filepath.Base(marker), "target_root": target})
}
