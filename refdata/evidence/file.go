package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
)

// File 绑定本地原件与外部身份。MaxBytes限制读取，不从文件时间补造来源时间。
type File struct {
	Path     string `json:"path"`
	Ref      Ref    `json:"ref"`
	MaxBytes int64  `json:"max_bytes"`
}

// Verify 流式复核本地原件，不分配与年包同等大小的第二份内存。
// 消费者可在初始、重开和Feed结束三处调用；校验不替代单写者或只读快照纪律。
func (f File) Verify() error {
	if err := f.Ref.Validate(); err != nil {
		return err
	}
	if f.MaxBytes <= 0 || f.MaxBytes > 1<<40 {
		return fmt.Errorf("evidence: 缺少文件字节上限")
	}
	file, err := os.Open(f.Path)
	if err != nil {
		return err
	}
	defer file.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(file, f.MaxBytes+1))
	if err != nil {
		return err
	}
	if n > f.MaxBytes {
		return fmt.Errorf("evidence: 文件超过字节上限")
	}
	if hex.EncodeToString(h.Sum(nil)) != f.Ref.SHA256 {
		return fmt.Errorf("evidence: 本地原件哈希不符 %s", f.Ref.ID)
	}
	return nil
}

// VerifyFiles 顺序检查全部冻结原件；一个失败就不能把本批结果作为完整证据。
func VerifyFiles(files []File) error {
	if len(files) == 0 {
		return fmt.Errorf("evidence: 空证据集合")
	}
	seen := map[string]bool{}
	for _, f := range files {
		if seen[f.Ref.ID] {
			return fmt.Errorf("evidence: 重复证据身份")
		}
		seen[f.Ref.ID] = true
		if err := f.Verify(); err != nil {
			return err
		}
	}
	return nil
}
