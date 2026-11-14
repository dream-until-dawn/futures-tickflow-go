package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSinaFrozenToolReplaysAndDoesNotPublish(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private-result")
	if e := reviewSamples("../../../source/sinavendor/testdata/receipts.json", root); e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(filepath.Join(root, "coverage.json"))
	if e != nil {
		t.Fatal(e)
	}
	var report struct {
		Published bool
		Samples   []sampleReview
	}
	if e = json.Unmarshal(b, &report); e != nil {
		t.Fatal(e)
	}
	if report.Published || len(report.Samples) != 4 {
		t.Fatal("scope")
	}
	total := 0
	for _, r := range report.Samples {
		if r.CalendarCertified || r.RosterComplete || r.FinancialEligible {
			t.Fatal("资格提升")
		}
		if r.Symbol == "DCE.c2105" {
			if r.MechanicalComplete || !strings.Contains(r.Failure, "2021-05-14") {
				t.Fatalf("%+v", r)
			}
			if _, e = os.Stat(filepath.Join(root, "C2105-store")); !os.IsNotExist(e) {
				t.Fatal("失败区间不应打开存储")
			}
		} else {
			if !r.MechanicalComplete || r.ResumeAdded != 0 {
				t.Fatalf("%+v", r)
			}
			total += r.FeedRows
		}
	}
	if total != 485 {
		t.Fatal(total)
	}
	if e = reviewSamples("../../../source/sinavendor/testdata/receipts.json", root); e == nil {
		t.Fatal("结果目录不得静默复用/覆盖")
	}
}
