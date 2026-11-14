// Command sina 分开执行有界原件归档和冻结样本机械回读，不宣称官方覆盖。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/dream-until-dawn/futures-tickflow-go/archive/batch"
	"github.com/dream-until-dawn/futures-tickflow-go/refdata/evidence"
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
	flag.Parse()
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
	addresses := make([]string, len(jobs))
	for i, j := range jobs {
		if j.Format != "vendor-raw-unqualified" {
			return fmt.Errorf("仅归档原件，不接受伪装官方/完整覆盖的format")
		}
		addresses[i] = j.URL
	}
	d, e := batch.NewSinaVendorDownloader(batch.Limits{Requests: 40, Bytes: 20 << 20, Interval: time.Second, Timeout: 15 * time.Second}, addresses)
	if e != nil {
		return e
	}
	// 此层只验证确有原件，received只指原字节归档；业务解析另行审核。
	validate := func(_ batch.Job, b []byte, r evidence.Ref) error {
		if len(b) == 0 {
			return fmt.Errorf("空响应未知")
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
