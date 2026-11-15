package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/dream-until-dawn/futures-tickflow-go/refdata/evidence"
)

const nativeCFFEXURL = "http://www.cffex.com.cn/sj/historysj/202101/zip/202101.zip"
const sdkModuleSHA = "c547fcc877d3a4da6d0ed60a8a660a7a4b41e2f85e299818c72eb17b19eb8dca"

func nativePreflight() error {
	b, err := os.ReadFile("C:/Users/沁心/AppData/Local/Python/pythoncore-3.14-64/Lib/site-packages/akshare/futures/futures_daily_bar.py")
	if err != nil {
		return err
	}
	sum := sha256.Sum256(b)
	if hex.EncodeToString(sum[:]) != sdkModuleSHA {
		return fmt.Errorf("SDK出处字节改变，不开放新URL")
	}
	return nil
}

// 独立显式候选，不复用协会/新浪HTTP角色；只有用户另行批准后才调用。
func runNative(root string) error {
	if err := nativePreflight(); err != nil {
		return err
	}
	transport := &http.Transport{Proxy: nil, DisableCompression: true, DisableKeepAlives: true, DialContext: (&net.Dialer{Timeout: 10 * time.Second}).DialContext}
	defer transport.CloseIdleConnections()
	return captureNative(root, "C:/Temp/tickflow-contract-directory-20261115-v1/cffex-http-202101.started.json", &http.Client{Transport: transport})
}

func captureNative(root, marker string, client *http.Client) (err error) {
	return captureNativeFor(root, marker, client, nativeCFFEXURL)
}

func captureNativeFor(root, marker string, client *http.Client, address string) (err error) {
	month, err := nativeMonth(address)
	if err != nil {
		return err
	}
	if err = os.Mkdir(root, 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(marker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("已有/未知原生HTTP启动，拒绝重发: %w", err)
	}
	err = json.NewEncoder(f).Encode(map[string]string{"url": address, "target_root": root, "source_sha256": sdkModuleSHA})
	f.Close()
	if err != nil {
		return err
	}
	at := time.Now().UTC()
	receipt := map[string]any{"state": "started", "url": address, "requested_at": at, "method": "GET", "attempted": false, "response_received": false, "identity": "native-http/cleartext/unauthenticated/unqualified", "source_sha256": sdkModuleSHA, "financial_eligible": false, "publisher_authentication": false}
	defer func() {
		if err != nil {
			receipt["state"], receipt["error"] = "failed", err.Error()
		}
		b, e := json.MarshalIndent(receipt, "", "  ")
		if e == nil {
			e = os.WriteFile(filepath.Join(root, "receipt.json"), append(b, '\n'), 0600)
		}
		if err == nil {
			err = e
		}
	}()
	h := *client
	h.Timeout = 15 * time.Second
	h.Jar = nil
	h.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return err
	}
	receipt["attempted"] = true
	resp, err := h.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	receipt["response_received"], receipt["status"] = true, resp.StatusCode
	receipt["server_date"] = resp.Header.Get("Date")
	b, readErr := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
	sum := sha256.Sum256(b)
	receipt["bytes"], receipt["sha256"] = len(b), hex.EncodeToString(sum[:])
	receipt["body_complete"] = readErr == nil && len(b) <= 4<<20
	if e := os.WriteFile(filepath.Join(root, "month.body"), b, 0600); e != nil {
		return e
	}
	if readErr != nil {
		return readErr
	}
	if len(b) > 4<<20 {
		return fmt.Errorf("原生HTTP正文超4MiB")
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("原生HTTP状态%d，禁止跟转/重试", resp.StatusCode)
	}
	ref := evidence.Ref{ID: "cffex-" + month + "-native-http", URL: address, SHA256: hex.EncodeToString(sum[:]), RetrievedAt: at, Version: "research-native-http-unauthenticated"}
	if err = ref.Verify(b); err != nil {
		return err
	}
	if err = validateNativeMonth(b, month); err != nil {
		return err
	}
	receipt["state"], receipt["evidence"] = "received", ref
	return nil
}
