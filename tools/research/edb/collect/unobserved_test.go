package main

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestColdFollowupsUseFrozenExplicitBindingsAndNoPriorSourceKey(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..", "docs")
	seen := map[string]bool{}
	count, jobs := 0, 0
	for _, directory := range []string{"history-followup-20261117", "current-cold-20261117"} {
		paths, err := filepath.Glob(filepath.Join(root, "research-plans", directory, "*.json"))
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range paths {
			if filepath.Base(path) == "index.json" {
				continue
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			sha := digest(raw)
			if !approvedUnobservedSHA(sha) {
				t.Fatal("未冻结的补充计划")
			}
			var plan struct {
				directoryPlan
				Bindings map[string]string `json:"bindings"`
			}
			if err := json.Unmarshal(raw, &plan); err != nil {
				t.Fatal(err)
			}
			ledgerName := filepath.Base(strings.ReplaceAll(plan.PriorRequests.File, "\\", "/"))
			ledgerRaw, err := os.ReadFile(filepath.Join(root, "evidence", ledgerName))
			if err != nil || digest(ledgerRaw) != plan.PriorRequests.SHA {
				t.Fatal("旧请求账本身份错误", err)
			}
			var ledger struct {
				Keys map[string][]string `json:"keys"`
			}
			if err := json.Unmarshal(ledgerRaw, &ledger); err != nil {
				t.Fatal(err)
			}
			for _, job := range plan.Jobs {
				u, err := url.Parse(job.URL)
				if err != nil {
					t.Fatal(err)
				}
				key := u.Query().Get("symbol")
				if seen[key] {
					t.Fatal("补充计划重复源键", key)
				}
				seen[key] = true
				if _, exists := ledger.Keys[key]; exists {
					t.Fatal("重试其他路线已请求或未知键", key)
				}
				if err := checkUnobservedTarget(job, key, plan.Bindings[key], sha); err != nil {
					t.Fatal(err)
				}
				if err := checkUnobservedTarget(job, key, "CZCE.WRONG", sha); err == nil {
					t.Fatal("年份/品种身份错配未拒绝")
				}
				jobs++
			}
			count++
		}
	}
	if count != 12 || jobs != 159 {
		t.Fatalf("补充冻结数量漂移 %d/%d", count, jobs)
	}
}

func TestHistoricalAnchorPlansAreSingleProductDisjointAndExcludeSeenKeys(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..", "docs")
	raw, err := os.ReadFile(filepath.Join(root, "evidence", "prior-edb-keys-20261117.json"))
	if err != nil {
		t.Fatal(err)
	}
	var ledger struct {
		Keys map[string][]string `json:"keys"`
	}
	if err := json.Unmarshal(raw, &ledger); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(root, "research-plans", "history-anchors-20261117", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	count := 0
	for _, path := range files {
		if filepath.Base(path) == "index.json" {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		sha := digest(raw)
		if !unobservedAnchorPlans[sha] {
			t.Fatalf("未冻结的执行单 %s", path)
		}
		if _, err := approvedDirectoryPlanSHA(raw); err == nil {
			t.Fatal("旧模式不应接收新研究计划")
		}
		var plan directoryPlan
		if err := json.Unmarshal(raw, &plan); err != nil {
			t.Fatal(err)
		}
		if len(plan.Jobs) == 0 || len(plan.Jobs) > 8 || plan.PriorRequests.File == "" || plan.PriorRequests.SHA == "" {
			t.Fatal("缺少预算或跨路线拒绝账本")
		}
		pair := plan.Jobs[0].Exchange + "." + plan.Jobs[0].Product
		for _, job := range plan.Jobs {
			key := job.Exchange + "." + strings.TrimSuffix(job.ID, "-unobserved-research")
			if job.Exchange+"."+job.Product != pair || seen[key] {
				t.Fatal("跨品种或重复研究键")
			}
			if _, exists := ledger.Keys[key]; exists {
				t.Fatalf("重用旧已请求或未知键 %s", key)
			}
			if err := checkUnobservedTarget(job, key, key, sha); err != nil {
				t.Fatal(err)
			}
			if err := checkUnobservedTarget(job, key, "GFEX.wrong", sha); err == nil {
				t.Fatal("目录身份冲突未拒绝")
			}
			seen[key] = true
		}
		count++
	}
	if count != 28 || len(seen) != 169 || len(unobservedAnchorPlans) != 28 {
		t.Fatalf("冻结范围漂移 %d/%d", count, len(seen))
	}
}

func TestUnobservedPilotIsSeparateFrozenAndCannotBypassOldMode(t *testing.T) {
	path := filepath.Join("..", "..", "..", "..", "docs", "research-plans", "edb-unobserved-pilot-20261117.json")
	raw, err := os.ReadFile(path)
	if err != nil || digest(raw) != unobservedPilotSHA {
		t.Fatalf("研究单身份不符: %v", err)
	}
	if _, err := approvedDirectoryPlanSHA(raw); err == nil {
		t.Fatal("研究单不能被旧官方模式批准")
	}
	if _, err := directoryJobsPolicy(append(raw, ' '), unobservedPilotSHA, false); err == nil || !strings.Contains(err.Error(), "SHA") {
		t.Fatalf("执行单篡改没有被身份门禁拒绝: %v", err)
	}
	if _, err := directoryJobsPolicy(raw, digest([]byte("arbitrary")), false); err == nil || !strings.Contains(err.Error(), "固定单次") {
		t.Fatalf("任意SHA不能开放无官方观察路线: %v", err)
	}
	root := t.TempDir()
	if err := reserveUnobservedPilot(path, root); err == nil || !strings.Contains(err.Error(), "既有或未知") {
		t.Fatalf("既有目录不能先消耗单次启动: %v", err)
	}
}
