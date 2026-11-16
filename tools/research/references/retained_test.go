package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRetainedReferencesPreserveReceiptsAndRejectMutations(t *testing.T) {
	root := t.TempDir()
	raw := []byte(`<Contracts><Contract><CtrCd>RM701</CtrCd><PrdCd>RM</PrdCd><PrdTp>期货</PrdTp><ExchCd>XZCE</ExchCd><YrCd>2027</YrCd><MnthCd>01</MnthCd><ExpiryTime>2027-01-15 15:00 北京时间</ExpiryTime><FrstTrdDt>2026-01-16</FrstTrdDt><LstTrdDt>2027-01-15</LstTrdDt></Contract></Contracts>`)
	url := address
	r := map[string]any{"id": "test-reference", "url": url, "final_url": url, "status": 200, "bytes": len(raw), "transport_state": "complete", "sha256": hash(raw), "requested_utc": "2026-11-16T11:00:00Z"}
	write := func() {
		t.Helper()
		b, e := json.Marshal([]any{r})
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(root, "requests.json"), b, 0600); e != nil {
			t.Fatal(e)
		}
	}
	write()
	if e := os.WriteFile(filepath.Join(root, "test-reference.body"), raw, 0600); e != nil {
		t.Fatal(e)
	}
	before, _ := os.ReadFile(filepath.Join(root, "requests.json"))
	if e := reviewRetained(root, filepath.Join(root, "good.json")); e != nil {
		t.Fatal(e)
	}
	after, _ := os.ReadFile(filepath.Join(root, "requests.json"))
	if string(before) != string(after) {
		t.Fatal("原回执被改")
	}
	r["status"] = 412
	write()
	if e := reviewRetained(root, filepath.Join(root, "bad.json")); e == nil || !strings.Contains(e.Error(), "回执不完整") {
		t.Fatalf("失败回执未拒绝: %v", e)
	}
	r["status"] = 200
	r["sha256"] = strings.Repeat("a", 64)
	write()
	if e := reviewRetained(root, filepath.Join(root, "tampered.json")); e == nil || !strings.Contains(e.Error(), "原字节身份") {
		t.Fatalf("篡改未拒绝: %v", e)
	}
}
