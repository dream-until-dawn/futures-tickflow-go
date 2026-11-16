// Command notices 仅办理六个冻结公开网页；默认零请求预检，不重试或执行页面脚本。
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
	"os"
	"path/filepath"
	"time"

	"github.com/dream-until-dawn/futures-tickflow-go/archive/batch"
)

const onceMarker = "C:/Temp/tickflow-notices-20261116.started.json"

var targets = []string{
	"https://www.shfe.com.cn/publicnotice/notice/",
	"https://www.shfe.com.cn/publicnotice/notice/202611/t20261116_834084.html",
	"https://www.shfe.com.cn/publicnotice/notice/202611/t20261113_834083.html",
	"https://www.shfe.com.cn/specialtopic/investor/trade/",
	"https://www.shfe.com.cn/services/calenderandholidays/tradinghours/",
	"https://www.shfe.com.cn/services/calenderandholidays/holiday/",
}

type plan struct {
	BinarySHA string          `json:"binary_sha256"`
	DenySHA   string          `json:"denylist_sha256"`
	URLs      []string        `json:"urls"`
	Blocked   map[string]bool `json:"blocked"`
}

type observation struct {
	URL       string         `json:"url"`
	State     string         `json:"state"`
	Error     string         `json:"error,omitempty"`
	HTTP      batch.Download `json:"http"`
	HeadersAt *time.Time     `json:"headers_received_at_utc,omitempty"`
	Finished  time.Time      `json:"finished_at_utc"`
	Bytes     int            `json:"bytes"`
	SHA       string         `json:"sha256,omitempty"`
}

func digest(raw []byte) string { h := sha256.Sum256(raw); return hex.EncodeToString(h[:]) }
func writeJSON(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0600)
}

func makePlan(binary, deny []byte) (plan, error) {
	var blocked struct {
		URLs []string `json:"urls"`
	}
	if err := json.Unmarshal(deny, &blocked); err != nil {
		return plan{}, err
	}
	p := plan{BinarySHA: digest(binary), DenySHA: digest(deny), URLs: targets, Blocked: map[string]bool{}}
	for _, url := range blocked.URLs {
		p.Blocked[url] = true
	}
	return p, nil
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func contentOK(index int, raw []byte) bool {
	if !bytes.Contains(bytes.ToLower(raw), []byte("<html")) {
		return false
	}
	anchors := [][]string{{"公告"}, {"259", "违规"}, {"258", "限制"}, {"交易"}, {"交易时间"}, {"休市"}}
	for _, anchor := range anchors[index] {
		if !bytes.Contains(raw, []byte(anchor)) {
			return false
		}
	}
	return true
}

// run 先核拒绝表；一个实际失败后同主机剩余项不再访问。时间只表示本机事件。
func run(root string, p plan, transport http.RoundTripper, pause func()) ([]observation, error) {
	var results []observation
	stopped := false
	for index, address := range p.URLs {
		item := observation{URL: address, State: "blocked_after_host_failure"}
		if p.Blocked[address] {
			item.State = "blocked_by_previous_evidence"
			results = append(results, item)
			continue
		}
		if stopped {
			results = append(results, item)
			continue
		}
		pause()
		var headersAt *time.Time
		wrapped := roundTrip(func(r *http.Request) (*http.Response, error) {
			response, err := transport.RoundTrip(r)
			if response != nil {
				now := time.Now().UTC()
				headersAt = &now
			}
			return response, err
		})
		downloader, err := batch.NewDownloader(&http.Client{Transport: wrapped}, batch.Limits{Requests: 1, Bytes: (2 << 20) - 1, Interval: time.Second, Timeout: 15 * time.Second})
		if err != nil {
			return results, err
		}
		result, fetchErr := downloader.FetchDetailed(context.Background(), address)
		item.HTTP, item.HeadersAt, item.Finished = result, headersAt, time.Now().UTC()
		item.Bytes, item.SHA = len(result.Body), digest(result.Body)
		if err = os.WriteFile(filepath.Join(root, fmt.Sprintf("%02d.body", index+1)), result.Body, 0600); err != nil {
			return results, err
		}
		item.State = "received_original_scope_unreviewed"
		if fetchErr != nil {
			item.Error = fetchErr.Error()
		} else if !contentOK(index, result.Body) {
			item.Error = "expected business text absent; not a verified document"
		}
		if item.Error != "" {
			item.State, stopped = "failed", true
		}
		results = append(results, item)
		if err = writeJSON(filepath.Join(root, "results.json"), results); err != nil {
			return results, err
		}
	}
	return results, writeJSON(filepath.Join(root, "results.json"), results)
}

func main() {
	root := flag.String("out", "", "仓外新目录，prepare后同目录launch")
	deny := flag.String("denylist", "", "已核旧失败URL清单")
	launch := flag.Bool("launch", false, "执行已预检清单；默认零请求")
	flag.Parse()
	if err := execute(*root, *deny, *launch); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func execute(root, denyPath string, launch bool) error {
	if root == "" || denyPath == "" {
		return fmt.Errorf("必须指定out及denylist")
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	binary, err := os.ReadFile(executable)
	if err != nil {
		return err
	}
	deny, err := os.ReadFile(denyPath)
	if err != nil {
		return err
	}
	p, err := makePlan(binary, deny)
	if err != nil {
		return err
	}
	if !launch {
		if err = os.Mkdir(root, 0700); err != nil {
			return err
		}
		if err = writeJSON(filepath.Join(root, "plan.json"), p); err != nil {
			return err
		}
		fmt.Println("六项预检完成，实际请求0；命中旧失败URL者不会请求")
		return nil
	}
	prepared, err := os.ReadFile(filepath.Join(root, "plan.json"))
	if err != nil {
		return err
	}
	var saved plan
	if err = json.Unmarshal(prepared, &saved); err != nil {
		return err
	}
	actual, _ := json.Marshal(p)
	expected, _ := json.Marshal(saved)
	if !bytes.Equal(actual, expected) {
		return fmt.Errorf("预检二进制/URL/拒绝表身份变化，禁止启动")
	}
	marker, err := os.OpenFile(onceMarker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("已有或未知启动，不得重发: %w", err)
	}
	err = json.NewEncoder(marker).Encode(map[string]string{"root": root, "binary_sha256": p.BinarySHA})
	marker.Close()
	if err != nil {
		return err
	}
	transport := &http.Transport{DialContext: (&net.Dialer{Timeout: 10 * time.Second}).DialContext, TLSHandshakeTimeout: 10 * time.Second, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	results, err := run(root, p, transport, func() { time.Sleep(time.Second) })
	if err != nil {
		return err
	}
	for _, r := range results {
		if r.State != "received_original_scope_unreviewed" {
			return fmt.Errorf("来源不完整，原始失败保留；不得重试")
		}
	}
	return nil
}
