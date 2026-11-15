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

func dailyAnchorJob() batch.Job {
	q := url.Values{"period": {"86400"}, "symbol": {"INE.ec2602"}, "start_time": {"2025-11-17 00:00:00"}, "end_time": {"2025-11-19 23:59:59"}}
	return batch.Job{ID: "ec2602-daily-anchor-window", URL: "https://edb.shinnytech.com/md/kline?" + q.Encode(), Format: "edb-csv", Exchange: "INE", Product: "ec", Day: 20251118, Year: 2025, Version: "shinny-edb-anonymous-research-v1"}
}

func dailyResearchJobs() []batch.Job {
	var result []batch.Job
	for _, item := range []struct{ exchange, product, code string }{{"DCE", "c", "c2105"}, {"DCE", "m", "m2105"}, {"GFEX", "si", "si2308"}} {
		q := url.Values{"period": {"86400"}, "symbol": {item.exchange + "." + item.code}, "start_time": {"2021-01-01 00:00:00"}, "end_time": {"2026-11-13 23:59:59"}}
		result = append(result, batch.Job{ID: item.code + "-daily-research", URL: "https://edb.shinnytech.com/md/kline?" + q.Encode(), Format: "edb-csv", Exchange: item.exchange, Product: item.product, Year: 2021, Version: "shinny-edb-anonymous-research-v1"})
	}
	return result
}

func requirePriorResearchFiles(root string) error {
	expected := map[string]string{"C2105": "2c56358bde9b12247df8ea3aceb384536bb0243e6daeca5e3c337a63cec63828", "M2105": "1627802176871d7bbf5d71118122dace2eb908c02b7f00e0cd323afa57129186", "SI2308": "c63e409fce5de3b67fc1d3adaeeb9cc915a28fbcfd3509b30ff5f63552b902bb"}
	for name, sha := range expected {
		body, e := os.ReadFile(filepath.Join(root, name+".jsonp"))
		if e != nil {
			return e
		}
		if digest(body) != sha {
			return fmt.Errorf("既有供应商观察原件不符: %s", name)
		}
	}
	return nil
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
	return collectSelected(root, jobs(), fetch)
}

func collectSelected(root string, planned []batch.Job, fetch func(batch.Job) (batch.Result, error)) error {
	if err := os.Mkdir(root, 0700); err != nil {
		return fmt.Errorf("既有/未知目录禁止重发: %w", err)
	}
	plan := map[string]any{"jobs": planned, "requests_max": len(planned), "original_all_phase_requests_max": 4, "actual_bytes_total_max": int64(len(planned)) * (2 << 20), "actual_bytes_per_response_max": 2 << 20, "timeout_seconds": 15, "interval_seconds": 2, "document_sha256": docSHA, "official_baseline_sha256": baselineSHA, "token_used": false, "financial_eligible": false}
	if len(planned) == 1 && planned[0].ID == dailyAnchorJob().ID {
		delete(plan, "original_all_phase_requests_max")
		plan["experiment"] = "one separate full-date window; original intraday daily requests are not repeated"
	}
	if len(planned) == 3 && planned[0].ID == dailyResearchJobs()[0].ID {
		delete(plan, "original_all_phase_requests_max")
		plan["scope"] = "three previously observed vendor-request codes; not an official historical universe or lifecycle"
		plan["settlement_and_turnover_available"] = false
	}
	if len(planned) > 0 && (strings.HasSuffix(planned[0].ID, "-daily-directory-pilot") || strings.HasSuffix(planned[0].ID, "-daily-official-history") || strings.HasSuffix(planned[0].ID, "-daily-additional-research")) {
		delete(plan, "original_all_phase_requests_max")
		plan["scope"] = "frozen directory and official-row observed contracts; daily only"
		plan["approved_daily_plan_sha256"] = []string{directoryPlanSHA, officialInventoryPlanSHA, officialHistoryPlanSHA, additionalMarketsPlanSHA, cffexObservedPlanSHA, czceObservedPlanSHA}
	}
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

// continuationEmpty 仅允许已完整确认的首个空CSV资源之后取未尝试项；
// HTTP错误、截断、未知状态、改写旧计划或已有后续启动都不能绕过。
func continuationEmpty(root string) error {
	if _, err := os.Stat(filepath.Join(root, "continuation-started.json")); !os.IsNotExist(err) {
		return fmt.Errorf("后续已有/未知启动，不得重试")
	}
	read := func(name string, target any) error {
		raw, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			return err
		}
		return json.Unmarshal(raw, target)
	}
	var plan struct {
		Jobs []batch.Job `json:"jobs"`
	}
	if err := read("plan.json", &plan); err != nil {
		return err
	}
	actual, _ := json.Marshal(plan.Jobs)
	expected, _ := json.Marshal(jobs())
	if !bytes.Equal(actual, expected) {
		return fmt.Errorf("旧计划不符，禁止后续")
	}
	var receipt batch.Receipt
	if err := read("archive/ec2602-86400.json", &receipt); err != nil {
		return err
	}
	body, err := os.ReadFile(filepath.Join(root, "archive/ec2602-86400.body"))
	if err != nil {
		return err
	}
	h := receipt.HTTP
	jobRaw, _ := json.Marshal(jobs()[0])
	if !bytes.Equal(body, []byte("datetime_nano,open,high,low,close,volume,open_oi,close_oi\r\n")) || receipt.Evidence.Verify(body) != nil || receipt.Bytes != int64(len(body)) || receipt.State != "failed" || receipt.BodyState != "complete" || receipt.JobSHA != digest(jobRaw) || receipt.Evidence.URL != jobs()[0].URL || h == nil || h.Status != 200 || !h.Complete || !h.Attempted || !h.ResponseReceived || h.FinalURL != jobs()[0].URL || h.Failure != "" {
		return fmt.Errorf("仅完整200空表头资源允许未尝试项后续，权限/超时/未知均拒绝")
	}
	var results []outcome
	if err := read("results.json", &results); err != nil {
		return err
	}
	entries, err := os.ReadDir(filepath.Join(root, "archive"))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() != "ec2602-86400.json" && entry.Name() != "ec2602-86400.body" {
			return fmt.Errorf("额外旧启动/收据/锁未知，禁止后续")
		}
	}
	if len(results) != 4 || results[0].ID != jobs()[0].ID || results[0].State != "failed" {
		return fmt.Errorf("旧结果未知")
	}
	for i := 1; i < 4; i++ {
		if results[i].ID != jobs()[i].ID || results[i].State != "not_run" {
			return fmt.Errorf("后续已有启动，拒绝重复")
		}
		if _, err := os.Stat(filepath.Join(root, "archive", jobs()[i].ID+".json")); !os.IsNotExist(err) {
			return fmt.Errorf("后续有收据或状态未知")
		}
	}
	return nil
}

