// Command contracts 冻结一次匿名目录取证，原始压缩响应留仓外。
package main

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const directoryURL = "https://openmd.shinnytech.com/t/md/symbols/latest.json"
const wireLimit = 32 << 20
const expandedLimit = 768 << 20

func save(root, name string, value any) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, name), append(b, '\n'), 0600)
}

type heartbeat struct {
	root  string
	bytes int64
	last  time.Time
}

func (h *heartbeat) Write(b []byte) (int, error) {
	h.bytes += int64(len(b))
	if time.Since(h.last) > 5*time.Second {
		h.last = time.Now()
		if err := save(h.root, "heartbeat.json", map[string]any{"at": h.last.UTC(), "wire_bytes": h.bytes}); err != nil {
			return 0, err
		}
	}
	return len(b), nil
}

func decodeGzip(r io.Reader, max int64) (inventory, error) {
	return decodeGzipWithPolicy(r, max, false)
}

func decodeGzipWithPolicy(r io.Reader, max int64, quarantine bool) (inventory, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return inventory{}, err
	}
	defer gz.Close()
	limited := &io.LimitedReader{R: gz, N: max + 1}
	result, err := scanWithPolicy(limited, quarantine)
	if err != nil {
		return inventory{}, err
	}
	if limited.N <= 1 {
		return inventory{}, fmt.Errorf("catalog_expanded_budget")
	}
	return result, nil
}

func acquire(root string, client *http.Client, address string) (err error) {
	// Mkdir本身是不可覆盖的启动门禁；未知旧目录绝不重试。
	if err = os.Mkdir(root, 0700); err != nil {
		return err
	}
	receipt := map[string]any{"url": address, "started_at": time.Now().UTC(), "state": "started", "requests_max": 1, "wire_limit": wireLimit, "expanded_limit": expandedLimit, "timeout_seconds": 1200, "full_history_certified": false}
	if err = save(root, "started.json", receipt); err != nil {
		return err
	}
	defer func() {
		receipt["finished_at"] = time.Now().UTC()
		if err != nil {
			receipt["state"], receipt["error"] = "failed", err.Error()
		}
		if finalErr := save(root, "receipt.json", receipt); err == nil {
			err = finalErr
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
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
	receipt["status"], receipt["content_encoding"] = response.StatusCode, response.Header.Get("Content-Encoding")
	receipt["server_date"], receipt["etag"] = response.Header.Get("Date"), response.Header.Get("ETag")
	file, err := os.OpenFile(filepath.Join(root, "symbols-response.gz"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	hash := sha256.New()
	activity := &heartbeat{root: root}
	validHeaders := response.StatusCode == 200 && strings.EqualFold(response.Header.Get("Content-Encoding"), "gzip")
	limit := int64(wireLimit)
	if !validHeaders {
		limit = 64 << 10 // 非法编码/错误页仅留有界诊断，不硬拉未压缩大包。
	}
	// 错误响应同样有界保存；读取上限含一个探针字节。
	n, copyErr := io.Copy(io.MultiWriter(file, hash, activity), io.LimitReader(response.Body, limit+1))
	closeErr := file.Close()
	receipt["wire_bytes"], receipt["sha256"] = n, hex.EncodeToString(hash.Sum(nil))
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	receipt["body_complete"] = n <= limit
	if !validHeaders {
		return fmt.Errorf("catalog_http_or_encoding")
	}
	if n > limit {
		return fmt.Errorf("catalog_wire_budget")
	}
	raw, err := os.Open(filepath.Join(root, "symbols-response.gz"))
	if err != nil {
		return err
	}
	defer raw.Close()
	result, err := decodeGzip(raw, expandedLimit)
	if err != nil {
		return err
	}
	if err = save(root, "inventory.json", result); err != nil {
		return err
	}
	receipt["state"], receipt["gzip_and_json_complete"] = "received", true
	return nil
}

func main() {
	root := flag.String("out", "", "全新仓外目录，存在即拒绝")
	offline := flag.String("offline-file", "", "仅离线重扫既有gzip，隔离产品身份冲突；绝不联网")
	flag.Parse()
	if *root == "" {
		fmt.Fprintln(os.Stderr, "必须给出-out")
		os.Exit(2)
	}
	if *offline != "" {
		if err := offlineScan(*offline, *root); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	transport := &http.Transport{Proxy: nil, DisableCompression: true, DialContext: (&net.Dialer{Timeout: 15 * time.Second}).DialContext, TLSHandshakeTimeout: 15 * time.Second, ResponseHeaderTimeout: 30 * time.Second}
	client := &http.Client{Transport: transport, Timeout: 20 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if err := acquire(*root, client, directoryURL); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("目录原件完整；仍不是官方历史挂牌全集或回测准入")
}

func offlineScan(input, root string) error {
	if err := os.Mkdir(root, 0700); err != nil {
		return err
	}
	file, err := os.Open(input)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, file); err != nil {
		return err
	}
	if _, err = file.Seek(0, 0); err != nil {
		return err
	}
	result, err := decodeGzipWithPolicy(file, expandedLimit, true)
	if err != nil {
		return err
	}
	if err = save(root, "inventory.json", result); err != nil {
		return err
	}
	b, err := os.ReadFile(filepath.Join(root, "inventory.json"))
	if err != nil {
		return err
	}
	sum := sha256.Sum256(b)
	return save(root, "scan-review.json", map[string]any{"state": "derived-complete", "input_sha256": hex.EncodeToString(hash.Sum(nil)), "inventory_sha256": hex.EncodeToString(sum[:]), "gzip_and_json_complete": true, "market_requests": 0, "product_conflicts_quarantined": len(result.Conflicts)})
}
