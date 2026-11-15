package batch

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"time"

	"github.com/dream-until-dawn/futures-tickflow-go/refdata/evidence"
)

const sinaStaticHTTPURL = "http://finance.sina.com.cn/iframe/futures_info_cff.js"
const sinaStaticSourceURL = "https://vip.stock.finance.sina.com.cn/q/view/vFutures_History.php"
const sinaStaticSourceSHA = "81cf741fad8436bb9477a7884597ea5c37ea40ae14a8527e6c0fc65267e3fa28"

// SinaStaticHTTPVersion 是明文供应商静态资料身份，不认证发行者、行情或历史覆盖。
const SinaStaticHTTPVersion = "vendor/cleartext/static-unqualified"

// SinaStaticHTTPGrant 仅授权已冻结历史HTML实际引用的一个GET，不复用CFA例外。
// 构造新实例不代表得到再次请求许可；一次性执行标记应由调用工具持久化。
type SinaStaticHTTPGrant struct {
	URL        string
	Method     string
	Source     evidence.Ref
	SourceBody []byte
}

func validateSinaStaticHTTPGrant(g SinaStaticHTTPGrant) error {
	if g.URL != sinaStaticHTTPURL || g.Method != http.MethodGet {
		return fmt.Errorf("batch: Sina静态HTTP缺精确单URL/GET授权")
	}
	if g.Source.URL != sinaStaticSourceURL || g.Source.SHA256 != sinaStaticSourceSHA || len(g.SourceBody) > 1<<20 || g.Source.Verify(g.SourceBody) != nil {
		return fmt.Errorf("batch: Sina静态HTTP出处身份/SHA不符")
	}
	src := regexp.MustCompile(`(?i)<script\b[^>]*\bsrc\s*=\s*["']` + regexp.QuoteMeta(sinaStaticHTTPURL) + `["']`)
	if !src.Match(g.SourceBody) {
		return fmt.Errorf("batch: Sina静态HTTP原件缺真实src")
	}
	return nil
}

// NewSinaStaticHTTPDownloader 是逐项获批的显式入口；旧默认及CFA入口保持原约束。
// 一次GET且不重试；预留一字节溢出探针，使实际读取总量也不超过1MiB。
func NewSinaStaticHTTPDownloader(l Limits, g SinaStaticHTTPGrant) (*Downloader, error) {
	if l.Requests != 1 || l.Bytes <= 0 || l.Bytes > (1<<20)-1 || l.Timeout > 15*time.Second || l.Interval < time.Second {
		return nil, fmt.Errorf("batch: Sina静态HTTP超出一次/1MiB/15秒批准")
	}
	if err := validateSinaStaticHTTPGrant(g); err != nil {
		return nil, err
	}
	transport := &http.Transport{DialContext: (&net.Dialer{Timeout: l.Timeout, KeepAlive: -1}).DialContext, DisableKeepAlives: true}
	transport.Proxy = func(r *http.Request) (*url.URL, error) {
		proxy, err := http.ProxyFromEnvironment(r)
		if err == nil && proxy != nil && proxy.User != nil {
			return nil, fmt.Errorf("batch: Sina静态HTTP不继承代理账户")
		}
		return proxy, err
	}
	d, err := NewDownloader(&http.Client{Transport: transport}, l)
	if err != nil {
		return nil, err
	}
	d.nativeHTTP = map[string]NativeHTTPGrant{g.URL: {URL: g.URL, Method: g.Method, LinkedURL: g.URL, Source: g.Source}}
	d.sinaStaticHTTP = true
	d.maxResponseBytes = (1 << 20) - 1
	return d, nil
}
