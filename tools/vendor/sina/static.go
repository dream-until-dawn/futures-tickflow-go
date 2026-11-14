package main

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/dream-until-dawn/futures-tickflow-go/archive/batch"
)

const staticURL = "https://raw.githubusercontent.com/akfamily/akshare/main/akshare/futures/cons.py"

// staticDownloader 只认冻结源明确引用的一个模块文件，不运行Python或SDK。
func staticDownloader(jobs []batch.Job) (*batch.Downloader, error) {
	if len(jobs) != 1 || jobs[0].URL != staticURL || jobs[0].Version != "akshare-static-source-v1" || jobs[0].Format != "static-python-raw" || jobs[0].Local != "" {
		return nil, fmt.Errorf("唯一静态原件清单不符")
	}
	t := &http.Transport{DialContext: (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: -1}).DialContext, DisableKeepAlives: true, TLSHandshakeTimeout: 10 * time.Second}
	t.Proxy = func(r *http.Request) (*url.URL, error) {
		p, e := http.ProxyFromEnvironment(r)
		if e == nil && p != nil && p.User != nil {
			return nil, fmt.Errorf("静态读取不继承代理凭据")
		}
		return p, e
	}
	// 留一字节给越界探针，使实际读取也不超过获批1MiB。
	return batch.NewDownloader(&http.Client{Transport: t}, batch.Limits{Requests: 1, Bytes: (1 << 20) - 1, Interval: time.Second, Timeout: 15 * time.Second})
}
