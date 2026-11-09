// archive-batch 执行显式冻结的原件归档，默认无网络；不创建Feed或交易任务。
package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/dream-until-dawn/futures-tickflow-go/archive/ingest"
	"os"
)

func main() {
	plan := flag.String("plan", "", "冻结JSON清单")
	out := flag.String("out", "", "仓库外归档目录")
	network := flag.Bool("network", false, "允许按清单预算访问官方HTTPS；默认离线")
	flag.Parse()
	if *plan == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "必须给-plan和-out")
		os.Exit(2)
	}
	raw, e := os.ReadFile(*plan)
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(2)
	}
	r, e := ingest.Run(context.Background(), raw, *out, *network)
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
	fmt.Printf("%s；本次HTTP尝试%d，读取正文%d字节；报告%s/report.json\n", r.Status, r.Requests, r.Bytes, *out)
	if r.Status != "received_all_planned_originals" {
		os.Exit(1)
	}
}
