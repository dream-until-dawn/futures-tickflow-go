// Package batch 提供有界、可审计的原件获取与恢复。它不认证交易日历或挂牌全集。
package batch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/dream-until-dawn/futures-tickflow-go/refdata/evidence"
)

// Limits 限制实际HTTP尝试、读取字节、最小间隔和单次总超时；所有值必须显式给出。
type Limits struct {
	Requests          int
	Bytes             int64
	Interval, Timeout time.Duration
}

// Downloader 串行获取，不自动重试或跟随重定向。连接失败也消耗请求预算。
type Downloader struct {
	mu               sync.Mutex
	client           *http.Client
	limits           Limits
	requests         int
	bytes            int64
	last             time.Time
	nativeHTTP       map[string]NativeHTTPGrant
	maxResponseBytes int64
}

// NewDownloader 保留传入Transport的正常TLS校验，不设置InsecureSkipVerify。
func NewDownloader(client *http.Client, l Limits) (*Downloader, error) {
	if client == nil || l.Requests <= 0 || l.Bytes <= 0 || l.Bytes > 1<<40 || l.Interval <= 0 || l.Timeout <= l.Interval {
		return nil, fmt.Errorf("batch: 请求/字节/间隔/超时预算非法")
	}
	h := *client
	h.Jar = nil // 公开资料下载不继承调用方的账户Cookie。
	h.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	h.Timeout = l.Timeout
	return &Downloader{client: &h, limits: l}, nil
}

// Usage 返回当前调用实例消耗，不把恢复读取算作网络请求。
func (d *Downloader) Usage() (int, int64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.requests, d.bytes
}

// Fetch 仅做一次公开HTTP GET；HTTPS为正式输入，HTTP仅允许本机测试服务器。
// 读取超预算探针字节会计入实际字节，故越界时最大多读一个字节并立即失败。
func (d *Downloader) Fetch(ctx context.Context, address string) ([]byte, error) {
	result, err := d.FetchDetailed(ctx, address)
	return result.Body, err
}

// Job 是冻结原件身份。Local非空走离线复用；本地原件必须给ExpectedSHA。
// Format、Day供验证器解释，不凭扩展名猜测。Blocked禁止网络尝试。
type Job struct {
	ID          string    `json:"id"`
	URL         string    `json:"url"`
	Format      string    `json:"format"`
	Day         int32     `json:"day"`
	Local       string    `json:"local,omitempty"`
	ExpectedSHA string    `json:"expected_sha256,omitempty"`
	RetrievedAt time.Time `json:"retrieved_at,omitempty"`
	Blocked     string    `json:"blocked,omitempty"`
	Exchange    string    `json:"exchange,omitempty"`
	Product     string    `json:"product,omitempty"`
	Year        int       `json:"year,omitempty"`
	Version     string    `json:"version,omitempty"`
}

// Receipt 的received仅表示原件通过指定内容验证器，不表示历史覆盖认证。
type Receipt struct {
	JobSHA    string       `json:"job_sha256"`
	State     string       `json:"state"`
	Evidence  evidence.Ref `json:"evidence"`
	Bytes     int64        `json:"bytes"`
	Error     string       `json:"error,omitempty"`
	BodyState string       `json:"body_state"` // complete / partial / none
	HTTP      *Download    `json:"http,omitempty"`
}

// Result 逐项给出失败/未知/阻塞，不能只返回一个成功计数。
type Result struct {
	ID      string  `json:"id"`
	Receipt Receipt `json:"receipt"`
	Resumed bool    `json:"resumed"`
}

// Validator 必须检查内容类型、业务日期、所需字段等，而不只是HTTP状态。
type Validator func(Job, []byte, evidence.Ref) error

func digest(raw []byte) string { h := sha256.Sum256(raw); return hex.EncodeToString(h[:]) }
func atomic(path string, v any) error {
	raw, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	tmp := path + ".tmp"
	if e = os.WriteFile(tmp, append(raw, '\n'), 0600); e != nil {
		return e
	}
	return os.Rename(tmp, path)
}

var idPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,120}$`)

// Run 按冻结顺序执行并落盘收据，受单写者锁保护。未知启动、失败旧收据不自动重试。
// 重跑成功项时重新核原件SHA和内容；原件篡改不能沿用旧成功。
// maxLocalBytes也限制离线文件读取，避免读入无限本地文件。
func Run(ctx context.Context, root string, jobs []Job, down *Downloader, maxLocalBytes int64, validate Validator) ([]Result, error) {
	if validate == nil || maxLocalBytes <= 0 || maxLocalBytes > 1<<40 {
		return nil, fmt.Errorf("batch: 缺少内容验证器/本地字节上限")
	}
	seen := map[string]bool{}
	for _, j := range jobs {
		if !idPattern.MatchString(j.ID) || seen[j.ID] {
			return nil, fmt.Errorf("batch: 非法/重复ID")
		}
		seen[j.ID] = true
	}
	if e := os.MkdirAll(root, 0700); e != nil {
		return nil, e
	}
	lock := filepath.Join(root, "writer.lock")
	f, e := os.OpenFile(lock, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return nil, fmt.Errorf("batch: 存在写锁或未知先前运行: %w", e)
	}
	f.Close()
	defer os.Remove(lock)
	var out []Result
	for _, j := range jobs {
		if e = ctx.Err(); e != nil {
			return out, e
		}
		spec, _ := json.Marshal(j)
		identity := digest(spec)
		receiptPath := filepath.Join(root, j.ID+".json")
		rawPath := filepath.Join(root, j.ID+".body")
		existing, readErr := os.ReadFile(receiptPath)
		if readErr == nil {
			var old Receipt
			if e = json.Unmarshal(existing, &old); e != nil {
				return out, e
			}
			if old.JobSHA != identity {
				return out, fmt.Errorf("batch: 冻结清单变更 %s", j.ID)
			}
			if old.State != "received" && old.State != "in_progress" && old.State != "failed" && old.State != "blocked" {
				return out, fmt.Errorf("batch: 未知收据状态")
			}
			if old.BodyState != "none" && old.BodyState != "complete" && old.BodyState != "partial" {
				return out, fmt.Errorf("batch: 正文完整性未知")
			}
			if old.State == "received" {
				if err := validateNativeReceipt(down, j.URL, old.HTTP); err != nil {
					return out, err
				}
				if old.BodyState != "complete" || (old.HTTP != nil && (!old.HTTP.Complete || old.HTTP.Status != http.StatusOK || !old.HTTP.ResponseReceived)) {
					return out, fmt.Errorf("batch: 成功收据实际不是完整200正文")
				}
				if old.Evidence.ID != j.ID || old.Evidence.URL != j.URL || old.Evidence.Version != j.Version || (j.ExpectedSHA != "" && old.Evidence.SHA256 != j.ExpectedSHA) || (j.Local != "" && !old.Evidence.RetrievedAt.Equal(j.RetrievedAt)) {
					return out, fmt.Errorf("batch: 收据与冻结原件身份不符")
				}
				raw, err := readBounded(rawPath, maxLocalBytes)
				if err != nil {
					return out, err
				}
				if err = old.Evidence.Verify(raw); err != nil {
					return out, err
				}
				if int64(len(raw)) != old.Bytes {
					return out, fmt.Errorf("batch: 收据字节不符")
				}
				if err = validate(j, raw, old.Evidence); err != nil {
					return out, err
				}
			}
			if old.State != "received" && old.BodyState != "none" {
				failureLimit := maxLocalBytes
				if old.BodyState == "partial" {
					failureLimit++
				} // 恢复已封存的单字节溢出探针。
				raw, err := readBounded(rawPath, failureLimit)
				if err != nil {
					return out, err
				}
				if err = old.Evidence.Verify(raw); err != nil {
					return out, err
				}
				if int64(len(raw)) != old.Bytes {
					return out, fmt.Errorf("batch: 失败正文长度不符")
				}
			}
			// 非received结果保持原状态；未知运行尤其不能再次发起。
			out = append(out, Result{ID: j.ID, Receipt: old, Resumed: true})
			continue
		}
		if !os.IsNotExist(readErr) {
			return out, readErr
		}
		r := Receipt{JobSHA: identity, State: "in_progress", BodyState: "none"}
		if e = atomic(receiptPath, r); e != nil {
			return out, e
		}
		var raw []byte
		var err error
		bodyAvailable := false
		at := j.RetrievedAt
		switch {
		case j.Blocked != "":
			r.State = "blocked"
			r.Error = j.Blocked
		case j.Local != "":
			if j.ExpectedSHA == "" || j.RetrievedAt.IsZero() {
				err = fmt.Errorf("batch: 本地复用缺原件SHA/实际取得时刻")
			} else {
				raw, err = readBounded(j.Local, maxLocalBytes)
				bodyAvailable = raw != nil
				if bodyAvailable {
					r.BodyState = "complete"
					if err != nil {
						r.BodyState = "partial"
					}
				}
			}
		case down == nil:
			err = fmt.Errorf("batch: 网络未授权/未配置")
		default:
			var downloaded Download
			downloaded, err = down.FetchDetailed(ctx, j.URL)
			raw = downloaded.Body
			bodyAvailable = downloaded.ResponseReceived
			at = downloaded.RequestedAt
			metadata := downloaded
			metadata.Body = nil
			r.HTTP = &metadata
			if bodyAvailable {
				r.BodyState = "partial"
				if downloaded.Complete {
					r.BodyState = "complete"
				}
			}
		}
		if r.State != "blocked" {
			if bodyAvailable {
				r.Evidence = evidence.Ref{ID: j.ID, URL: j.URL, SHA256: digest(raw), RetrievedAt: at, Version: j.Version}
				r.Bytes = int64(len(raw))
				if err == nil && j.ExpectedSHA != "" && j.ExpectedSHA != r.Evidence.SHA256 {
					err = fmt.Errorf("batch: 预期SHA不符")
				}
				if err == nil {
					err = r.Evidence.Verify(raw)
				}
				if err == nil {
					err = validate(j, raw, r.Evidence)
				}
				// 即使HTTP失败、正文截断或超限，也保留有界前缀及其真实SHA。
				if e = os.WriteFile(rawPath, raw, 0600); e != nil {
					return out, e
				}
			}
			if err == nil && !bodyAvailable {
				err = fmt.Errorf("batch: 未取得可验证正文")
			}
			if err != nil {
				r.State = "failed"
				r.Error = err.Error()
			} else {
				r.State = "received"
			}
		}
		if e = atomic(receiptPath, r); e != nil {
			return out, e
		}
		out = append(out, Result{ID: j.ID, Receipt: r})
	}
	return out, nil
}
func readBounded(path string, limit int64) ([]byte, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	raw, e := io.ReadAll(io.LimitReader(f, limit+1))
	if e != nil {
		return raw, e
	}
	if int64(len(raw)) > limit {
		return raw, fmt.Errorf("batch: 本地文件超过上限")
	}
	return raw, nil
}
