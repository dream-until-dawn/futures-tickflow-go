package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSinaGitEvidenceBytesAndScope(t *testing.T) {
	base := "../../../docs/evidence/sina-vendor-20261114"
	b, e := os.ReadFile(filepath.Join(base, "files.json"))
	if e != nil {
		t.Fatal(e)
	}
	var manifest []struct {
		File   string
		Bytes  int
		SHA256 string
	}
	if e = json.Unmarshal(b, &manifest); e != nil {
		t.Fatal(e)
	}
	if len(manifest) != 87 {
		t.Fatal("封存范围变更")
	}
	for _, f := range manifest {
		raw, e := os.ReadFile(filepath.Join(base, filepath.FromSlash(f.File)))
		if e != nil {
			t.Fatal(e)
		}
		sum := sha256.Sum256(raw)
		if len(raw) != f.Bytes || hex.EncodeToString(sum[:]) != f.SHA256 {
			t.Fatalf("Git证据改变 %s", f.File)
		}
	}
	b, e = os.ReadFile(filepath.Join(base, "scope.json"))
	if e != nil {
		t.Fatal(e)
	}
	var scope map[string]any
	if e = json.Unmarshal(b, &scope); e != nil {
		t.Fatal(e)
	}
	if scope["requests"] != float64(40) || scope["bytes"] != float64(359219) || scope["complete_2021_history"] != false || scope["historical_calendar_certified"] != false || scope["production_changed"] != false || scope["additional_requests_authorized"] != false {
		t.Fatal("范围/资格被改变")
	}
}

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
