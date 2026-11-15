package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/dream-until-dawn/futures-tickflow-go/archive/batch"
)

func TestEDBDirectoryPlanSHAAndSingleReservation(t *testing.T) {
	plan := filepath.Join(t.TempDir(), "plan.json")
	if err := os.WriteFile(plan, []byte(`{"jobs":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := directoryJobs(plan); err == nil {
		t.Fatal("篡改单不得生成网络任务")
	}
	if err := reserveDirectoryPlanAt(plan+".started", "first"); err != nil {
		t.Fatal(err)
	}
	if err := reserveDirectoryPlanAt(plan+".started", "second"); err == nil {
		t.Fatal("换采集目录不得重复启动")
	}
}

func TestEDBDirectoryPlanPositiveIdentityAndMutatedSource(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "symbols-response.gz"), []byte("frozen-original"), 0600); err != nil {
		t.Fatal(err)
	}
	keys := []map[string]string{}
	rows := []map[string]string{}
	jobs := []batch.Job{}
	for i := 1; i <= 6; i++ {
		key := fmt.Sprintf("SHFE.rb240%d", i)
		keys = append(keys, map[string]string{"source_key": key, "canonical": key})
		rows = append(rows, map[string]string{"exchange": "SHFE", "native": key[5:]})
		q := url.Values{"symbol": {key}, "period": {"86400"}, "start_time": {"2021-01-01 00:00:00"}, "end_time": {"2026-11-13 23:59:59"}}
		jobs = append(jobs, batch.Job{ID: fmt.Sprint(i), URL: "https://edb.shinnytech.com/md/kline?" + q.Encode()})
	}
	// 郑商所源键与规范键并不相同，不能靠给URL补四位年月伪造源身份。
	keys[0] = map[string]string{"source_key": "CZCE.CF105", "canonical": "CZCE.CF2105"}
	rows[0] = map[string]string{"exchange": "CZCE", "native": "CF2105"}
	q := url.Values{"symbol": {"CZCE.CF105"}, "period": {"86400"}, "start_time": {"2021-01-01 00:00:00"}, "end_time": {"2026-11-13 23:59:59"}}
	jobs[0].URL = "https://edb.shinnytech.com/md/kline?" + q.Encode()
	inv, _ := json.Marshal(map[string]any{"contracts": keys})
	official, _ := json.Marshal(map[string]any{"records": rows})
	if err := os.WriteFile(filepath.Join(root, "inventory.json"), inv, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "official.json"), official, 0600); err != nil {
		t.Fatal(err)
	}
	p := map[string]any{"jobs": jobs, "directory_root": root, "inventory_root": root, "directory_sha256": digest([]byte("frozen-original")), "inventory_sha256": digest(inv), "official_inputs": []map[string]string{{"file": filepath.Join(root, "official.json"), "sha256": digest(official)}}}
	raw, _ := json.Marshal(p)
	got, err := directoryJobsChecked(raw, digest(raw))
	if err != nil || len(got) != 6 {
		t.Fatal(got, err)
	}
	jobs[0].URL = "https://other.invalid/md/kline"
	raw, _ = json.Marshal(p)
	if _, err = directoryJobsChecked(raw, digest(raw)); err == nil {
		t.Fatal("重算执行单SHA也不能放宽URL身份")
	}
	jobs[0] = got[0]
	raw, _ = json.Marshal(p)
	if err = os.WriteFile(filepath.Join(root, "inventory.json"), append(inv, ' '), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = directoryJobsChecked(raw, digest(raw)); err == nil {
		t.Fatal("目录原件变动未拒绝")
	}
}

func TestEDBApprovedDirectoryPlansHaveNoRepeatedURLs(t *testing.T) {
	seen := map[string]bool{}
	for _, name := range []string{"daily-pilot-plan.json", "daily-official-inventory-plan.json", "daily-official-history-master-plan.json", "daily-additional-plan.json", "daily-cffex-observed-plan.json", "daily-czce-observed-plan.json"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "docs", "evidence", "historical-contract-directory-20261115", name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = approvedDirectoryPlanSHA(raw); err != nil {
			t.Fatal(err)
		}
		var plan directoryPlan
		if err = json.Unmarshal(raw, &plan); err != nil {
			t.Fatal(err)
		}
		for _, job := range plan.Jobs {
			if seen[job.URL] {
				t.Fatal("不同执行单重复了URL", job.URL)
			}
			seen[job.URL] = true
		}
	}
	if len(seen) != 1965 {
		t.Fatal("冻结的官方观察全集计数漂移", len(seen))
	}
}
