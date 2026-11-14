package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	tf "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/archive/batch"
)

const nullProfileVersion = "sina-tool-exact-null-v1"

// nullProfile 是显式工具配置，不参与公共库默认行为或资格判断。
type nullProfile struct {
	Version             string `json:"version"`
	PriorEvidence       string `json:"prior_evidence"`
	PriorManifestSHA    string `json:"prior_manifest_sha256"`
	Unattempted         string `json:"unattempted"`
	UnattemptedSHA      string `json:"unattempted_sha256"`
	ExpectedUnattempted int    `json:"expected_unattempted"`
	ExpectedStopSHA     string `json:"expected_stop_sha256"`
	MaxQuarantined      int    `json:"max_quarantined_per_batch"`
}

type verifiedNullProfile struct {
	Profile nullProfile
	SHA     string
	Allowed map[string]bool
	Symbols map[string]tf.Symbol
}

// decodeNullProfile 拒绝重复/大小写别名/未知配置键，防止审阅看到的政策被后项覆盖。
func decodeNullProfile(b []byte, p *nullProfile) error {
	allowed := map[string]bool{"version": true, "prior_evidence": true, "prior_manifest_sha256": true, "unattempted": true, "unattempted_sha256": true, "expected_unattempted": true, "expected_stop_sha256": true, "max_quarantined_per_batch": true}
	d := json.NewDecoder(bytes.NewReader(b))
	token, e := d.Token()
	if e != nil || token != json.Delim('{') {
		return fmt.Errorf("null配置必须是对象")
	}
	seen := map[string]bool{}
	for d.More() {
		k, e := d.Token()
		if e != nil {
			return e
		}
		key, ok := k.(string)
		if !ok || !allowed[key] || seen[key] {
			return fmt.Errorf("null配置键重复/未知")
		}
		seen[key] = true
		var value json.RawMessage
		if e = d.Decode(&value); e != nil {
			return e
		}
	}
	if _, e = d.Token(); e != nil {
		return e
	}
	if _, e = d.Token(); e != io.EOF {
		return fmt.Errorf("null配置有尾部内容")
	}
	return json.Unmarshal(b, p)
}

func bytesDigest(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func boundedFile(p string) ([]byte, error) {
	f, e := os.Open(p)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, (2<<20)+1))
	if e != nil {
		return nil, e
	}
	if len(b) > 2<<20 {
		return nil, fmt.Errorf("工具配置/证据文件过大")
	}
	return b, nil
}

