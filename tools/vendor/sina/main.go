// Command sina 分开执行有界原件归档和冻结样本机械回读，不宣称官方覆盖。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	tf "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/archive/batch"
	"github.com/dream-until-dawn/futures-tickflow-go/refdata/evidence"
	"github.com/dream-until-dawn/futures-tickflow-go/source/sinavendor"
)

func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func run() error {
	root := flag.String("root", "", "归档目录或全新离线结果目录")
	plan := flag.String("plan", "", "完整冻结Job清单；仅显式Sina匿名HTTPS")
	samples := flag.String("samples", "", "复用收据文件，同目录须有原样name.jsonp")
	requests := flag.Int("requests", 40, "本批累计请求上限，不得超过40")
	byteLimit := flag.Int64("bytes", 20<<20, "本批累计读取字节上限，不得超过20MiB")
	static := flag.Bool("static-cons", false, "仅取已引用的唯一cons.py静态原件；不是市场请求")
	isolateNull := flag.Bool("isolate-exact-null", false, "显式工具模式：合法null仅记失败隔离；不改变库默认")
	nullProfilePath := flag.String("null-profile", "", "null工具的已核旧封存及175清单配置")
	flag.Parse()
	if (*isolateNull && (*nullProfilePath == "" || *samples != "" || *static)) || (!*isolateNull && *nullProfilePath != "") {
		return fmt.Errorf("null隔离必须显式独立选择工具配置，不能混用其他入口")
	}
	if *root == "" || (*plan == "") == (*samples == "") {
		return fmt.Errorf("必须指定-root及-plan或-samples其一")
	}
	if *samples != "" {
		return reviewSamples(*samples, *root)
	}
	b, e := os.ReadFile(*plan)
	if e != nil {
		return e
	}
	var jobs []batch.Job
	if e = json.Unmarshal(b, &jobs); e != nil {
		return e
	}
	if *isolateNull {
		report, e := runNullIsolation(context.Background(), *root, jobs, batch.Limits{Requests: *requests, Bytes: *byteLimit, Interval: time.Second, Timeout: 15 * time.Second}, *nullProfilePath)
		if report != nil {
			encoder := json.NewEncoder(os.Stdout)
			encoder.SetIndent("", "  ")
			if outputErr := encoder.Encode(report); outputErr != nil {
				return outputErr
			}
		}
		return e
	}
	addresses := make([]string, len(jobs))
	for i, j := range jobs {
		if !*static && j.Format != "vendor-raw-unqualified" {
			return fmt.Errorf("仅归档原件，不接受伪装官方/完整覆盖的format")
		}
		addresses[i] = j.URL
	}
	var d *batch.Downloader
	if *static {
		d, e = staticDownloader(jobs)
	} else {
		d, e = batch.NewSinaVendorDownloader(batch.Limits{Requests: *requests, Bytes: *byteLimit, Interval: time.Second, Timeout: 15 * time.Second}, addresses)
	}
	if e != nil {
		return e
	}
	// 此层只验证确有原件，received只指原字节归档；业务解析另行审核。
	validate := func(j batch.Job, b []byte, r evidence.Ref) error {
		if len(b) == 0 {
			return fmt.Errorf("空响应未知")
		}
		if !*static {
			u, e := url.Parse(j.URL)
			if e != nil {
				return e
			}
			if u.Host == "stock2.finance.sina.com.cn" {
				code := u.Query().Get("symbol")
				if len(code) < 5 {
					return fmt.Errorf("缺真实年月代码")
				}
				sym, e := tf.ParseSymbol(j.Exchange + "." + j.Product + code[len(code)-4:])
				if e != nil {
					return e
				}
				_, e = sinavendor.Parse(sinavendor.Document{Symbol: sym, Evidence: r, Raw: b})
				return e
			}
		}
		return r.Verify(b)
	}
	rr, e := batch.Run(context.Background(), *root, jobs, d, 1<<20, validate)
	if e != nil {
		return e
	}
	n, size := d.Usage()
	out := struct {
		Class    string         `json:"class"`
		Requests int            `json:"requests"`
		Bytes    int64          `json:"bytes"`
		Results  []batch.Result `json:"results"`
	}{"vendor/sina/raw-only-unqualified", n, size, rr}
	if *static {
		out.Class = "static/akshare/source-only"
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if e = enc.Encode(out); e != nil {
		return e
	}
	for _, r := range rr {
		if r.Receipt.State != "received" {
			return fmt.Errorf("存在失败/未知项，参见%s", filepath.Join(*root, r.ID+".json"))
		}
	}
	return nil
}
