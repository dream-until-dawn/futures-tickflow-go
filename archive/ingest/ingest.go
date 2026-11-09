// Package ingest 提供官方原件的冻结批次入口，不依赖Calendar、catalog或Feed资格。
package ingest

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	tf "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/archive/batch"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/verified"
	"github.com/dream-until-dawn/futures-tickflow-go/refdata/evidence"
	"github.com/dream-until-dawn/futures-tickflow-go/source/officialdaily"
)

// Plan 必须事前冻结；日期仅表示归档目标，不认证目标里的交易日/挂牌全集。
type Plan struct {
	Schema        string        `json:"schema"`
	From          tf.TradingDay `json:"from"`
	To            tf.TradingDay `json:"to"`
	MaxRequests   int           `json:"max_requests"`
	MaxBytes      int64         `json:"max_bytes"`
	IntervalMS    int64         `json:"interval_ms"`
	TimeoutMS     int64         `json:"timeout_ms"`
	MaxLocalBytes int64         `json:"max_local_bytes"`
	Jobs          []batch.Job   `json:"jobs"`
}

// Report 保留每个结果、HTTP失败证据及分区缺口，不从计数推出全历史资格。
type Report struct {
	Schema        string                  `json:"schema"`
	PlanSHA       string                  `json:"plan_sha256"`
	Status        string                  `json:"status"`
	Requests      int                     `json:"requests"`
	Bytes         int64                   `json:"bytes"`
	TotalRequests int                     `json:"cumulative_requests"`
	TotalBytes    int64                   `json:"cumulative_bytes"`
	Results       []batch.Result          `json:"results"`
	Partitions    []batch.PartitionReport `json:"partitions"`
	Qualification string                  `json:"qualification"`
}

func hash(raw []byte) string { h := sha256.Sum256(raw); return hex.EncodeToString(h[:]) }

