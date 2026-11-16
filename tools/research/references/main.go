// Command references 仅取得固定日期郑商所参考快照；不把字段值当专项规则或金融准入。
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	tf "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/archive/batch"
	"github.com/dream-until-dawn/futures-tickflow-go/refdata/catalog"
	"github.com/dream-until-dawn/futures-tickflow-go/refdata/evidence"
)

const address = "https://www.czce.com.cn/cn/DFSStaticFiles/Future/2026/20261116/FutureDataReferenceData.xml"

func validate(raw []byte) error { return validateFor(raw, address, 20261116) }
func validateFor(raw []byte, url string, day tf.TradingDay) error {
	sum := sha256.Sum256(raw)
	_, err := catalog.ParseCZCEReference(raw, evidence.Ref{ID: "reference-transfer-check", URL: url, SHA256: hex.EncodeToString(sum[:]), RetrievedAt: time.Now().UTC()}, day)
	return err
}

func main() {
	root := flag.String("out", "", "全新仓外目录")
	launch := flag.Bool("launch", false, "仅此固定URL一次请求；默认零请求")
	historical := flag.Bool("historical-20210104", false, "另批只取已有2021首日行情对应的一个历史参考快照")
	offline := flag.String("offline", "", "只读已有reference.body/receipt.json目录，不发请求")
	flag.Parse()
	if *offline != "" {
		if err := reviewExisting(*offline, *root, *historical); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if err := run(*root, *launch, *historical); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(root string, launch, historical bool) error {
	url, day, markerPath := address, tf.TradingDay(20261116), "C:/Temp/tickflow-reference-20261116.started.json"
	if historical {
		url = "https://www.czce.com.cn/cn/DFSStaticFiles/Future/2021/20210104/FutureDataReferenceData.xml"
		day = 20210104
		markerPath = "C:/Temp/tickflow-reference-20210104.started.json"
	}
	if root == "" {
		return fmt.Errorf("必须指定out")
	}
	if !launch {
		fmt.Println(url, "1项，最多一次/2MiB/15秒；预检请求0")
		return nil
	}
	if err := os.Mkdir(root, 0700); err != nil {
		return err
	}
	marker, err := os.OpenFile(markerPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("既有/未知启动禁止重发: %w", err)
	}
	err = json.NewEncoder(marker).Encode(map[string]string{"root": root, "url": url})
	marker.Close()
	if err != nil {
		return err
	}
	transport := &http.Transport{DialContext: (&net.Dialer{Timeout: 10 * time.Second}).DialContext, TLSHandshakeTimeout: 10 * time.Second, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	d, err := batch.NewDownloader(&http.Client{Transport: transport}, batch.Limits{Requests: 1, Bytes: (2 << 20) - 1, Interval: time.Second, Timeout: 15 * time.Second})
	if err != nil {
		return err
	}
	result, fetchErr := d.FetchDetailed(context.Background(), url)
	sum := sha256.Sum256(result.Body)
	if err = os.WriteFile(filepath.Join(root, "reference.body"), result.Body, 0600); err != nil {
		return err
	}
	state := "received_original_scope_unreviewed"
	if fetchErr == nil {
		fetchErr = validateFor(result.Body, url, day)
	}
	message := ""
	if fetchErr != nil {
		state = "failed"
		message = fetchErr.Error()
	}
	receipt := map[string]any{"url": url, "state": state, "http": result, "finished_at_utc": time.Now().UTC(), "bytes": len(result.Body), "sha256": hex.EncodeToString(sum[:]), "error": message, "financial_admission_added": 0}
	raw, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(root, "receipt.json"), append(raw, '\n'), 0600); err != nil {
		return err
	}
	return fetchErr
}
