package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const providerURL = "https://openmd.shinnytech.com/t/md/symbols/latest.json"
const providerWireLimit = 32 << 20
const providerTimeout = 240 * time.Second

// providerPlan 只生成可审阅的零请求执行单；旧拒绝表例外必须由用户另行批准。
func providerPlan() map[string]any {
	return map[string]any{"schema": "explicit-minimum-refresh-plan-v1", "url": providerURL,
		"requests": 1, "wire_bytes_max": providerWireLimit, "expanded_bytes_max": 768 << 20,
		"timeout_seconds": 240, "retries": 0, "redirects": 0, "accounts": false,
		"trade_day": 20261117, "exception_approval_required": true,
		"execute_before": "2026-11-17T15:00:00+08:00",
		"purpose":        "仅取明确open_min_limit_order_volume/open_min_market_order_volume，不使用普通最小报单或SDK默认1",
		"marker":         "C:/Temp/tickflow-explicit-minimum-refresh-20261117.once"}
}

func providerWindow(now time.Time) bool {
	local := now.In(time.FixedZone("Asia/Shanghai", 8*3600))
	return local.Format("20060102") == "20261117" && local.Hour() >= 9 && local.Hour() < 15
}

func fetchProvider(root, marker string, client *http.Client, address string) (err error) {
	// 文件和URL均由正式入口固定；测试通过注入本机服务检查传输反例。
	f, err := os.OpenFile(marker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("未知或已启动刷新不得重试: %w", err)
	}
	_, err = f.WriteString(root)
	f.Close()
	if err != nil {
		return err
	}
	if err = os.Mkdir(root, 0700); err != nil {
		return err
	}
	receipt := map[string]any{"url": address, "requested_at": time.Now().UTC(), "state": "started", "defaults_applied": false}
	defer func() {
		receipt["received_at"] = time.Now().UTC()
		if err != nil {
			receipt["state"] = "failed"
			receipt["error"] = err.Error()
		}
		saveErr := write(filepath.Join(root, "provider.receipt.json"), receipt)
		if err == nil {
			err = saveErr
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), providerTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept-Encoding", "gzip")
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	receipt["status"], receipt["final_url"], receipt["server_date"], receipt["etag"] = response.StatusCode, response.Request.URL.String(), response.Header.Get("Date"), response.Header.Get("ETag")
	receipt["content_encoding"] = response.Header.Get("Content-Encoding")
	limit := int64(providerWireLimit)
	if response.StatusCode != 200 || response.Header.Get("Content-Encoding") != "gzip" {
		limit = 64 << 10
	}
	output, err := os.OpenFile(filepath.Join(root, "symbols-response.gz"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	hash := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(output, hash), io.LimitReader(response.Body, limit+1))
	closeErr := output.Close()
	receipt["bytes"], receipt["sha256"], receipt["body_complete"] = n, hex.EncodeToString(hash.Sum(nil)), copyErr == nil && n <= limit
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if response.StatusCode != 200 || response.Header.Get("Content-Encoding") != "gzip" || response.Request.URL.String() != address || n > limit {
		return fmt.Errorf("provider_response_boundary")
	}
	receipt["state"] = "received_raw_unqualified"
	return nil
}

func providerClient() *http.Client {
	// 自带总时限和请求context；没有重定向、SDK初始化、鉴权或隐式重连。
	return &http.Client{Timeout: providerTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