// Parse 校验预算、日期、市场及受支持格式；已知阻塞路线必须显式blocked。
func Parse(raw []byte) (Plan, error) {
	var p Plan
	if len(raw) > 16<<20 {
		return p, fmt.Errorf("ingest: 清单超过16MiB")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if e := d.Decode(&p); e != nil {
		return p, e
	}
	var tail any
	if e := d.Decode(&tail); e != io.EOF {
		return p, fmt.Errorf("ingest: 清单尾部还有内容")
	}
	if p.Schema != "historical-archive-v1" || p.From > p.To || len(p.Jobs) == 0 || len(p.Jobs) > 10000 {
		return p, fmt.Errorf("ingest: schema/区间/任务数无效")
	}
	if _, e := verified.Date(p.From); e != nil {
		return p, e
	}
	if _, e := verified.Date(p.To); e != nil {
		return p, e
	}
	if p.MaxRequests <= 0 || p.MaxRequests > 10000 || p.MaxBytes <= 0 || p.MaxBytes > 1<<40 || p.MaxLocalBytes <= 0 || p.MaxLocalBytes > 256<<20 || p.IntervalMS <= 0 || p.IntervalMS > 3600000 || p.TimeoutMS <= p.IntervalMS || p.TimeoutMS > 3600000 {
		return p, fmt.Errorf("ingest: 必须显式给出有效预算")
	}
	ids := map[string]bool{}
	for _, j := range p.Jobs {
		if !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,120}$`).MatchString(j.ID) || ids[j.ID] {
			return p, fmt.Errorf("ingest: 非法/重复ID")
		}
		ids[j.ID] = true
		u, e := url.Parse(j.URL)
		if e != nil || u.Scheme != "https" || u.User != nil || u.Host == "" || (u.Port() != "" && u.Port() != "443") {
			return p, fmt.Errorf("ingest: 原件必须绑定公开HTTPS身份")
		}
		switch j.Exchange {
		case "SHFE", "INE":
			if u.Hostname() != "www.shfe.com.cn" && u.Hostname() != "www.shfe.cn" && u.Hostname() != "www.ine.com.cn" && u.Hostname() != "www.ine.cn" {
				return p, fmt.Errorf("ingest: 非登记官方主机")
			}
		case "CZCE":
			if u.Hostname() != "www.czce.com.cn" {
				return p, fmt.Errorf("ingest: 非郑商所官方主机")
			}
		case "DCE", "GFEX":
			if j.Exchange == "DCE" && u.Hostname() != "www.dce.com.cn" && u.Hostname() != "www.dce.cn" || j.Exchange == "GFEX" && u.Hostname() != "www.gfex.com.cn" {
				return p, fmt.Errorf("ingest: 非登记市场主机")
			}
			if j.Blocked == "" && j.Local == "" {
				return p, fmt.Errorf("ingest: 已知DCE/GFEX路线未解，禁止自动网络尝试")
			}
		default:
			return p, fmt.Errorf("ingest: 未支持市场")
		}
		if j.Blocked != "" {
			continue
		}
		if j.Local != "" && (j.ExpectedSHA == "" || j.RetrievedAt.IsZero()) {
			return p, fmt.Errorf("ingest: 本地复用须原SHA和真实取得时刻")
		}
		switch j.Format {
		case "shfe-json", "czce-txt":
			day := tf.TradingDay(j.Day)
			if _, e = verified.Date(day); e != nil {
				return p, e
			}
			if day < p.From || day > p.To {
				return p, fmt.Errorf("ingest: 任务日期越界")
			}
			if j.Year != int(day)/10000 {
				return p, fmt.Errorf("ingest: 分区年份与业务日期不符")
			}
			if j.Format == "czce-txt" && j.Exchange != "CZCE" || j.Format == "shfe-json" && (j.Exchange != "SHFE" && j.Exchange != "INE") {
				return p, fmt.Errorf("ingest: 格式与市场不符")
			}
		case "shfe-annual-zip":
			if j.Exchange != "SHFE" && j.Exchange != "INE" || j.Year < int(p.From)/10000 || j.Year > int(p.To)/10000 {
				return p, fmt.Errorf("ingest: 年包市场/年份越界")
			}
		case "identity-only":
			if j.Local == "" {
				return p, fmt.Errorf("ingest: 未解析原件只允许有据本地导入")
			}
		default:
			return p, fmt.Errorf("ingest: 未支持格式 %q", j.Format)
		}
	}
	return p, nil
}

// ValidateOriginal 仅检查原件身份和对应结构，不问品种时段/费用/策略资格。
func ValidateOriginal(j batch.Job, raw []byte, r evidence.Ref) error {
	if e := r.Verify(raw); e != nil {
		return e
	}
	switch j.Format {
	case "shfe-json", "czce-txt":
		_, e := officialdaily.Parse(officialdaily.Document{Format: j.Format, Day: tf.TradingDay(j.Day), Evidence: r, Raw: raw})
		return e
	case "identity-only":
		return nil
	case "shfe-annual-zip":
		z, e := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
		if e != nil {
			return e
		}
		if len(z.File) == 0 || len(z.File) > 4096 {
			return fmt.Errorf("ingest: 年包成员数无效")
		}
		var expanded uint64
		seen := map[string]bool{}
		workbooks := 0
		for _, f := range z.File {
			if seen[f.Name] || strings.HasPrefix(f.Name, "/") || strings.Contains(strings.ReplaceAll(f.Name, "\\", "/"), "../") {
				return fmt.Errorf("ingest: 年包成员身份异常")
			}
			seen[f.Name] = true
			if f.UncompressedSize64 > (1<<30)-expanded {
				return fmt.Errorf("ingest: 展开超过1GiB")
			}
			expanded += f.UncompressedSize64
			if f.FileInfo().IsDir() {
				continue
			}
			name := strings.ToLower(f.Name)
			if !strings.HasSuffix(name, ".xls") && !strings.HasSuffix(name, ".xlsx") {
				return fmt.Errorf("ingest: 未知年包成员")
			}
			if !regexp.MustCompile(fmt.Sprintf(`(^|[^0-9])%d([^0-9]|$)`, j.Year)).MatchString(name) {
				return fmt.Errorf("ingest: 工作簿年份不符/未知")
			}
			workbooks++
			stream, e := f.Open()
			if e != nil {
				return e
			}
			_, e = io.Copy(io.Discard, stream)
			closeErr := stream.Close()
			if e != nil {
				return e
			}
			if closeErr != nil {
				return closeErr
			}
		}
		if workbooks == 0 {
			return fmt.Errorf("ingest: 无工作簿")
		}
		return nil
	default:
		return fmt.Errorf("ingest: 未知格式")
	}
}

// Run 默认离线。network=true才允许在冻结预算内访问已支持官方端点。
// 同一目录仅接受原清单；失败/未知状态沿用，增补/重试必须建立新计划新目录。
func Run(ctx context.Context, raw []byte, out string, network bool) (Report, error) {
	result := Report{Schema: "historical-archive-report-v1", PlanSHA: hash(raw), Status: "incomplete", Qualification: "raw_archive_only_not_calendar_roster_or_backtest_certification"}
	p, e := Parse(raw)
	if e != nil {
		return result, e
	}
	if e = os.MkdirAll(out, 0700); e != nil {
		return result, e
	}
	pin := filepath.Join(out, "frozen-plan.json")
	f, e := os.OpenFile(pin, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e == nil {
		_, writeErr := f.Write(raw)
		closeErr := f.Close()
		if writeErr != nil {
			return result, writeErr
		}
		if closeErr != nil {
			return result, closeErr
		}
	} else if os.IsExist(e) {
		saved, err := os.ReadFile(pin)
		if err != nil {
			return result, err
		}
		if hash(saved) != result.PlanSHA {
			return result, fmt.Errorf("ingest: 冻结清单已改变")
		}
	} else {
		return result, e
	}
	used, usedBytes, pending, unknown, e := priorUsage(out, p.Jobs)
	if e != nil {
		return result, e
	}
	result.TotalRequests = used
	result.TotalBytes = usedBytes
	var down *batch.Downloader
	if network && pending {
		if unknown {
			return result, fmt.Errorf("ingest: 有未知网络执行，累计预算无法证明，不发新请求")
		}
		if used >= p.MaxRequests || usedBytes >= p.MaxBytes {
			return result, fmt.Errorf("ingest: 累计预算已耗尽")
		}
		down, e = batch.NewDownloader(&http.Client{}, batch.Limits{Requests: p.MaxRequests - used, Bytes: p.MaxBytes - usedBytes, Interval: time.Duration(p.IntervalMS) * time.Millisecond, Timeout: time.Duration(p.TimeoutMS) * time.Millisecond})
		if e != nil {
			return result, e
		}
	}
	result.Results, e = batch.Run(ctx, filepath.Join(out, "originals"), p.Jobs, down, p.MaxLocalBytes, ValidateOriginal)
	if down != nil {
		result.Requests, result.Bytes = down.Usage()
	}
	result.TotalRequests += result.Requests
	result.TotalBytes += result.Bytes
	if e != nil {
		return result, e
	}
	result.Partitions, e = batch.Summarize(p.Jobs, result.Results)
	if e != nil {
		return result, e
	}
	result.Status = "received_all_planned_originals"
	for _, r := range result.Results {
		if r.Receipt.State != "received" {
			result.Status = "incomplete"
		}
	}
	encoded, e := json.MarshalIndent(result, "", "  ")
	if e != nil {
		return result, e
	}
	tmp := filepath.Join(out, "report.tmp")
	if e = os.WriteFile(tmp, append(encoded, '\n'), 0600); e != nil {
		return result, e
	}
	e = os.Rename(tmp, filepath.Join(out, "report.json"))
	return result, e
}