// reserveContinuation 在原批次追加一次性启动记录；新目录不能重置已用许可/未知启动。
func reserveContinuation(priorRoot, targetRoot string, selected []batch.Job) error {
	if _, err := os.Stat(targetRoot); !os.IsNotExist(err) {
		return fmt.Errorf("后续输出目录已有或未知")
	}
	f, err := os.OpenFile(filepath.Join(priorRoot, "continuation-started.json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("后续启动记录已存在或未知: %w", err)
	}
	ids := make([]string, len(selected))
	for i, j := range selected {
		ids[i] = j.ID
	}
	raw, _ := json.Marshal(map[string]any{"target_root": targetRoot, "planned_ids": ids, "started_at": time.Now().UTC(), "first_empty_not_retried": true})
	_, err = f.Write(append(raw, '\n'))
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}

// continuationLastBR 只闭合原四项中的最后一分钟项；两日线空资源不重取，
// 已成功EC分钟须是本轮锁定原件，不能把任意错误改成“空”来重开新批。
func continuationLastBR(root string) error {
	return continuationLastBRWithEC(root, "285e3eeda841195e7c458c758223c72c1ee79241db77317f4a94ef236b0ea1cb")
}

func continuationLastBRWithEC(root, expectedEC string) error {
	if _, err := os.Stat(filepath.Join(root, "continuation-started.json")); !os.IsNotExist(err) {
		return fmt.Errorf("最后一项已有/未知启动")
	}
	read := func(name string, target any) error {
		raw, e := os.ReadFile(filepath.Join(root, name))
		if e != nil {
			return e
		}
		return json.Unmarshal(raw, target)
	}
	var plan struct {
		Jobs []batch.Job `json:"jobs"`
	}
	if e := read("plan.json", &plan); e != nil {
		return e
	}
	a, _ := json.Marshal(plan.Jobs)
	b, _ := json.Marshal(jobs()[1:])
	if !bytes.Equal(a, b) {
		return fmt.Errorf("不是原三项后续计划")
	}
	var results []outcome
	if e := read("results.json", &results); e != nil {
		return e
	}
	if len(results) != 3 || results[0].ID != jobs()[1].ID || results[0].State != "received" || results[1].ID != jobs()[2].ID || results[1].State != "failed" || results[2].ID != jobs()[3].ID || results[2].State != "not_run" {
		return fmt.Errorf("旧三项结果未知或最后一项已启动")
	}
	allowed := map[string]bool{}
	for i, j := range jobs()[1:3] {
		allowed[j.ID+".body"], allowed[j.ID+".json"] = true, true
		var r batch.Receipt
		if e := read("archive/"+j.ID+".json", &r); e != nil {
			return e
		}
		body, e := os.ReadFile(filepath.Join(root, "archive", j.ID+".body"))
		if e != nil {
			return e
		}
		jobRaw, _ := json.Marshal(j)
		h := r.HTTP
		if r.Evidence.Verify(body) != nil || r.Evidence.URL != j.URL || r.JobSHA != digest(jobRaw) || r.Bytes != int64(len(body)) || r.BodyState != "complete" || h == nil || h.Status != 200 || !h.Attempted || !h.ResponseReceived || !h.Complete || h.FinalURL != j.URL || h.Failure != "" {
			return fmt.Errorf("原件或完整200身份不符")
		}
		if i == 0 {
			if r.State != "received" || digest(body) != expectedEC || validateCSV(j, body, r.Evidence) != nil {
				return fmt.Errorf("成功EC分钟原件不符")
			}
		} else if r.State != "failed" || !bytes.Equal(body, []byte("datetime_nano,open,high,low,close,volume,open_oi,close_oi\r\n")) {
			return fmt.Errorf("BR不是完整空资源")
		}
	}
	entries, e := os.ReadDir(filepath.Join(root, "archive"))
	if e != nil {
		return e
	}
	for _, entry := range entries {
		if !allowed[entry.Name()] {
			return fmt.Errorf("额外旧启动/锁/收据")
		}
	}
	return nil
}

func run() error {
	root := flag.String("root", "", "必须全新仓外样本目录")
	docRoot := flag.String("document-root", "", "既有EDB文档归档根")
	baseline := flag.String("official-baseline", "", "已冻结官方对照基线")
	priorEmpty := flag.String("prior-empty-root", "", "另批复核完整空表头后，仅取原计划尚未尝试三项")
	anchor := flag.Bool("daily-anchor-experiment", false, "另批仅一条EC完整日期窗，查日线时间锚点；不重发原日内URL")
	research := flag.Bool("daily-research-pilot", false, "日线限定：原C2105/M2105/SI2308观察代码三项研究批，不猜月码")
	priorResearch := flag.String("prior-research-dir", "", "三份旧供应商观察原件目录；仅研究批使用")
	directoryPlanFile := flag.String("daily-directory-plan", "", "固定SHA执行单，目录和官方合约行双重核验")
	prepareOnly := flag.Bool("prepare-only", false, "仅目录日线单离线预检，不创建启动标记或网络请求")
	flag.Parse()
	if *root == "" || *docRoot == "" || *baseline == "" {
		return fmt.Errorf("缺少明确root/document-root/official-baseline")
	}
	if *prepareOnly && *directoryPlanFile == "" {
		return fmt.Errorf("prepare-only必须给目录日线执行单")
	}
	if (*anchor && *priorEmpty != "") || (*research && (*anchor || *priorEmpty != "")) || (*research != (*priorResearch != "")) || (*directoryPlanFile != "" && (*anchor || *research || *priorEmpty != "")) {
		return fmt.Errorf("不能混用独立日期窗和旧计划续取")
	}
	if err := requireEvidence(*docRoot, *baseline); err != nil {
		return err
	}
	selected := jobs()
	if *directoryPlanFile != "" {
		var err error
		selected, err = directoryJobs(*directoryPlanFile)
		if err != nil {
			return err
		}
		if *prepareOnly {
			fmt.Printf("离线预检通过：%d项；真实市场请求0\n", len(selected))
			return nil
		}
		if err = reserveDirectoryPlan(*directoryPlanFile, *root); err != nil {
			return err
		}
	}
	if *research {
		if err := requirePriorResearchFiles(*priorResearch); err != nil {
			return err
		}
		selected = dailyResearchJobs()
	}
	if *anchor {
		selected = []batch.Job{dailyAnchorJob()}
	}
	if *priorEmpty != "" {
		if err := continuationEmpty(*priorEmpty); err == nil {
			selected = selected[1:]
		} else if lastErr := continuationLastBR(*priorEmpty); lastErr == nil {
			selected = selected[3:]
		} else {
			return fmt.Errorf("没有可受控续取的原计划未尝试项: %v / %v", err, lastErr)
		}
		if err := reserveContinuation(*priorEmpty, *root, selected); err != nil {
			return err
		}
	}
	fetch := completionPacedFetch(2*time.Second, func(j batch.Job) (batch.Result, error) {
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
	if len(selected) > 128 {
		return collectChunks(*root, selected, 128, fetch)
	}
	return collectSelected(*root, selected, fetch)
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
