package batch

import (
	"bytes"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/dream-until-dawn/futures-tickflow-go/refdata/evidence"
)

// GFEXEnglishHomeURL 是本次仅批准一次公开GET的精确首页，不代表开放该站其他路径。
const GFEXEnglishHomeURL = "http://www.gfex.com.cn/en/index.shtml"

const gfexEnglishReportURL = "https://www.cs.com.cn/zzqh2020/202601/t20260113_6532950.html"
const gfexEnglishReportSHA = "5ec71426dfec7fa421e1a7ff131a5269eff5317b0ddfd198f33e2b47f6c95d6c"

// GFEXEnglishHomeGrant 独立于协会业务页授权，只接受已冻结的原始上线报道。
// SourceBody须为完整原件字节；Ref的获取时刻不可用本地复制时刻补造。
type GFEXEnglishHomeGrant struct {
	URL        string       `json:"url"`
	Method     string       `json:"method"`
	Source     evidence.Ref `json:"source"`
	SourceBody []byte       `json:"-"`
}

// NewGFEXEnglishHomeDownloader 仅构造候选，不联网；默认HTTPS及旧NativeHTTPGrant不变。
// 每实例只允许一次GET、最多2MiB/15秒、至少1秒间隔，无Cookie、重试或重定向。
// 明文标签只说明传输身份，HTTP200和Complete不等于通过业务内容验证。
func NewGFEXEnglishHomeDownloader(l Limits, g GFEXEnglishHomeGrant) (*Downloader, error) {
	if l.Requests != 1 || l.Bytes > 2<<20 || l.Timeout > 15*time.Second || l.Interval < time.Second {
		return nil, fmt.Errorf("batch: gfex-english-home预算超出批准")
	}
	if g.URL != GFEXEnglishHomeURL || g.Method != http.MethodGet {
		return nil, fmt.Errorf("batch: gfex-english-home仅允许精确GET")
	}
	if g.Source.URL != gfexEnglishReportURL || g.Source.SHA256 != gfexEnglishReportSHA {
		return nil, fmt.Errorf("batch: gfex-english-home报道身份不符")
	}
	if len(g.SourceBody) > 2<<20 || g.Source.Verify(g.SourceBody) != nil {
		return nil, fmt.Errorf("batch: gfex-english-home报道原件字节或Ref不符")
	}
	// 报道以正文文字列出首页，不是href；只认实际字节，不解码/推测其他地址。
	if !bytes.Contains(g.SourceBody, []byte(GFEXEnglishHomeURL)) {
		return nil, fmt.Errorf("batch: gfex-english-home报道未列明精确字面量")
	}
	transport := &http.Transport{
		DialContext:         (&net.Dialer{Timeout: l.Timeout, KeepAlive: -1}).DialContext,
		DisableKeepAlives:   true,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	// 不复用调用方client/transport，也不继承环境代理中的账户凭据。
	transport.Proxy = func(r *http.Request) (*url.URL, error) {
		proxy, err := http.ProxyFromEnvironment(r)
		if err == nil && proxy != nil && proxy.User != nil {
			return nil, fmt.Errorf("batch: gfex-english-home不继承代理账户凭据")
		}
		return proxy, err
	}
	d, err := NewDownloader(&http.Client{Transport: transport}, l)
	if err != nil {
		return nil, err
	}
	// 复用既有逐URL获取/明文收据机制；不调用或扩宽旧协会授权验证器。
	d.nativeHTTP = map[string]NativeHTTPGrant{g.URL: {
		URL: g.URL, Method: g.Method, LinkedURL: g.URL, Source: g.Source,
	}}
	d.maxResponseBytes = 2 << 20
	return d, nil
}
