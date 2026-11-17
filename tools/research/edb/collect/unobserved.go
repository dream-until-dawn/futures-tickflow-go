package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/dream-until-dawn/futures-tickflow-go/archive/batch"
)

const unobservedPilotSHA = "6e4f15a4705eeaf7ed44fc8f1bd759b9ae721358eb7afa23c90bcb0c7d40fecf"

func approvedUnobservedSHA(sha string) bool {
	return sha == unobservedPilotSHA || unobservedAnchorPlans[sha] || unobservedFollowupPlans[sha] || unobservedCurrentColdPlans[sha]
}

func isColdUnobserved(sha string) bool {
	return unobservedColdPlans[sha] || unobservedCurrentColdPlans[sha]
}

func checkUnobservedTarget(job batch.Job, key, canonical, sha string) error {
	if sha == unobservedPilotSHA {
		if key == "DCE.a2201" && canonical == key && job.Exchange == "DCE" && job.Product == "a" && job.ID == "a2201-unobserved-research" {
			return nil
		}
	} else if isColdUnobserved(sha) {
		// 郑商所三位源键只按冻结目录的显式四位规范键核对，绝不猜最近十年。
		prefix := "CZCE." + job.Product
		if job.Exchange == "CZCE" && regexp.MustCompile("^"+regexp.QuoteMeta(prefix)+`[0-9]{3}$`).MatchString(key) &&
			regexp.MustCompile("^"+regexp.QuoteMeta(prefix)+`[0-9]{4}$`).MatchString(canonical) &&
			key == prefix+canonical[len(canonical)-3:] && job.ID == key[5:]+"-unobserved-research" {
			return nil
		}
	} else if approvedUnobservedSHA(sha) && (job.Exchange == "DCE" || job.Exchange == "GFEX") && canonical == key &&
		regexp.MustCompile("^"+regexp.QuoteMeta(job.Exchange+"."+job.Product)+`[0-9]{4}$`).MatchString(key) &&
		job.ID == key[len(job.Exchange)+1:]+"-unobserved-research" {
		return nil
	}
	return fmt.Errorf("目录研究目标越界")
}

func unobservedPilotJobs(planFile string) ([]batch.Job, error) {
	raw, err := os.ReadFile(planFile)
	if err != nil {
		return nil, err
	}
	return directoryJobsPolicy(raw, digest(raw), false)
}

func reserveUnobservedPilot(planFile, root string) error {
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		return fmt.Errorf("既有或未知采集目录，禁止消费单次许可")
	}
	raw, err := os.ReadFile(planFile)
	if err != nil || !approvedUnobservedSHA(digest(raw)) {
		return fmt.Errorf("目录研究单次计划身份不符")
	}
	var plan directoryPlan
	if err := json.Unmarshal(raw, &plan); err != nil {
		return err
	}
	marker := "unobserved-" + digest(raw) + ".started.json"
	// 原已启动探针的标记绝不迁址，否则换目录会重新消费旧授权。
	if digest(raw) == unobservedPilotSHA {
		marker = "unobserved-a2201-20261117.started.json"
	}
	return reserveDirectoryPlanAt(filepath.Join(plan.DirectoryRoot, marker), root)
}
