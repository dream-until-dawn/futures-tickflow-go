// Package evidence 标识和校验原始资料；哈希只证明字节身份，不认证资料真伪或制度覆盖。
package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"time"
)

// Ref 绑定原件来源与实际取得时刻。版本不是资料业务生效时间。
type Ref struct {
	ID          string    `json:"id"`
	URL         string    `json:"url"`
	SHA256      string    `json:"sha256"`
	RetrievedAt time.Time `json:"retrieved_at"`
	Version     string    `json:"version,omitempty"`
}

// Validate 要求完整身份；调用方不能以文件修改时刻补造取得时刻。
func (r Ref) Validate() error {
	u, err := url.Parse(r.URL)
	digest, hexerr := hex.DecodeString(r.SHA256)
	if r.ID == "" || err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || hexerr != nil || len(digest) != 32 || r.RetrievedAt.IsZero() {
		return fmt.Errorf("evidence: 原件身份不完整: %q", r.ID)
	}
	return nil
}

// Verify 校验实际字节与已冻结身份一致；不把HTTP成功等同内容成功。
func (r Ref) Verify(raw []byte) error {
	if err := r.Validate(); err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != r.SHA256 {
		return fmt.Errorf("evidence: %s 原件SHA256不符", r.ID)
	}
	return nil
}
