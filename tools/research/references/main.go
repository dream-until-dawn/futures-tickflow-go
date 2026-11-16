// Command references 仅取得固定日期郑商所参考快照；不把字段值当专项规则或金融准入。
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/dream-until-dawn/futures-tickflow-go/archive/batch"
)

const address = "https://www.czce.com.cn/cn/DFSStaticFiles/Future/2026/20261116/FutureDataReferenceData.xml"

func validate(raw []byte) error {
	var value struct{ XMLName xml.Name }
	if err := xml.Unmarshal(raw, &value); err != nil {
		return err
	}
	if value.XMLName.Local != "FIXML" {
		return fmt.Errorf("不是已知FIXML参考快照")
	}
	return nil
}

func main() {
	root := flag.String("out", "", "全新仓外目录")
	launch := flag.Bool("launch", false, "仅此固定URL一次请求；默认零请求")
	flag.Parse()
	if err := run(*root, *launch); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(root string, launch bool) error {
	if root == "" {
		return fmt.Errorf("必须指定out")
	}
	if !launch {
		fmt.Println(address, "1项，最多一次/2MiB/15秒；预检请求0")
		return nil
	}
	if err := os.Mkdir(root, 0700); err != nil {
		return err
	}
	marker, err := os.OpenFile("C:/Temp/tickflow-reference-20261116.started.json", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("既有/未知启动禁止重发: %w", err)
	}
	err = json.NewEncoder(marker).Encode(map[string]string{"root": root, "url": address})
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
	result, fetchErr := d.FetchDetailed(context.Background(), address)
	sum := sha256.Sum256(result.Body)
	if err = os.WriteFile(filepath.Join(root, "reference.body"), result.Body, 0600); err != nil {
		return err
	}
	state := "received_original_scope_unreviewed"
	if fetchErr == nil {
		fetchErr = validate(result.Body)
	}
	message := ""
	if fetchErr != nil {
		state = "failed"
		message = fetchErr.Error()
	}
	receipt := map[string]any{"url": address, "state": state, "http": result, "finished_at_utc": time.Now().UTC(), "bytes": len(result.Body), "sha256": hex.EncodeToString(sum[:]), "error": message, "financial_admission_added": 0}
	raw, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(root, "receipt.json"), append(raw, '\n'), 0600); err != nil {
		return err
	}
	return fetchErr
}
