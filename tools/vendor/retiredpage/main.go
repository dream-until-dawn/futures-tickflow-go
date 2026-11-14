// Command retiredpage 只读取冻结SDK明确列举的一个静态页；不执行脚本或跟随链接。
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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

const quoteURL = "https://finance.sina.com.cn/futures/quotes/V2105.shtml"
const originSHA = "cfa10698129f81c67e95a7dfbf9bae7a7d19fa57c5586f5263b826243560ba55"
const receiptSHA = "5778f66ae1e51ba8f3d5a56fc6b6ca51650d97db6559a4cfcb3e123e1751cedc"

func digest(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// prepare 只读原件/原取得收据；构造不会联网，不接受任意URL或月份。
func prepare(sourceDir string) (batch.Job, evidence.Ref, error) {
	body, e := os.ReadFile(filepath.Join(sourceDir, "akshare-source.py.txt"))
	if e != nil {
		return batch.Job{}, evidence.Ref{}, e
	}
	b, e := os.ReadFile(filepath.Join(sourceDir, "source-receipt.json"))
	if e != nil {
		return batch.Job{}, evidence.Ref{}, e
	}
	if digest(body) != originSHA || digest(b) != receiptSHA || !bytes.Contains(body, []byte(quoteURL)) {
		return batch.Job{}, evidence.Ref{}, fmt.Errorf("冻结SDK出处SHA/精确URL字面量不符")
	}
	var receipt struct {
		Name, URL, SHA256 string
		At                time.Time `json:"received_at"`
		Status, Bytes     int
	}
	if e = json.Unmarshal(b, &receipt); e != nil {
		return batch.Job{}, evidence.Ref{}, e
	}
	ref := evidence.Ref{ID: receipt.Name, URL: receipt.URL, SHA256: receipt.SHA256, RetrievedAt: receipt.At}
	if receipt.Status != 200 || receipt.Bytes != len(body) || ref.URL != "https://raw.githubusercontent.com/akfamily/akshare/main/akshare/futures/futures_zh_sina.py" || ref.Verify(body) != nil {
		return batch.Job{}, evidence.Ref{}, fmt.Errorf("SDK原件/原收据身份不符")
	}
	return batch.Job{ID: "v2105-static-page", URL: quoteURL, Format: "static-html-unqualified", Version: "sina-retired-page-v1"}, ref, nil
}

// downloader 使用新匿名client和正常TLS；实际读取含溢出探针不超过1MiB。
func downloader() (*batch.Downloader, error) {
	t := &http.Transport{DialContext: (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: -1}).DialContext, DisableKeepAlives: true, TLSHandshakeTimeout: 10 * time.Second}
	t.Proxy = func(r *http.Request) (*url.URL, error) {
		p, e := http.ProxyFromEnvironment(r)
		if e == nil && p != nil && p.User != nil {
			return nil, fmt.Errorf("静态页不继承代理账户")
		}
		return p, e
	}
	return batch.NewDownloader(&http.Client{Transport: t}, batch.Limits{Requests: 1, Bytes: (1 << 20) - 1, Interval: time.Second, Timeout: 15 * time.Second})
}

func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func run() error {
	source := flag.String("source-dir", "", "冻结SDK正文与原收据目录")
	root := flag.String("root", "", "必须全新的仓库外结果目录")
	flag.Parse()
	if *source == "" || *root == "" {
		return fmt.Errorf("需要source-dir与全新root")
	}
	job, origin, e := prepare(*source)
	if e != nil {
		return e
	}
	if e = os.Mkdir(*root, 0700); e != nil {
		return fmt.Errorf("既有/未知目录拒绝重发: %w", e)
	}
	plan, e := json.MarshalIndent(map[string]any{"job": job, "origin": origin, "requests_limit": 1, "actual_body_limit": 1 << 20, "covered": false, "financial_eligible": false}, "", "  ")
	if e != nil {
		return e
	}
	if e = os.WriteFile(filepath.Join(*root, "plan.json"), append(plan, '\n'), 0600); e != nil {
		return e
	}
	d, e := downloader()
	if e != nil {
		return e
	}
	results, e := batch.Run(context.Background(), filepath.Join(*root, "archive"), []batch.Job{job}, d, 1<<20, func(_ batch.Job, b []byte, ref evidence.Ref) error {
		if len(b) == 0 {
			return fmt.Errorf("空正文未知")
		}
		return ref.Verify(b)
	})
	if e != nil {
		return e
	}
	n, size := d.Usage()
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if e = encoder.Encode(map[string]any{"kind": "static-html-unqualified", "requests": n, "bytes": size, "results": results, "retired_roster_certified": false, "financial_eligible": false}); e != nil {
		return e
	}
	if len(results) != 1 || results[0].Receipt.State != "received" {
		return fmt.Errorf("静态页失败，原响应/诊断保留，不重试")
	}
	return nil
}
