// Command collect 只取本批两条新发现的正常HTTPS候选；不重试、不自动发现其他链接。
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
		{ID: "pr-first-rules", URL: "https://www.czce.com.cn/cn/uploadfile/2024/08/30/20240830162542437.pdf", Format: "pdf", Version: "official-candidate-unqualified"},
		{ID: "year-end-2025-candidate", URL: "https://www.czce.com.cn/cn/gyjys/jysdt/ggytz/webinfo/2024/12/1726399021155050.htm", Format: "html-candidate", Version: "official-candidate-unqualified"},
	}
}

func validate(job batch.Job, body []byte, ref evidence.Ref) error {
	if err := ref.Verify(body); err != nil {
		return err
	}
	if len(body) == 0 {
		return fmt.Errorf("空候选正文")
	}
	if job.Format == "pdf" && !bytes.HasPrefix(body, []byte("%PDF-")) {
		return fmt.Errorf("候选并非PDF，保留诊断停止该主机")
	}
	// HTML只保存未获资格的原响应，后续离线核正文；received不是规则或日历认证。
	return nil
}

func collect(root string, fetch func(batch.Job) (batch.Result, error)) error {
	return collectSelected(root, candidates(), fetch)
}

// continuation404 只允许已完整封存的资源404之后，另批尝试原先列明的不同URL。
// 挑战、权限、TLS、超时或未知状态不能借此继续同一主机。
func continuation404(root string) error {
	raw, err := os.ReadFile(filepath.Join(root, "archive/pr-first-rules.json"))
	if err != nil {
		return err
	}
	var r batch.Receipt
	if err = json.Unmarshal(raw, &r); err != nil {
		return err
	}
	body, err := os.ReadFile(filepath.Join(root, "archive/pr-first-rules.body"))
	if err != nil {
		return err
	}
	if r.State != "failed" || r.BodyState != "complete" || r.HTTP == nil || r.HTTP.Status != 404 || !r.HTTP.Complete || !r.HTTP.Attempted || !r.HTTP.ResponseReceived || r.HTTP.Failure != "http_status" || r.Evidence.URL != candidates()[0].URL || r.HTTP.FinalURL != candidates()[0].URL || r.Bytes != int64(len(body)) || r.Evidence.Verify(body) != nil {
		return fmt.Errorf("仅完整资源404允许不同URL后续批；挑战/TLS/超时/未知均停止")
	}
	return nil
}

func collectSelected(root string, jobs []batch.Job, fetch func(batch.Job) (batch.Result, error)) error {
	if err := os.Mkdir(root, 0700); err != nil {
		return fmt.Errorf("既有/未知目录禁止重取: %w", err)
	}
	plan, _ := json.MarshalIndent(map[string]any{"jobs": jobs, "max_actual_requests": len(jobs), "max_actual_bytes_per_request": 12 << 20, "timeout_seconds": 15, "discovery_http_literal": "http://www.czce.com.cn/cn/gyjys/jysdt/ggytz/webinfo/2024/12/1726399021155050.htm", "discovery_page": "https://www.zrhxqh.com/cn/about/detail/id/15139.html", "year_end_is_independent_https_candidate": true, "calendar_qualified": false}, "", "  ")
	if err := os.WriteFile(filepath.Join(root, "plan.json"), append(plan, '\n'), 0600); err != nil {
		return err
	}
	for _, job := range jobs {
		result, err := fetch(job)
		if err != nil {
			return err
		}
		if result.Receipt.State != "received" {
			return fmt.Errorf("候选失败，本批停止；不重试当前候选")
		}
	}
	return nil
}

func main() {
	root := flag.String("root", "", "必须全新采集目录")
	yearOnly := flag.Bool("year-end-only", false, "资源404后另批仅尝试原列明的年界候选")
	prior := flag.String("prior-root", "", "前批完整404封存目录")
	flag.Parse()
	if *root == "" {
		fmt.Fprintln(os.Stderr, "需要root")
		os.Exit(1)
	}
	var last time.Time
	jobs := candidates()
	if *yearOnly {
		if err := continuation404(*prior); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		jobs = jobs[1:]
	}
	err := collectSelected(*root, jobs, func(job batch.Job) (batch.Result, error) {
		if wait := time.Until(last.Add(time.Second)); !last.IsZero() && wait > 0 {
			time.Sleep(wait)
		}
		transport := &http.Transport{DialContext: (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: -1}).DialContext, DisableKeepAlives: true, TLSHandshakeTimeout: 10 * time.Second}
		transport.Proxy = func(r *http.Request) (*url.URL, error) {
			proxy, err := http.ProxyFromEnvironment(r)
			if err == nil && proxy != nil && proxy.User != nil {
				return nil, fmt.Errorf("不继承代理账户")
			}
			return proxy, err
		}
		downloader, err := batch.NewDownloader(&http.Client{Transport: transport}, batch.Limits{Requests: 1, Bytes: (12 << 20) - 1, Interval: time.Second, Timeout: 15 * time.Second})
		if err != nil {
			return batch.Result{}, err
		}
		last = time.Now()
		results, err := batch.Run(context.Background(), filepath.Join(*root, "archive"), []batch.Job{job}, downloader, 12<<20, validate)
		n, size := downloader.Usage()
		fmt.Printf("%s actual_attempts=%d actual_bytes=%d\n", job.ID, n, size)
		if err != nil {
			return batch.Result{}, err
		}
		if len(results) != 1 {
			return batch.Result{}, fmt.Errorf("候选结果数不符")
		}
		return results[0], nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