// loadNullProfile 先逐项核旧封存；任何身份/旧停止不符均在联网之前拒绝。
func loadNullProfile(path string) (*verifiedNullProfile, error) {
	b, e := boundedFile(path)
	if e != nil {
		return nil, e
	}
	v := &verifiedNullProfile{SHA: bytesDigest(b), Allowed: map[string]bool{}, Symbols: map[string]tf.Symbol{}}
	if e = decodeNullProfile(b, &v.Profile); e != nil {
		return nil, e
	}
	p := v.Profile
	if p.Version != nullProfileVersion || p.MaxQuarantined < 1 || p.MaxQuarantined > 40 || p.ExpectedUnattempted < 1 || p.ExpectedUnattempted > 5000 {
		return nil, fmt.Errorf("null隔离配置版本/上限不符")
	}
	resolve := func(s string) string {
		if filepath.IsAbs(s) {
			return s
		}
		return filepath.Join(filepath.Dir(path), s)
	}
	prior, e := filepath.EvalSymlinks(resolve(p.PriorEvidence))
	if e != nil {
		return nil, e
	}
	prior, e = filepath.Abs(prior)
	if e != nil {
		return nil, e
	}
	manifest, e := boundedFile(filepath.Join(prior, "files.json"))
	if e != nil {
		return nil, e
	}
	if bytesDigest(manifest) != p.PriorManifestSHA {
		return nil, fmt.Errorf("旧封存清单SHA不符")
	}
	var files []struct {
		File   string
		Bytes  int
		SHA256 string
	}
	if e = json.Unmarshal(manifest, &files); e != nil {
		return nil, e
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("旧封存清单空")
	}
	listed := map[string]bool{}
	for _, f := range files {
		if !filepath.IsLocal(f.File) || listed[f.File] {
			return nil, fmt.Errorf("旧清单路径越界/重复")
		}
		listed[f.File] = true
		resolved, e := filepath.EvalSymlinks(filepath.Join(prior, filepath.FromSlash(f.File)))
		if e != nil {
			return nil, e
		}
		rel, e := filepath.Rel(prior, resolved)
		if e != nil || !filepath.IsLocal(rel) {
			return nil, fmt.Errorf("旧清单链接越界")
		}
		raw, e := boundedFile(resolved)
		if e != nil {
			return nil, e
		}
		if len(raw) != f.Bytes || bytesDigest(raw) != f.SHA256 {
			return nil, fmt.Errorf("旧证据SHA/长度不符 %s", f.File)
		}
	}
	for _, required := range []string{"state.json", "request-ledger.json", "current-coverage.json", "raw/daily-A2711.jsonp"} {
		if !listed[required] {
			return nil, fmt.Errorf("旧清单漏关键证据")
		}
	}
	stateRaw, e := boundedFile(filepath.Join(prior, "state.json"))
	if e != nil {
		return nil, e
	}
	var state struct {
		State   string
		Stopped bool `json:"future_market_batches_stopped"`
	}
	if e = json.Unmarshal(stateRaw, &state); e != nil {
		return nil, e
	}
	if state.State != "blocked-with-diagnostics" || !state.Stopped {
		return nil, fmt.Errorf("原stop不符，不能借新工具恢复旧批")
	}
	stop, e := boundedFile(filepath.Join(prior, "raw", "daily-A2711.jsonp"))
	if e != nil {
		return nil, e
	}
	if bytesDigest(stop) != p.ExpectedStopSHA {
		return nil, fmt.Errorf("原失败原件SHA不符")
	}
	ledgerRaw, e := boundedFile(filepath.Join(prior, "request-ledger.json"))
	if e != nil {
		return nil, e
	}
	var ledger []struct {
		ID, URL, Kind string
		Attempted     bool
	}
	if e = json.Unmarshal(ledgerRaw, &ledger); e != nil {
		return nil, e
	}
	for _, r := range ledger {
		if strings.HasPrefix(r.Kind, "vendor/") && !r.Attempted {
			if v.Allowed[r.URL] {
				return nil, fmt.Errorf("旧未尝试URL重复")
			}
			v.Allowed[r.URL] = true
		}
	}
	if len(v.Allowed) != p.ExpectedUnattempted {
		return nil, fmt.Errorf("未尝试集合数量不符")
	}
	allowedRaw, e := boundedFile(resolve(p.Unattempted))
	if e != nil {
		return nil, e
	}
	if bytesDigest(allowedRaw) != p.UnattemptedSHA {
		return nil, fmt.Errorf("175未尝试清单SHA不符")
	}
	var allowed []struct {
		URL       string
		Attempted bool
	}
	if e = json.Unmarshal(allowedRaw, &allowed); e != nil {
		return nil, e
	}
	if len(allowed) != len(v.Allowed) {
		return nil, fmt.Errorf("未尝试清单集合不符")
	}
	seen := map[string]bool{}
	for _, r := range allowed {
		if r.Attempted || !v.Allowed[r.URL] || seen[r.URL] {
			return nil, fmt.Errorf("未尝试清单包含旧请求/重复")
		}
		seen[r.URL] = true
	}
	matrixRaw, e := boundedFile(filepath.Join(prior, "current-coverage.json"))
	if e != nil {
		return nil, e
	}
	var matrix []struct{ Code, Exchange string }
	if e = json.Unmarshal(matrixRaw, &matrix); e != nil {
		return nil, e
	}
	for _, r := range matrix {
		sym, e := tf.ParseSymbol(r.Exchange + "." + strings.ToLower(r.Code))
		if e != nil {
			return nil, e
		}
		address := "https://stock2.finance.sina.com.cn/futures/api/jsonp.php/var%20_V21052021_4_12=/InnerFuturesNewService.getDailyKLine?symbol=" + r.Code + "&type=2021_04_12"
		if _, exists := v.Symbols[address]; exists {
			return nil, fmt.Errorf("旧目录代码重复")
		}
		v.Symbols[address] = sym
	}
	return v, nil
}

func validateNullJobs(jobs []batch.Job, l batch.Limits, p *verifiedNullProfile) ([]string, error) {
	if len(jobs) == 0 || len(jobs) > 40 || l.Requests != len(jobs) || l.Bytes <= 0 || l.Bytes > 3<<20 {
		return nil, fmt.Errorf("显式null工具仅精确≤40请求/3MiB批次")
	}
	var addresses []string
	seen := map[string]bool{}
	for _, j := range jobs {
		if !p.Allowed[j.URL] {
			return nil, fmt.Errorf("请求不在175未尝试清单，旧请求一律不重发")
		}
		if seen[j.URL] {
			return nil, fmt.Errorf("新清单重复URL")
		}
		seen[j.URL] = true
		sym, ok := p.Symbols[j.URL]
		if !ok {
			return nil, fmt.Errorf("未取得声明合约/市场映射")
		}
		u, e := url.Parse(j.URL)
		if e != nil {
			return nil, e
		}
		code := u.Query().Get("symbol")
		if j.ID != "daily-"+code || j.Exchange != sym.Exchange || j.Product != sym.Product || j.Local != "" || j.Blocked != "" || j.Version != batch.SinaVendorVersion || j.Format != "vendor-raw-unqualified" {
			return nil, fmt.Errorf("精确计划身份不符")
		}
		addresses = append(addresses, j.URL)
	}
	return addresses, nil
}
