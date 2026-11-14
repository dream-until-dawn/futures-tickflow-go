package batch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// SinaVendorVersion 是显式供应商身份；成功响应不表示官方或完整目录。
const SinaVendorVersion = "sina-vendor-v1"

// SinaProductMapURL 来自已冻结AKShare静态源码，仅为产品节点观察。
const SinaProductMapURL = "https://vip.stock.finance.sina.com.cn/quotes_service/view/js/qihuohangqing.js"

type sinaPolicy struct {
	allowed, attempted map[string]bool
	stopped            map[string]bool
	restored           bool
}

// NewSinaVendorDownloader 构造匿名正常TLS精确清单；不借用账户、重试或重定向。
// 调用方须保留完整批次清单和同一归档目录；分阶段恢复会累计旧收据预算与停主机状态。
func NewSinaVendorDownloader(l Limits, addresses []string) (*Downloader, error) {
	if l.Requests > 40 || l.Bytes > 20<<20 || l.Timeout > 15*time.Second || l.Interval < time.Second || len(addresses) == 0 || len(addresses) > 40 {
		return nil, fmt.Errorf("batch: Sina预算/清单超出批准")
	}
	p := &sinaPolicy{allowed: map[string]bool{}, attempted: map[string]bool{}, stopped: map[string]bool{}}
	identities := map[string]bool{}
	for _, a := range addresses {
		if !allowedSinaURL(a) || p.allowed[a] || identities[sinaRequestIdentity(a)] {
			return nil, fmt.Errorf("batch: Sina非批准服务或重复精确URL")
		}
		p.allowed[a] = true
		identities[sinaRequestIdentity(a)] = true
	}
	t := &http.Transport{DialContext: (&net.Dialer{Timeout: l.Timeout, KeepAlive: -1}).DialContext, DisableKeepAlives: true, TLSHandshakeTimeout: 10 * time.Second}
	t.Proxy = func(r *http.Request) (*url.URL, error) {
		p, e := http.ProxyFromEnvironment(r)
		if e == nil && p != nil && p.User != nil {
			return nil, fmt.Errorf("batch: Sina不继承代理账户")
		}
		return p, e
	}
	d, e := NewDownloader(&http.Client{Transport: t}, l)
	if e != nil {
		return nil, e
	}
	d.sina = p
	d.maxResponseBytes = 1 << 20
	return d, nil
}

// 相同业务请求换callback或查询参数顺序也不能变成一次新的授权尝试。
func sinaRequestIdentity(a string) string {
	u, e := url.Parse(a)
	if e != nil {
		return a
	}
	q := u.Query()
	if u.Host == "stock2.finance.sina.com.cn" {
		return u.Host + "/daily/" + q.Get("symbol")
	}
	return u.Host + u.Path + "?" + q.Encode()
}

var sinaNode = regexp.MustCompile(`^[a-z][a-z0-9_]{0,47}$`)
var sinaCode = regexp.MustCompile(`^[A-Z]{1,8}[0-9]{4}$`)
var sinaDailyPath = regexp.MustCompile(`^/futures/api/jsonp.php/var [A-Za-z_$][A-Za-z0-9_$]{0,63}=/InnerFuturesNewService.getDailyKLine$`)

func allowedSinaURL(a string) bool {
	u, e := url.Parse(a)
	if e != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" || u.Opaque != "" || u.ForceQuery || strings.Contains(a, "\\") {
		return false
	}
	if a == SinaProductMapURL {
		return true
	}
	q, e := url.ParseQuery(u.RawQuery)
	if e != nil {
		return false
	}
	for _, v := range q {
		if len(v) != 1 {
			return false
		}
	}
	if u.Host == "vip.stock.finance.sina.com.cn" && u.Path == "/quotes_service/api/json_v2.php/Market_Center.getHQFuturesData" {
		return len(q) == 5 && q.Get("page") == "1" && q.Get("sort") == "position" && q.Get("asc") == "0" && q.Get("base") == "futures" && sinaNode.MatchString(q.Get("node"))
	}
	return u.Host == "stock2.finance.sina.com.cn" && sinaDailyPath.MatchString(u.Path) && len(q) == 2 && sinaCode.MatchString(q.Get("symbol")) && q.Get("type") == "2021_04_12"
}

func (d *Downloader) beforeSina(a string) error {
	u, _ := url.Parse(a)
	if !d.sina.allowed[a] || u == nil || d.sina.stopped[u.Host] || d.sina.attempted[a] {
		return fmt.Errorf("batch: Sina未授权/停主机/既有尝试，禁止请求")
	}
	return nil
}

