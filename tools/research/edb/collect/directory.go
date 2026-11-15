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

func approvedDirectoryPlanSHA(raw []byte) (string, error) {
	sha := digest(raw)
	if sha != directoryPlanSHA && sha != officialInventoryPlanSHA {
		return "", fmt.Errorf("日线执行单SHA不符")
	}
	return sha, nil
}

type directoryPlan struct {
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
	if digest(raw) != expectedSHA {
		return nil, fmt.Errorf("日线执行单SHA不符")
	}
	var p directoryPlan
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
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
			Key string `json:"source_key"`
		} `json:"contracts"`
	}
	if err = json.Unmarshal(inventoryRaw, &inv); err != nil {
		return nil, err
	}
	keys := map[string]bool{}
	for _, c := range inv.Contracts {
		keys[c.Key] = true
	}
	observed := map[string]bool{}
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
	if len(p.Jobs) == 0 || len(p.Jobs) > 128 {
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
		if u.Scheme != "https" || u.Host != "edb.shinnytech.com" || u.Path != "/md/kline" || u.User != nil || u.Fragment != "" || len(q) != 4 || q.Get("period") != "86400" || q.Get("start_time") != "2021-01-01 00:00:00" || q.Get("end_time") != "2026-11-13 23:59:59" || !keys[key] || !observed[key] || seen[job.URL] {
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
