package batch

import (
	"fmt"
	"html"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/dream-until-dawn/futures-tickflow-go/refdata/evidence"
)

// NativeHTTPGrant 逐URL/方法授权明文公开读取。SourceBody必须是已审阅的协会原件；
// SHA只锁定字节，不认证来源。LinkedURL是正文实际列出的业务页，URL只能为该页或同站首页。
// 当前只支持GET和已证实的业务参数页，不预先开放未知POST/查询接口。
type NativeHTTPGrant struct {
	URL        string       `json:"url"`
	Method     string       `json:"method"`
	LinkedURL  string       `json:"linked_url"`
	Source     evidence.Ref `json:"source"`
	SourceBody []byte       `json:"-"`
}

var publicParameterPath = regexp.MustCompile(`^/(dce/channel/list/[0-9]+\.html|gfex/(rjycs|rjscs)/ywcs\.shtml)$`)
var sourceHref = regexp.MustCompile(`(?i)\bhref\s*=\s*["']([^"']+)["']`)
var sourceLiteralHref = regexp.MustCompile(`(?i)\.attr\(\s*["']href["']\s*,\s*["']([^"']+)["']\s*\)`)

func nativeURL(address string) (*url.URL, error) {
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || strings.Contains(address, "\\") || (u.Host != "www.dce.com.cn" && u.Host != "www.gfex.com.cn") {
		return nil, fmt.Errorf("batch: native-http非法公开URL")
	}
	if u.Path != "/" && !publicParameterPath.MatchString(u.Path) {
		return nil, fmt.Errorf("batch: native-http路径未开放")
	}
	if (u.Host == "www.dce.com.cn" && strings.HasPrefix(u.Path, "/gfex/")) || (u.Host == "www.gfex.com.cn" && strings.HasPrefix(u.Path, "/dce/")) {
		return nil, fmt.Errorf("batch: native-http路径与主机不符")
	}
	return u, nil
}

func validateGrant(g NativeHTTPGrant) error {
	u, err := nativeURL(g.URL)
	if err != nil {
		return err
	}
	linked, err := nativeURL(g.LinkedURL)
	if err != nil || linked.Path == "/" || linked.Host != u.Host || (u.Path != "/" && g.URL != g.LinkedURL) || g.Method != http.MethodGet {
		return fmt.Errorf("batch: native-http缺少精确GET/明示业务页授权")
	}
	if g.Source.URL != "https://www.cfachina.org/servicesupport/sspz/" || len(g.SourceBody) > 2<<20 || g.Source.Verify(g.SourceBody) != nil {
		return fmt.Errorf("batch: native-http协会出处或SHA无效")
	}
	found := false
	// 协会原页以.attr("href", "常量URL")设置业务链接；只读常量，不执行脚本。
	links := append(sourceHref.FindAllSubmatch(g.SourceBody, -1), sourceLiteralHref.FindAllSubmatch(g.SourceBody, -1)...)
	for _, match := range links {
		if html.UnescapeString(string(match[1])) == g.LinkedURL {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("batch: native-http原件未列明业务页href")
	}
	return nil
}

// NewNativeHTTPDownloader 是原生明文HTTP的显式候选入口；旧NewDownloader不受影响。
// 只接受逐URL/GET及已核协会出处，预算不能超过本次12请求/16MiB/15秒/最小1秒间隔。
// 自建client不继承调用方Cookie/Authorization；不重试、不重定向、不做HTTPS降级。
func NewNativeHTTPDownloader(l Limits, grants []NativeHTTPGrant) (*Downloader, error) {
	if l.Requests > 12 || l.Bytes > 16<<20 || l.Timeout > 15*time.Second || l.Interval < time.Second || len(grants) == 0 || len(grants) > 12 {
		return nil, fmt.Errorf("batch: native-http超出有界批准或空清单")
	}
	allowed := make(map[string]NativeHTTPGrant, len(grants))
	for _, g := range grants {
		if err := validateGrant(g); err != nil {
			return nil, err
		}
		if _, exists := allowed[g.URL]; exists {
			return nil, fmt.Errorf("batch: native-http重复授权URL")
		}
		g.SourceBody = nil // 核字后只留身份，不持有或共享可变正文。
		allowed[g.URL] = g
	}
	// 不借用可被调用方注入账户头的client/transport。系统网络设置不作任何修改。
	transport := &http.Transport{
		DialContext:         (&net.Dialer{Timeout: l.Timeout, KeepAlive: -1}).DialContext,
		DisableKeepAlives:   true,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	transport.Proxy = func(r *http.Request) (*url.URL, error) {
		proxy, err := http.ProxyFromEnvironment(r)
		if err == nil && proxy != nil && proxy.User != nil {
			return nil, fmt.Errorf("batch: native-http不继承代理账户凭据")
		}
		return proxy, err
	}
	d, err := NewDownloader(&http.Client{Transport: transport}, l)
	if err != nil {
		return nil, err
	}
	d.nativeHTTP = allowed
	d.maxResponseBytes = 2 << 20
	return d, nil
}

func (d *Downloader) nativeGrant(address string) (NativeHTTPGrant, bool) {
	g, ok := d.nativeHTTP[address]
	return g, ok && g.Method == http.MethodGet
}

func (d *Downloader) markNative(result *Download, address string) {
	if g, ok := d.nativeGrant(address); ok {
		result.Transport = "native-http"
		result.Unauthenticated = true
		result.SourceURL = g.Source.URL
		result.SourceSHA = g.Source.SHA256
	}
}

// validateNativeReceipt在恢复前重新检查明文身份；不能把HTTP原件洗成HTTPS或无条件认证。
func validateNativeReceipt(d *Downloader, address string, metadata *Download) error {
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "http" || u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1" {
		return nil // 旧HTTPS和本机测试收据不增加字段要求。
	}
	if d == nil {
		return fmt.Errorf("batch: native-http恢复缺显式授权")
	}
	g, ok := d.nativeGrant(address)
	if !ok || metadata == nil || metadata.Transport != "native-http" || !metadata.Unauthenticated || metadata.SourceURL != g.Source.URL || metadata.SourceSHA != g.Source.SHA256 || metadata.FinalURL != address {
		return fmt.Errorf("batch: native-http收据明文标签或出处不符")
	}
	return nil
}
