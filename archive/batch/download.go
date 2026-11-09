package batch

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Download 即使HTTP失败也保留响应身份与有界正文。Complete只表示读取完整，
// 不表示HTTP或业务验证成功；Body的SHA由收据记录，partial的SHA只指已收到前缀。
type Download struct {
	Body             []byte    `json:"-"`
	Attempted        bool      `json:"attempted"`
	ResponseReceived bool      `json:"response_received"`
	Status           int       `json:"status"`
	FinalURL         string    `json:"final_url,omitempty"`
	ContentType      string    `json:"content_type,omitempty"`
	ContentLength    int64     `json:"content_length"`
	ServerDate       string    `json:"server_date,omitempty"`
	RequestedAt      time.Time `json:"requested_at"`
	Complete         bool      `json:"complete"`
	Failure          string    `json:"failure,omitempty"`
}

// FetchDetailed 不丢弃非200、超预算和截断响应的有界失败字节；从不重试。
// 预算耗尽/取消/网络无响应与已收到部分响应明确分开。
func (d *Downloader) FetchDetailed(ctx context.Context, address string) (Download, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	result := Download{ContentLength: -1}
	u, e := url.Parse(address)
	if e != nil || u.User != nil || u.Host == "" || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1"))) {
		result.Failure = "invalid_url"
		return result, fmt.Errorf("batch: 非公开HTTPS地址")
	}
	if d.requests >= d.limits.Requests || d.bytes >= d.limits.Bytes {
		result.Failure = "budget"
		return result, fmt.Errorf("batch: 预算耗尽")
	}
	ctx, cancel := context.WithTimeout(ctx, d.limits.Timeout)
	defer cancel()
	if wait := time.Until(d.last.Add(d.limits.Interval)); !d.last.IsZero() && wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			result.Failure = "cancelled_before_request"
			return result, ctx.Err()
		case <-timer.C:
		}
	}
	request, e := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if e != nil {
		result.Failure = "invalid_request"
		return result, e
	}
	d.requests++
	d.last = time.Now()
	result.Attempted = true
	result.RequestedAt = d.last.UTC()
	response, e := d.client.Do(request)
	if e != nil {
		result.Failure = "network"
		return result, e
	}
	defer response.Body.Close()
	result.ResponseReceived = true
	result.Status = response.StatusCode
	result.ContentType = response.Header.Get("Content-Type")
	result.ContentLength = response.ContentLength
	result.ServerDate = response.Header.Get("Date")
	result.FinalURL = address
	if response.Request != nil && response.Request.URL != nil {
		result.FinalURL = response.Request.URL.String()
	}
	remaining := d.limits.Bytes - d.bytes
	raw, e := io.ReadAll(io.LimitReader(response.Body, remaining+1))
	d.bytes += int64(len(raw))
	result.Body = raw
	if e != nil {
		result.Failure = "read_error"
		return result, e
	}
	if int64(len(raw)) > remaining {
		result.Failure = "byte_limit"
		return result, fmt.Errorf("batch: 响应超过剩余字节预算")
	}
	result.Complete = true
	if response.StatusCode != http.StatusOK {
		result.Failure = "http_status"
		return result, fmt.Errorf("batch: HTTP %d", response.StatusCode)
	}
	return result, nil
}
