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
	"strings"
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

func edbDocumentCandidate() batch.Job {
	return batch.Job{ID: "edb-public-document", URL: "https://doc.shinnytech.com/edb/latest/md_server.html", Format: "html", Version: "shinny-edb-public-document-unqualified"}
}

func semanticsCandidates() []batch.Job {
	return []batch.Job{
		{ID: "semantics-algorithm", URL: "https://www.shinnytech.com/articles/reference/why-our-klines-are-different", Format: "html", Version: "vendor-algorithm-not-edb-daily-version-proof"},
		{ID: "semantics-old-kline", URL: "https://doc.shinnytech.com/tqsdk/1.8.2/reference/tqsdk.objs.html", Format: "html", Version: "versioned-sdk-schema-not-edb-history-proof"},
		{ID: "semantics-forum-case", URL: "https://forum.shinnytech.com/question/26581/answer/26582/", Format: "html", Version: "forum-response-not-independent-event-proof"},
		{ID: "semantics-pk-original", URL: "https://www.czce.com.cn/cn/rootfiles/2021/01/25/1605577988714900-1605577988801327.pdf", Format: "pdf", Version: "official-contract-candidate-2021"},
		{ID: "semantics-czce-rules", URL: "https://www.czce.com.cn/cn/uploadfile/2021/02/23/20210223143255447.pdf", Format: "pdf", Version: "official-rule-version-candidate-2021"},
	}
}

// followupCandidates 固定两个新文档入口；不含旧超时PDF、行情API或受阻首页。
func followupCandidates() []batch.Job {
	return []batch.Job{
		{ID: "followup-rule2019", URL: "https://www.czce.com.cn/cn/uploadfile/2019/10/30/20191030161831109.pdf", Format: "pdf", Version: "official-2020-effective-candidate-not-continuity-proof"},
		{ID: "followup-minute-forum", URL: "https://forum.shinnytech.com/question/26588/", Format: "html", Version: "forum-minute-context-not-edb-daily-version-proof"},
	}
}

func responseLimit(job batch.Job) int64 {
	if strings.HasPrefix(job.ID, "semantics-") || strings.HasPrefix(job.ID, "followup-") {
		if job.Format == "pdf" {
			return 4 << 20
		}
		return 2 << 20
	}
	if job.ID == "edb-public-document" {
		return 2 << 20
	}
	return 8 << 20
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
		markers["edb-public-document"] = "免费访问"
		markers["semantics-algorithm"], markers["semantics-old-kline"], markers["semantics-forum-case"] = "除日线以外", "open_oi", "自对冲"
		markers["followup-minute-forum"] = "盘前"
		if marker := markers[job.ID]; marker == "" || !bytes.Contains(body, []byte(marker)) {
			return fmt.Errorf("未取得预期业务正文，停止该主机")
		}
	}
	for _, anchor := range map[string][]string{
		"semantics-algorithm":   {"除日线以外", "连续交易时段"},
		"semantics-old-kline":   {"open_oi", "close_oi", "Kline"},
		"semantics-forum-case":  {"行权", "自对冲", "3,575,857"},
		"followup-minute-forum": {"盘前", "分钟", "合成规则"},
	}[job.ID] {
		if !bytes.Contains(body, []byte(anchor)) {
			return fmt.Errorf("未取得指定口径业务正文")
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
	limits := map[string]int64{}
	for _, job := range jobs {
		limits[job.ID] = responseLimit(job)
	}
	plan := map[string]any{"jobs": jobs, "requests_max": len(jobs), "bytes_per_response_max": limits, "timeout_seconds": 15, "interval_seconds": 1, "calendar_qualified": false, "market_data_requests": 0}
	if len(jobs) > 0 && strings.HasPrefix(jobs[0].ID, "semantics-") {
		plan["interval_seconds"] = 2
		plan["interval_anchor"] = "previous fetch/archive completed"
	}
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
	edbDocOnly := flag.Bool("edb-doc-only", false, "另批仅核EDB公开文档，不发业务GET或获取token")
	semanticsOnly := flag.Bool("semantics-only", false, "另批固定五项口径/合约新来源，不重试旧受阻URL")
	followupOnly := flag.Bool("followup-only", false, "另批固定两项新规则/分钟口径文档，不重跑旧项")
	prepareOnly := flag.Bool("prepare-only", false, "仅口径资料零请求预检")
	flag.Parse()
	modes := 0
	for _, selected := range []bool{*cyOnly, *vendorOnly, *edbDocOnly, *semanticsOnly, *followupOnly} {
		if selected {
			modes++
		}
	}
	if *root == "" || modes > 1 || (*prepareOnly && !*semanticsOnly && !*followupOnly) {
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
	if *edbDocOnly {
		jobs = []batch.Job{edbDocumentCandidate()}
	}
	interval := time.Second
	if *semanticsOnly || *followupOnly {
		jobs = semanticsCandidates()
		marker := "C:/Temp/tickflow-daily-semantics-sources-20261115.started.json"
		if *followupOnly {
			jobs = followupCandidates()
			marker = "C:/Temp/tickflow-edb-pk-followup-20261115.started.json"
		}
		interval = 2 * time.Second
		if *prepareOnly {
			var total int64
			for _, job := range jobs {
				total += responseLimit(job)
			}
			fmt.Printf("%d项口径候选预检通过；请求0；最大正文%d字节\n", len(jobs), total)
			return
		}
		f, e := os.OpenFile(marker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			fmt.Fprintln(os.Stderr, "口径资料已有/未知启动，拒绝重发:", e)
			os.Exit(1)
		}
		e = json.NewEncoder(f).Encode(map[string]string{"target_root": *root})
		f.Close()
		if e != nil {
			fmt.Fprintln(os.Stderr, e)
			os.Exit(1)
		}
	}
	err := collectJobs(*root, jobs, func(job batch.Job) (batch.Result, error) {
		if wait := time.Until(last.Add(interval)); !last.IsZero() && wait > 0 {
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
		limit := responseLimit(job)
		d, e := batch.NewDownloader(&http.Client{Transport: transport}, batch.Limits{Requests: 1, Bytes: limit - 1, Interval: time.Second, Timeout: 15 * time.Second})
		if e != nil {
			return batch.Result{}, e
		}
		results, e := batch.Run(context.Background(), filepath.Join(*root, "archive"), []batch.Job{job}, d, limit, validate)
		// 锚定归档完成点，不让文件操作开销侵蚀后续请求间隔。
		last = time.Now()
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
