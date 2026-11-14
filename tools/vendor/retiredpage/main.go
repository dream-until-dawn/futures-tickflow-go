// Command retiredpage 只读逐项批准、经冻结原件绑定的单一静态资源；不执行或自动跟随链接。
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
	"regexp"
	"time"

	"github.com/dream-until-dawn/futures-tickflow-go/archive/batch"
	"github.com/dream-until-dawn/futures-tickflow-go/refdata/evidence"
)

const quoteURL = "https://finance.sina.com.cn/futures/quotes/V2105.shtml"
const originSHA = "cfa10698129f81c67e95a7dfbf9bae7a7d19fa57c5586f5263b826243560ba55"
const receiptSHA = "5778f66ae1e51ba8f3d5a56fc6b6ca51650d97db6559a4cfcb3e123e1751cedc"
const quoteSHA = "4978059061a9e985ba3484da3e3b53e1560b5fe414319842fb480ed4565612b3"
const controlURL = "https://stock2.finance.sina.com.cn/futures/page2012/js/control.js"
const modularURL = "https://n.sinaimg.cn/finance/futures/moudler20200421.js"
const configURL = "https://n.sinaimg.cn/finance/futures/pc/src/config20240115.js"

// prepareControl 只认实际HTML的script src，不执行脚本或扩大公共下载器白名单。
func prepareControl(sourceDir string) (batch.Job, evidence.Ref, error) {
	return prepareScript(sourceDir, controlURL, "sina-control-static")
}

// prepareScript 仅接收本命令固定的两条已逐项批准的静态路径，不接受用户URL。
func prepareScript(sourceDir, target, id string) (batch.Job, evidence.Ref, error) {
	body, e := os.ReadFile(filepath.Join(sourceDir, "page.jsonp"))
	if e != nil {
		return batch.Job{}, evidence.Ref{}, e
	}
	b, e := os.ReadFile(filepath.Join(sourceDir, "receipt.json"))
	if e != nil {
		return batch.Job{}, evidence.Ref{}, e
	}
	var r batch.Receipt
	if e = json.Unmarshal(b, &r); e != nil {
		return batch.Job{}, evidence.Ref{}, e
	}
	if target != controlURL && target != modularURL && target != configURL {
		return batch.Job{}, evidence.Ref{}, fmt.Errorf("未批准静态路径")
	}
	src := regexp.MustCompile(`(?i)<script\b[^>]*\bsrc\s*=\s*["']` + regexp.QuoteMeta("//"+target[len("https://"):]) + `["']`)
	if digest(body) != quoteSHA || r.Evidence.URL != quoteURL || r.Evidence.Verify(body) != nil || r.State != "received" || r.BodyState != "complete" || r.Bytes != int64(len(body)) || r.HTTP == nil || r.HTTP.Status != 200 || !r.HTTP.Complete || r.HTTP.FinalURL != quoteURL || !src.Match(body) {
		return batch.Job{}, evidence.Ref{}, fmt.Errorf("原HTML/收据/SHA或准确script src不符")
	}
	return batch.Job{ID: id, URL: target, Format: "static-js-unqualified", Version: "sina-static-v1"}, r.Evidence, nil
}

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
	controlFrom := flag.String("control-from", "", "原HTML/收据目录；显式唯一control.js静态模式")
	modularFrom := flag.String("modular-from", "", "原HTML/收据目录；显式唯一modular静态模式")
	configFrom := flag.String("config-from", "", "原HTML/收据目录；显式唯一config静态模式")
	root := flag.String("root", "", "必须全新的仓库外结果目录")
	flag.Parse()
	modes := 0
	for _, value := range []string{*source, *controlFrom, *modularFrom, *configFrom} {
		if value != "" {
			modes++
		}
	}
	if modes != 1 || *root == "" {
		return fmt.Errorf("需要source-dir或control-from其一与全新root")
	}
	var job batch.Job
	var origin evidence.Ref
	var e error
	if *configFrom != "" {
		job, origin, e = prepareScript(*configFrom, configURL, "sina-config-static")
	} else if *modularFrom != "" {
		job, origin, e = prepareScript(*modularFrom, modularURL, "sina-modular-static")
	} else if *controlFrom != "" {
		job, origin, e = prepareControl(*controlFrom)
	} else {
		job, origin, e = prepare(*source)
	}
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
	if e = encoder.Encode(map[string]any{"kind": job.Format, "requests": n, "bytes": size, "results": results, "retired_roster_certified": false, "financial_eligible": false}); e != nil {
		return e
	}
	if len(results) != 1 || results[0].Receipt.State != "received" {
		return fmt.Errorf("静态页失败，原响应/诊断保留，不重试")
	}
	return nil
}
