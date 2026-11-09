package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEvidenceHashAndIdentity(t *testing.T) {
	raw := []byte("original\n")
	sum := sha256.Sum256(raw)
	r := Ref{ID: "original", URL: "https://example.org/original", SHA256: hex.EncodeToString(sum[:]), RetrievedAt: time.Now()}
	if e := r.Verify(raw); e != nil {
		t.Fatal(e)
	}
	if e := r.Verify([]byte("changed\n")); e == nil {
		t.Fatal("篡改未拒绝")
	}
	r.RetrievedAt = time.Time{}
	if e := r.Verify(raw); e == nil {
		t.Fatal("未知取得时间通过")
	}
}

func TestFrozenFilesDetectMutationAndBudget(t *testing.T) {
	raw := []byte("frozen")
	sum := sha256.Sum256(raw)
	path := filepath.Join(t.TempDir(), "original.txt")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	file := File{Path: path, Ref: Ref{ID: "frozen", URL: "https://example.org/frozen", SHA256: hex.EncodeToString(sum[:]), RetrievedAt: time.Now()}, MaxBytes: 64}
	if err := VerifyFiles([]File{file}); err != nil {
		t.Fatal(err)
	}
	small := file
	small.MaxBytes = 2
	if err := small.Verify(); err == nil {
		t.Fatal("文件预算失效")
	}
	if err := os.WriteFile(path, []byte("modified"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyFiles([]File{file}); err == nil {
		t.Fatal("结束关口漏掉原件变化")
	}
}
