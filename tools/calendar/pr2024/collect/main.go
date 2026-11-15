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
	if err := os.Mkdir(root, 0700); err != nil {
		return fmt.Errorf("既有/未知目录禁止重取: %w", err)
	}
	plan, _ := json.MarshalIndent(map[string]any{"jobs": candidates(), "max_actual_requests": 2, "max_actual_bytes_per_request": 12 << 20, "timeout_seconds": 15, "discovery_http_literal": "http://www.czce.com.cn/cn/gyjys/jysdt/ggytz/webinfo/2024/12/1726399021155050.htm", "discovery_page": "https://www.zrhxqh.com/cn/about/detail/id/15139.html", "year_end_is_independent_https_candidate": true, "calendar_qualified": false}, "", "  ")
	if err := os.WriteFile(filepath.Join(root, "plan.json"), append(plan, '\n'), 0600); err != nil {
		return err
	}
	for _, job := range candidates() {
		result, err := fetch(job)
		if err != nil {
			return err
		}
		if result.Receipt.State != "received" {
			return fmt.Errorf("候选失败，停止相应主机；不换路径或重试")
		}
	}
	return nil
}

func main() {
	root := flag.String("root", "", "必须全新采集目录")
	flag.Parse()
	if *root == "" {
		fmt.Fprintln(os.Stderr, "需要root")
		os.Exit(1)
	}
	var last time.Time
	err := collect(*root, func(job batch.Job) (batch.Result, error) {
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
