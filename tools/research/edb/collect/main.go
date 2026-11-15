// Command collect 仅做已冻结EDB匿名研究样本获取，不登录、不取token、不接生产Source。
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/dream-until-dawn/futures-tickflow-go/archive/batch"
	"github.com/dream-until-dawn/futures-tickflow-go/refdata/evidence"
)

const docSHA = "1491f0e0642c233c14a17b9dc8b02400debffb171c6d27496fb43562c8162a6c"
const baselineSHA = "1fc30d47bf6403b5f9807508d0aec7407b2c4f339d32451dccf1515c08942759"
const docURL = "https://doc.shinnytech.com/edb/latest/md_server.html"

var decimalLiteral = regexp.MustCompile(`^[+]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)

func digest(body []byte) string { sum := sha256.Sum256(body); return hex.EncodeToString(sum[:]) }

// checkEvidence 要求已复核的完整文档及官方合约日源锁，不用当前网页/猜测代码代替。
func checkEvidence(body []byte, receipt batch.Receipt, baseline []byte, expectedDoc, expectedBaseline string) error {
	if digest(body) != expectedDoc || digest(baseline) != expectedBaseline || receipt.Evidence.Verify(body) != nil {
		return fmt.Errorf("文档/官方基线SHA不符")
	}
	h := receipt.HTTP
	if receipt.State != "received" || receipt.BodyState != "complete" || receipt.Bytes != int64(len(body)) || receipt.Evidence.URL != docURL || h == nil || h.Status != 200 || !h.Complete || !h.Attempted || !h.ResponseReceived || h.FinalURL != docURL {
		return fmt.Errorf("文档不是完整200已收原件")
	}
	return nil
}

func requireEvidence(root, baselineFile string) error {
	body, err := os.ReadFile(filepath.Join(root, "archive/edb-public-document.body"))
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(root, "archive/edb-public-document.json"))
	if err != nil {
		return err
	}
	var receipt batch.Receipt
	if err = json.Unmarshal(raw, &receipt); err != nil {
		return err
	}
	baseline, err := os.ReadFile(baselineFile)
	if err != nil {
		return err
	}
	return checkEvidence(body, receipt, baseline, docSHA, baselineSHA)
}

func jobs() []batch.Job {
	var result []batch.Job
	for _, selection := range []struct{ code, exchange, product string }{{"ec2602", "INE", "ec"}, {"br2601", "SHFE", "br"}} {
		for _, period := range []string{"86400", "60"} {
			start := "2025-11-18 09:00:00"
			if selection.product == "br" && period == "60" {
				start = "2025-11-17 21:00:00"
			}
			query := url.Values{"period": {period}, "symbol": {selection.exchange + "." + selection.code}, "start_time": {start}, "end_time": {"2025-11-18 15:00:00"}}
			result = append(result, batch.Job{ID: selection.code + "-" + period, URL: "https://edb.shinnytech.com/md/kline?" + query.Encode(), Format: "edb-csv", Exchange: selection.exchange, Product: selection.product, Day: 20251118, Year: 2025, Version: "shinny-edb-anonymous-research-v1"})
		}
	}
	return result
}

// validateCSV 只核非空、列结构、有限数值和递增纳秒时间，不把received等同价格获证。
func validateCSV(_ batch.Job, body []byte, ref evidence.Ref) error {
	if err := ref.Verify(body); err != nil {
		return err
	}
	r := csv.NewReader(bytes.NewReader(body))
	r.FieldsPerRecord = 8
	rows, err := r.ReadAll()
	if err != nil || len(rows) < 2 {
		return fmt.Errorf("CSV错误或没有样本行: %v", err)
	}
	expected := []string{"datetime_nano", "open", "high", "low", "close", "volume", "open_oi", "close_oi"}
	for i, name := range expected {
		if rows[0][i] != name {
			return fmt.Errorf("CSV表头不符")
		}
	}
	var previous int64
	for _, row := range rows[1:] {
		stamp, err := strconv.ParseInt(row[0], 10, 64)
		if err != nil || stamp <= previous {
			return fmt.Errorf("纳秒时间错误/重复/乱序")
		}
		previous = stamp
		for i, literal := range row[1:] {
			if len(literal) > 128 || !decimalLiteral.MatchString(literal) {
				return fmt.Errorf("数值字面量过长")
			}
			if position := strings.IndexAny(literal, "eE"); position >= 0 {
				exponent, err := strconv.Atoi(literal[position+1:])
				if err != nil || exponent < -308 || exponent > 308 {
					return fmt.Errorf("指数超探针预算")
				}
			}
			// 先拒绝超出有限float范围的指数，避免把异常指数交给精确有理数分配。
			if _, err := strconv.ParseFloat(literal, 64); err != nil {
				return fmt.Errorf("数值超出有限范围")
			}
			number, ok := new(big.Rat).SetString(literal)
			if !ok || number.Sign() < 0 || (i >= 4 && !number.IsInt()) {
				return fmt.Errorf("数值或量仓整数非法")
			}
		}
	}
	return nil
}

type outcome struct {
	ID    string `json:"id"`
	State string `json:"state"`
	Error string `json:"error,omitempty"`
}

func collect(root string, fetch func(batch.Job) (batch.Result, error)) error {
	if err := os.Mkdir(root, 0700); err != nil {
		return fmt.Errorf("既有/未知目录禁止重发: %w", err)
	}
	planned := jobs()
	plan := map[string]any{"jobs": planned, "requests_max": 4, "actual_bytes_total_max": 8 << 20, "actual_bytes_per_response_max": 2 << 20, "timeout_seconds": 15, "interval_seconds": 2, "document_sha256": docSHA, "official_baseline_sha256": baselineSHA, "token_used": false, "financial_eligible": false}
	raw, _ := json.MarshalIndent(plan, "", "  ")
	if err := os.WriteFile(filepath.Join(root, "plan.json"), append(raw, '\n'), 0600); err != nil {
		return err
	}
	results := make([]outcome, len(planned))
	for i, j := range planned {
		results[i] = outcome{ID: j.ID, State: "not_run"}
	}
	var failure error
	for i, j := range planned {
		r, err := fetch(j)
		results[i].State = r.Receipt.State
		if err != nil || r.Receipt.State != "received" {
			results[i].State = "failed"
			results[i].Error = r.Receipt.Error
			if err != nil {
				results[i].Error = err.Error()
			}
			failure = fmt.Errorf("样本失败/未知即停，未尝试项不重发")
			break
		}
	}
	raw, _ = json.MarshalIndent(results, "", "  ")
	if err := os.WriteFile(filepath.Join(root, "results.json"), append(raw, '\n'), 0600); err != nil {
		return err
	}
	return failure
}

func run() error {
	root := flag.String("root", "", "必须全新仓外样本目录")
	docRoot := flag.String("document-root", "", "既有EDB文档归档根")
	baseline := flag.String("official-baseline", "", "已冻结官方对照基线")
	flag.Parse()
	if *root == "" || *docRoot == "" || *baseline == "" {
		return fmt.Errorf("缺少明确root/document-root/official-baseline")
	}
	if err := requireEvidence(*docRoot, *baseline); err != nil {
		return err
	}
	var last time.Time
	return collect(*root, func(j batch.Job) (batch.Result, error) {
		if delay := time.Until(last.Add(2 * time.Second)); !last.IsZero() && delay > 0 {
			time.Sleep(delay)
		}
		transport := &http.Transport{DialContext: (&net.Dialer{Timeout: 15 * time.Second}).DialContext, TLSHandshakeTimeout: 10 * time.Second, DisableKeepAlives: true}
		defer transport.CloseIdleConnections()
		transport.Proxy = func(r *http.Request) (*url.URL, error) {
			p, e := http.ProxyFromEnvironment(r)
			if e == nil && p != nil && p.User != nil {
				return nil, fmt.Errorf("不继承代理账户")
			}
			return p, e
		}
		d, err := batch.NewDownloader(&http.Client{Transport: transport}, batch.Limits{Requests: 1, Bytes: (2 << 20) - 1, Interval: 2 * time.Second, Timeout: 15 * time.Second})
		if err != nil {
			return batch.Result{}, err
		}
		last = time.Now()
		results, err := batch.Run(context.Background(), filepath.Join(*root, "archive"), []batch.Job{j}, d, 2<<20, validateCSV)
		n, size := d.Usage()
		fmt.Printf("%s actual_attempts=%d actual_bytes=%d\n", j.ID, n, size)
		if err != nil {
			return batch.Result{}, err
		}
		if len(results) != 1 {
			return batch.Result{}, fmt.Errorf("结果数不符")
		}
		return results[0], nil
	})
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
