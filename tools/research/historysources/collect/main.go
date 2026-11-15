// Command collect 获取按阶段冻结的公开资料候选；不发现链接、不取行情、不重试。
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/dream-until-dawn/futures-tickflow-go/archive/batch"
	"github.com/dream-until-dawn/futures-tickflow-go/refdata/evidence"
)

func candidates() []batch.Job {
	return []batch.Job{
		{ID: "pk-release", URL: "https://htfc.com/main/a/20240603/80164382.shtml", Format: "html", Version: "broker-reprint-unqualified"},
		{ID: "pk-rules", URL: "https://htfc.com/wz_upload/png_upload/20240603/1717400586452da88bb.pdf", Format: "pdf", Version: "broker-reprint-unqualified"},
		{ID: "pk-amendment", URL: "https://htfc.com/wz_upload/png_upload/20240603/1717400586478d92b7a.pdf", Format: "pdf", Version: "broker-reprint-unqualified"},
		{ID: "czce-year2024-reprint", URL: "https://m.hicend.com.cn/h5/CommonArticleDetail/10933", Format: "html", Version: "broker-reprint-unqualified"},
		{ID: "pk-contract2021", URL: "https://www.investor.org.cn/xxzx/tjzl/tjnrgmjytx/bk/kj/202302/P020230226499757160919.pdf", Format: "pdf", Version: "investor-site-copy-unqualified"},
		{ID: "zhunda-docs", URL: "https://zhundata.cn/hist-data-cloud.html", Format: "html", Version: "vendor-documentation-unqualified"},
	}
}

func cyCandidate() batch.Job {
	return batch.Job{ID: "cy-night-change", URL: "https://www.xfqh.cn/dt/142.52358.html", Format: "html", Version: "broker-trading-reminder-unqualified"}
}

// vendorAccessCandidates 只取公开说明，不能借此发API请求或使用任何账户密钥。
func vendorAccessCandidates() []batch.Job {
	return []batch.Job{
		{ID: "vendor-home", URL: "https://zhundata.cn/", Format: "html", Version: "vendor-public-doc-unqualified"},
		{ID: "vendor-history-api", URL: "https://zhundata.cn/hist-quotes-api.html", Format: "html", Version: "vendor-public-doc-unqualified"},
		{ID: "vendor-realtime-api", URL: "https://zhundata.cn/realtime-quotes-api.html", Format: "html", Version: "vendor-public-doc-unqualified"},
	}
}

// validate 仅核传输内容格式；完整规则/日期/许可须另行离线复核，received不表示准入。
func validate(job batch.Job, body []byte, ref evidence.Ref) error {
	if err := ref.Verify(body); err != nil {
		return err
	}
	if job.Format == "pdf" {
		if !bytes.HasPrefix(body, []byte("%PDF-")) {
			return fmt.Errorf("候选不是PDF")
		}
	} else if !bytes.Contains(bytes.ToLower(body), []byte("<html")) {
		return fmt.Errorf("候选不是HTML文档")
	} else {
		// 200挑战/空壳不得当正文继续同主机；标题只作取证停止门禁，不作资格认证。
		markers := map[string]string{"pk-release": "花生期货合约及业务规则", "czce-year2024-reprint": "节假日休市安排", "zhunda-docs": "future_minute", "cy-night-change": "全部夜盘交易品种"}
		markers["vendor-home"], markers["vendor-history-api"], markers["vendor-realtime-api"] = "准数据", "x-api-key", "timestamp"
		if marker := markers[job.ID]; marker == "" || !bytes.Contains(body, []byte(marker)) {
			return fmt.Errorf("未取得预期业务正文，停止该主机")
		}
	}
	return nil
}

type observation struct {
	ID    string `json:"id"`
	State string `json:"state"`
	Error string `json:"error,omitempty"`
}

// collect 在未知/失败后停止同主机，仍可核其他独立主机；全新根目录保证不能重发旧计划。
func collect(root string, fetch func(batch.Job) (batch.Result, error)) error {
	return collectJobs(root, candidates(), fetch)
}

