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

func TestSinaOriginEvidenceRetainsOriginalLineEndings(t *testing.T) {
	base := "../../../docs/evidence/sina-vendor-origin-20261114"
	b, e := os.ReadFile(filepath.Join(base, "files.json"))
	if e != nil {
		t.Fatal(e)
	}
	var files []struct {
		File   string
		Bytes  int
		SHA256 string
	}
	if e = json.Unmarshal(b, &files); e != nil {
		t.Fatal(e)
	}
	if len(files) != 4 {
		t.Fatal("来源封存范围变更")
	}
	for _, f := range files {
		raw, e := os.ReadFile(filepath.Join(base, f.File))
		if e != nil {
			t.Fatal(e)
		}
		sum := sha256.Sum256(raw)
		if len(raw) != f.Bytes || hex.EncodeToString(sum[:]) != f.SHA256 {
			t.Fatalf("原始出处字节/行尾被改变 %s", f.File)
		}
	}
}

func TestSinaExpansionPreservesFailureAndUnattempted(t *testing.T) {
	base := "../../../docs/evidence/sina-vendor-expanded-20261114"
	b, e := os.ReadFile(filepath.Join(base, "files.json"))
	if e != nil {
		t.Fatal(e)
	}
	var files []struct {
		File   string
		Bytes  int
		SHA256 string
	}
	if e = json.Unmarshal(b, &files); e != nil {
		t.Fatal(e)
	}
	if len(files) != 146 {
		t.Fatal("扩样封存范围变更")
	}
	for _, f := range files {
		raw, e := os.ReadFile(filepath.Join(base, filepath.FromSlash(f.File)))
		if e != nil {
			t.Fatal(e)
		}
		sum := sha256.Sum256(raw)
		if len(raw) != f.Bytes || hex.EncodeToString(sum[:]) != f.SHA256 {
			t.Fatalf("扩样原件改变 %s", f.File)
		}
	}
	b, e = os.ReadFile(filepath.Join(base, "request-ledger.json"))
	if e != nil {
		t.Fatal(e)
	}
	var ledger []struct {
		ID, Kind, State string
		Attempted       bool
	}
	if e = json.Unmarshal(b, &ledger); e != nil {
		t.Fatal(e)
	}
	market, static, unattempted := 0, 0, 0
	for _, r := range ledger {
		if r.Kind == "static-source" {
			if r.Attempted {
				static++
			}
			continue
		}
		if r.Attempted {
			market++
		} else {
			unattempted++
		}
		if r.ID == "daily-A2711" && (!r.Attempted || r.State != "failed") {
			t.Fatal("null不可改成成功/未发请求")
		}
	}
	if market != 55 || static != 1 || unattempted != 175 {
		t.Fatalf("真实计数 %d %d %d", market, static, unattempted)
	}
	b, e = os.ReadFile(filepath.Join(base, "raw", "daily-A2711.jsonp"))
	if e != nil {
		t.Fatal(e)
	}
	if len(b) != 76 || !strings.Contains(string(b), "=(null);") {
		t.Fatal("失败原件不可改写")
	}
}

func TestSinaFinalIsolationEvidenceAndUnknownAreNotCoverage(t *testing.T) {
	base := "../../../docs/evidence/sina-null-results-20261114"
	b, e := os.ReadFile(filepath.Join(base, "files.json"))
	if e != nil {
		t.Fatal(e)
	}
	var files []struct {
		File   string
		Bytes  int
		SHA256 string
	}
	if e = json.Unmarshal(b, &files); e != nil {
		t.Fatal(e)
	}
	if len(files) != 542 {
		t.Fatal("最终封存范围变更")
	}
	for _, f := range files {
		raw, e := os.ReadFile(filepath.Join(base, filepath.FromSlash(f.File)))
		if e != nil {
			t.Fatal(e)
		}
		sum := sha256.Sum256(raw)
		if len(raw) != f.Bytes || hex.EncodeToString(sum[:]) != f.SHA256 {
			t.Fatalf("最终原件改变 %s", f.File)
		}
	}
	b, e = os.ReadFile(filepath.Join(base, "request-ledger.json"))
	if e != nil {
		t.Fatal(e)
	}
	var ledger []isolatedReceipt
	if e = json.Unmarshal(b, &ledger); e != nil {
		t.Fatal(e)
	}
	normal, unknown := 0, 0
	for _, r := range ledger {
		if r.ID == "daily-A2711" || r.Covered || r.FinancialEligible {
			t.Fatal("重取旧请求或提升资格")
		}
		switch r.State {
		case "received":
			normal++
		case "failed-quarantined":
			unknown++
			if r.Records != 0 || r.FailureClass != "exact-null-unavailable" || r.Evidence == nil || r.HTTP.Status != 200 {
				t.Fatal("未知项被改成可用行")
			}
		default:
			t.Fatalf("最终状态不符 %s", r.State)
		}
	}
	if len(ledger) != 175 || normal != 169 || unknown != 6 {
		t.Fatalf("%d %d %d", len(ledger), normal, unknown)
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
