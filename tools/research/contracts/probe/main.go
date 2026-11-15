// Command probe 仅核两个有源码出处的官方HTTPS资源，不切换HTTP、不跟重定向。
package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dream-until-dawn/futures-tickflow-go/archive/batch"
	"github.com/dream-until-dawn/futures-tickflow-go/refdata/evidence"
)

func jobs() []batch.Job {
	return []batch.Job{
		{ID: "czce-20210104-https-official", URL: "https://www.czce.com.cn/cn/DFSStaticFiles/Future/2021/20210104/FutureDataDaily.txt", Exchange: "CZCE", Format: "official-research-text", Day: 20210104, Year: 2021},
		{ID: "cffex-202101-https-official", URL: "https://www.cffex.com.cn/sj/historysj/202101/zip/202101.zip", Exchange: "CFFEX", Format: "official-research-month", Day: 20210104, Year: 2021},
	}
}

func validate(j batch.Job, raw []byte, ref evidence.Ref) error {
	if err := ref.Verify(raw); err != nil {
		return err
	}
	if j.Exchange == "CZCE" {
		if !bytes.Contains(raw, []byte("2021-01-04")) || !bytes.Contains(raw, []byte("|")) {
			return fmt.Errorf("未识别官方候选日期/表格，保留待核")
		}
		return nil // 这里只认原件表形，不声称编码/数值/单位已核。
	}
	z, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return err
	}
	var expanded int64
	found := false
	seen := map[string]bool{}
	for _, file := range z.File {
		name := strings.ReplaceAll(file.Name, "\\", "/")
		if strings.HasPrefix(name, "/") || strings.Contains("/"+name+"/", "/../") || seen[name] {
			return fmt.Errorf("月包成员路径/重复非法")
		}
		seen[name] = true
		if file.UncompressedSize64 > 64<<20 {
			return fmt.Errorf("月包解压预算")
		}
		r, e := file.Open()
		if e != nil {
			return e
		}
		n, e := io.Copy(io.Discard, io.LimitReader(r, (64<<20)-expanded+1))
		r.Close()
		expanded += n
		if e != nil || expanded > 64<<20 {
			return fmt.Errorf("月包CRC/总展开预算: %v", e)
		}
		if filepath.Base(name) == "20210104_1.csv" {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("月包缺少预期样本成员，格式待核")
	}
	return nil
}

func run(root string) error {
	if err := os.Mkdir(root, 0700); err != nil {
		return err
	}
	marker, err := os.OpenFile("C:/Temp/tickflow-contract-directory-20261115-v1/official-https-probe-202101.started.json", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("官方HTTPS两源已有/未知启动，不重发: %w", err)
	}
	err = json.NewEncoder(marker).Encode(map[string]string{"target_root": root})
	marker.Close()
	if err != nil {
		return err
	}
	p, _ := json.MarshalIndent(map[string]any{"jobs": jobs(), "per_host_attempts": 1, "timeout_seconds": 15, "per_response_bytes": 4 << 20, "redirects": false, "native_http": false, "qualification": "research originals only; schema/units/lifecycle pending"}, "", "  ")
	if err := os.WriteFile(filepath.Join(root, "plan.json"), append(p, '\n'), 0600); err != nil {
		return err
	}
	results := []batch.Result{}
	failed := false
	for _, j := range jobs() {
		transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 10 * time.Second}).DialContext, TLSHandshakeTimeout: 10 * time.Second, DisableKeepAlives: true}
		d, e := batch.NewDownloader(&http.Client{Transport: transport}, batch.Limits{Requests: 1, Bytes: (4 << 20) - 1, Interval: time.Second, Timeout: 15 * time.Second})
		if e != nil {
			return e
		}
		r, e := batch.Run(context.Background(), filepath.Join(root, "archive"), []batch.Job{j}, d, 4<<20, validate)
		transport.CloseIdleConnections()
		if e != nil {
			return e
		}
		results = append(results, r...)
		if len(r) != 1 || r[0].Receipt.State != "received" {
			failed = true
		}
		n, size := d.Usage()
		state := "unknown"
		if len(r) == 1 {
			state = r[0].Receipt.State
		}
		fmt.Printf("%s attempts=%d bytes=%d state=%s\n", j.ID, n, size, state)
	}
	b, _ := json.MarshalIndent(results, "", "  ")
	if err := os.WriteFile(filepath.Join(root, "results.json"), append(b, '\n'), 0600); err != nil {
		return err
	}
	if failed {
		return fmt.Errorf("至少一个来源失败/未知；两主机均不自动重试/切协议")
	}
	return nil
}

func main() {
	root := flag.String("out", "", "全新仓外目录")
	nativePrepare := flag.Bool("prepare-native-cffex", false, "仅核原生HTTP候选出处，不联网/写启动标记")
	nativeApproved := flag.Bool("execute-approved-cffex-http", false, "仅用户另行批准后，单次精确中金所HTTP月包；不复用旧授权")
	prepareMonths := flag.Bool("prepare-cffex-months", false, "只预检69个已结束月份的新候选，不联网，旧一次授权不覆盖")
	executeMonths := flag.Bool("execute-approved-cffex-months", false, "仅获得另外69个月份明确授权后启用")
	monthsPlan := flag.String("cffex-months-plan", "docs/evidence/historical-contract-directory-20261115/cffex-remaining-months-candidate.json", "固定SHA的69月候选单")
	flag.Parse()
	modeCount := 0
	for _, b := range []bool{*nativePrepare, *nativeApproved, *prepareMonths, *executeMonths} {
		if b {
			modeCount++
		}
	}
	if modeCount > 1 {
		fmt.Fprintln(os.Stderr, "候选模式不能混用")
		os.Exit(2)
	}
	if *prepareMonths {
		if e := nativePreflight(); e != nil {
			fmt.Fprintln(os.Stderr, e)
			os.Exit(1)
		}
		j, e := remainingMonths(*monthsPlan)
		if e != nil {
			fmt.Fprintln(os.Stderr, e)
			os.Exit(1)
		}
		fmt.Printf("69月候选预检通过：%d项；请求0；仍未获本次授权\n", len(j))
		return
	}
	if *nativePrepare {
		if *nativeApproved {
			fmt.Fprintln(os.Stderr, "预检不能混用执行")
			os.Exit(2)
		}
		if err := nativePreflight(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println("原生HTTP候选预检通过；实际请求0；仍待本次人工许可", nativeCFFEXURL)
		return
	}
	if *root == "" {
		fmt.Fprintln(os.Stderr, "必须给-out")
		os.Exit(2)
	}
	var e error
	if *executeMonths {
		e = runRemainingMonths(*root, *monthsPlan)
	} else if *nativeApproved {
		e = runNative(*root)
	} else {
		e = run(*root)
	}
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