func collectJobs(root string, jobs []batch.Job, fetch func(batch.Job) (batch.Result, error)) error {
	if err := os.Mkdir(root, 0700); err != nil {
		return fmt.Errorf("既有/未知目录禁止采集: %w", err)
	}
	plan := map[string]any{"jobs": jobs, "requests_max": len(jobs), "bytes_per_response_max": 8 << 20, "timeout_seconds": 15, "interval_seconds": 1, "calendar_qualified": false, "market_data_requests": 0}
	raw, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(root, "plan.json"), append(raw, '\n'), 0600); err != nil {
		return err
	}
	blocked := map[string]bool{}
	var results []observation
	failed := false
	for _, job := range jobs {
		u, _ := url.Parse(job.URL) // 地址是本文件冻结的常量，没有用户输入URL。
		item := observation{ID: job.ID, State: "blocked_after_host_failure"}
		if !blocked[u.Hostname()] {
			r, fetchErr := fetch(job)
			item.State = r.Receipt.State
			if fetchErr != nil || r.Receipt.State != "received" {
				failed, blocked[u.Hostname()] = true, true
				item.State = "failed"
				item.Error = r.Receipt.Error
				if fetchErr != nil {
					item.Error = fetchErr.Error()
				}
			}
		}
		results = append(results, item)
	}
	raw, err = json.MarshalIndent(results, "", "  ")
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(root, "results.json"), append(raw, '\n'), 0600); err != nil {
		return err
	}
	if failed {
		return fmt.Errorf("部分来源失败，原响应保留；不重试失败主机")
	}
	return nil
}

func main() {
	root := flag.String("root", "", "必须全新仓外目录")
	cyOnly := flag.Bool("cy-only", false, "另批仅核新发现的先锋CY调整提醒，不重复原六项")
	vendorOnly := flag.Bool("vendor-access-only", false, "另批仅核三个公开文档，不取市场数据/样例包或重跑旧项")
	flag.Parse()
	if *root == "" || (*cyOnly && *vendorOnly) {
		fmt.Fprintln(os.Stderr, "缺少root或混用了独立批次模式")
		os.Exit(1)
	}
	var last time.Time
	jobs := candidates()
	if *cyOnly {
		jobs = []batch.Job{cyCandidate()}
	}
	if *vendorOnly {
		jobs = vendorAccessCandidates()
	}
	err := collectJobs(*root, jobs, func(job batch.Job) (batch.Result, error) {
		if wait := time.Until(last.Add(time.Second)); !last.IsZero() && wait > 0 {
			time.Sleep(wait)
		}
		transport := &http.Transport{DialContext: (&net.Dialer{Timeout: 15 * time.Second}).DialContext, TLSHandshakeTimeout: 10 * time.Second, DisableKeepAlives: true}
		defer transport.CloseIdleConnections()
		transport.Proxy = func(r *http.Request) (*url.URL, error) {
			proxy, e := http.ProxyFromEnvironment(r)
			if e == nil && proxy != nil && proxy.User != nil {
				return nil, fmt.Errorf("不继承代理账户")
			}
			return proxy, e
		}
		d, e := batch.NewDownloader(&http.Client{Transport: transport}, batch.Limits{Requests: 1, Bytes: (8 << 20) - 1, Interval: time.Second, Timeout: 15 * time.Second})
		if e != nil {
			return batch.Result{}, e
		}
		last = time.Now()
		results, e := batch.Run(context.Background(), filepath.Join(*root, "archive"), []batch.Job{job}, d, 8<<20, validate)
		n, size := d.Usage()
		fmt.Printf("%s actual_attempts=%d actual_bytes=%d\n", job.ID, n, size)
		if e != nil {
			return batch.Result{}, e
		}
		if len(results) != 1 {
			return batch.Result{}, fmt.Errorf("结果数不符")
		}
		return results[0], nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