func sinaChallenge(b []byte) bool {
	b = bytes.TrimSpace(bytes.TrimPrefix(bytes.TrimSpace(b), []byte(`/*<script>location.href='//sina.com';</script>*/`)))
	lower := bytes.ToLower(b)
	for _, s := range []string{"<html", "<!doctype", "<script", "captcha", "verify you are human", "document.cookie"} {
		if bytes.Contains(lower, []byte(s)) {
			return true
		}
	}
	return false
}

func (d *Downloader) afterSina(a string, r *Download) {
	if (r.Attempted && (r.Failure != "" || r.Status != 200 || !r.Complete || len(r.Body) == 0)) || sinaChallenge(r.Body) {
		u, _ := url.Parse(a)
		d.sina.stopped[u.Host] = true
	}
}

func (d *Downloader) failSinaValidation(a string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if u, e := url.Parse(a); e == nil {
		d.sina.stopped[u.Host] = true
	}
}

func validSinaMetadata(a string, r *Download) bool {
	return r != nil && r.DataClass == SinaVendorVersion && r.Unauthenticated && (!r.ResponseReceived || r.FinalURL == a)
}

// restoreSina 先读全批次旧收据，避免先处理新项绕过后列的失败主机；未知启动整批停止。
func (d *Downloader) restoreSina(root string, jobs []Job) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.sina.restored {
		return fmt.Errorf("batch: Sina实例只运行一个完整批次")
	}
	d.sina.restored = true
	entries, e := os.ReadDir(root)
	if e != nil {
		return e
	}
	known := map[string]Job{}
	for _, j := range jobs {
		known[j.ID] = j
	}
	for _, f := range entries {
		if !strings.HasSuffix(f.Name(), ".json") {
			continue
		}
		j, ok := known[strings.TrimSuffix(f.Name(), ".json")]
		if !ok {
			return fmt.Errorf("batch: Sina旧收据未列入完整清单")
		}
		b, e := os.ReadFile(filepath.Join(root, f.Name()))
		if e != nil {
			return e
		}
		var r Receipt
		if e = json.Unmarshal(b, &r); e != nil {
			return e
		}
		jb, _ := json.Marshal(j)
		if r.JobSHA != digest(jb) {
			return fmt.Errorf("batch: Sina冻结清单变更")
		}
		if r.State == "in_progress" {
			return fmt.Errorf("batch: Sina未知旧启动，禁止新请求")
		}
		if (r.State != "received" && r.State != "failed" && r.State != "blocked") || (r.BodyState != "none" && r.BodyState != "partial" && r.BodyState != "complete") || r.Bytes < 0 {
			return fmt.Errorf("batch: Sina旧收据状态/长度未知")
		}
		if r.HTTP == nil {
			if r.State == "blocked" && j.Blocked != "" && r.BodyState == "none" && r.Bytes == 0 {
				continue
			}
			return fmt.Errorf("batch: Sina旧收据缺HTTP诊断")
		}
		if !validSinaMetadata(j.URL, r.HTTP) {
			return fmt.Errorf("batch: Sina旧收据供应商身份不符")
		}
		if (r.HTTP.ResponseReceived && !r.HTTP.Attempted) || (r.HTTP.Attempted && r.HTTP.RequestedAt.IsZero()) || (r.BodyState == "none" && (r.Bytes != 0 || r.HTTP.ResponseReceived)) {
			return fmt.Errorf("batch: Sina旧收据尝试/正文身份不符")
		}
		if r.State == "received" && (r.BodyState != "complete" || !r.HTTP.Complete || !r.HTTP.ResponseReceived || r.HTTP.Status != 200) {
			return fmt.Errorf("batch: Sina旧成功收据并非完整200")
		}
		if r.BodyState != "none" && (r.Evidence.ID != j.ID || r.Evidence.URL != j.URL || r.Evidence.Version != j.Version || !r.HTTP.ResponseReceived) {
			return fmt.Errorf("batch: Sina旧正文与冻结清单身份不符")
		}
		if r.HTTP.Attempted {
			d.requests++
			d.bytes += r.Bytes
			d.sina.attempted[j.URL] = true
			if r.State == "failed" {
				u, _ := url.Parse(j.URL)
				d.sina.stopped[u.Host] = true
			}
			if r.HTTP.RequestedAt.After(d.last) {
				d.last = r.HTTP.RequestedAt
			}
		}
		if r.BodyState != "none" {
			raw, e := readBounded(filepath.Join(root, j.ID+".body"), (1<<20)+1)
			if e != nil {
				return e
			}
			if int64(len(raw)) != r.Bytes || r.Evidence.Verify(raw) != nil {
				return fmt.Errorf("batch: Sina旧原件不符")
			}
			h := *r.HTTP
			h.Body = raw
			d.afterSina(j.URL, &h)
		} else {
			d.afterSina(j.URL, r.HTTP)
		}
	}
	return nil
}
