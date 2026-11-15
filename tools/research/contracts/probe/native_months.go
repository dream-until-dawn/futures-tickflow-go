package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const remainingMonthsSHA = "849ed0d68e21aa34fd36fc8f266ca42e72bb54e5b72956fe0d8762813da4053b"

var nativeMonthURL = regexp.MustCompile(`^http://www\.cffex\.com\.cn/sj/historysj/([0-9]{6})/zip/([0-9]{6})\.zip$`)

func nativeMonth(address string) (string, error) {
	m := nativeMonthURL.FindStringSubmatch(address)
	if m == nil || m[1] != m[2] || m[1] < "202101" || m[1] > "202610" {
		return "", fmt.Errorf("原生HTTP不在精确月份候选范围")
	}
	if _, e := time.Parse("200601", m[1]); e != nil {
		return "", e
	}
	return m[1], nil
}

func validateNativeMonth(raw []byte, month string) error {
	z, e := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if e != nil {
		return e
	}
	seen := map[string]bool{}
	var total int64
	found := false
	for _, f := range z.File {
		name := strings.ReplaceAll(f.Name, "\\", "/")
		if seen[name] || strings.HasPrefix(name, "/") || strings.Contains("/"+name+"/", "/../") {
			return fmt.Errorf("原生月包路径/重复非法")
		}
		seen[name] = true
		if f.UncompressedSize64 > 64<<20 {
			return fmt.Errorf("原生月包解压预算")
		}
		r, e := f.Open()
		if e != nil {
			return e
		}
		n, e := io.Copy(io.Discard, io.LimitReader(r, (64<<20)-total+1))
		r.Close()
		total += n
		if e != nil || total > 64<<20 {
			return fmt.Errorf("原生月包CRC/展开预算: %v", e)
		}
		if regexp.MustCompile(`^` + month + `[0-9]{2}_1\.csv$`).MatchString(filepath.Base(name)) {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("原生月包没有目标月份CSV")
	}
	return nil
}

type monthJob struct {
	Month string `json:"month"`
	URL   string `json:"url"`
}

func remainingMonths(planFile string) ([]monthJob, error) {
	b, e := os.ReadFile(planFile)
	if e != nil {
		return nil, e
	}
	sum := sha256.Sum256(b)
	if hex.EncodeToString(sum[:]) != remainingMonthsSHA {
		return nil, fmt.Errorf("69月候选单SHA不符")
	}
	var p struct {
		Jobs []monthJob `json:"jobs"`
	}
	if e = json.Unmarshal(b, &p); e != nil {
		return nil, e
	}
	if len(p.Jobs) != 69 {
		return nil, fmt.Errorf("月份计数不符")
	}
	seen := map[string]bool{}
	for _, j := range p.Jobs {
		month, e := nativeMonth(j.URL)
		if e != nil || month != j.Month || month == "202101" || seen[month] {
			return nil, fmt.Errorf("月份清单越界/重复/重发已耗尽月份")
		}
		seen[month] = true
	}
	return p.Jobs, nil
}

// 仅准备好候选，不推导授权；主入口必须由用户新的明确许可后显式启用。
func runRemainingMonths(root, planFile string) error {
	if e := nativePreflight(); e != nil {
		return e
	}
	jobs, e := remainingMonths(planFile)
	if e != nil {
		return e
	}
	if e = os.Mkdir(root, 0700); e != nil {
		return e
	}
	marker, e := os.OpenFile("C:/Temp/tickflow-cffex-remaining-months-20261115.started.json", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	e = json.NewEncoder(marker).Encode(map[string]string{"target_root": root, "plan_sha256": remainingMonthsSHA})
	marker.Close()
	if e != nil {
		return e
	}
	states := make([]map[string]string, len(jobs))
	for i, j := range jobs {
		states[i] = map[string]string{"month": j.Month, "state": "not_run"}
	}
	checkpoint := func() error {
		b, e := json.MarshalIndent(states, "", "  ")
		if e != nil {
			return e
		}
		return os.WriteFile(filepath.Join(root, "results.json"), append(b, '\n'), 0600)
	}
	if e = checkpoint(); e != nil {
		return e
	}
	transport := &http.Transport{Proxy: nil, DisableCompression: true, DisableKeepAlives: true, DialContext: (&net.Dialer{Timeout: 10 * time.Second}).DialContext}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	var last time.Time
	for i, j := range jobs {
		if delay := time.Until(last.Add(2 * time.Second)); !last.IsZero() && delay > 0 {
			time.Sleep(delay)
		}
		states[i]["state"] = "running"
		if e = checkpoint(); e != nil {
			return e
		}
		e = captureNativeFor(filepath.Join(root, j.Month), filepath.Join(root, j.Month+".started.json"), client, j.URL)
		// 以上次请求和写盘完成为锚点；启动前写盘开销不再侵蚀下一次请求间隔。
		last = time.Now()
		if e != nil {
			states[i]["state"], states[i]["error"] = "failed", e.Error()
			if ce := checkpoint(); ce != nil {
				return ce
			}
			return e
		}
		states[i]["state"] = "completed"
		if e = checkpoint(); e != nil {
			return e
		}
	}
	return nil
}
