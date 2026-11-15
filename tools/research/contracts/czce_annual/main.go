// Command czce_annual 只做有SDK出处的2021年包HTTPS候选单次核查。
package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/dream-until-dawn/futures-tickflow-go/archive/batch"
	"github.com/dream-until-dawn/futures-tickflow-go/refdata/evidence"
)

const address = "https://www.czce.com.cn/cn/exchange/datahistory2021.zip"

func validate(raw []byte) error {
	z, e := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if e != nil {
		return e
	}
	if len(z.File) != 1 || z.File[0].Name != "datahistory2021.txt" || z.File[0].UncompressedSize64 > 64<<20 {
		return fmt.Errorf("年包成员或展开预算不符")
	}
	r, e := z.File[0].Open()
	if e != nil {
		return e
	}
	defer r.Close()
	n, e := io.Copy(io.Discard, io.LimitReader(r, (64<<20)+1))
	if e != nil || n > 64<<20 {
		return fmt.Errorf("年包CRC/展开预算: %v", e)
	}
	return nil
}

func run(root string) error {
	if root == "" {
		return fmt.Errorf("必须明确全新仓外目录")
	}
	if e := os.Mkdir(root, 0700); e != nil {
		return e
	}
	f, e := os.OpenFile("C:/Temp/tickflow-czce-annual-2021-https-20261115.started.json", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	e = json.NewEncoder(f).Encode(map[string]string{"url": address, "target_root": root})
	f.Close()
	if e != nil {
		return e
	}
	t := &http.Transport{Proxy: nil, DisableKeepAlives: true, DialContext: (&net.Dialer{Timeout: 10 * time.Second}).DialContext, TLSHandshakeTimeout: 10 * time.Second}
	defer t.CloseIdleConnections()
	d, e := batch.NewDownloader(&http.Client{Transport: t}, batch.Limits{Requests: 1, Bytes: (16 << 20) - 1, Interval: time.Second, Timeout: 15 * time.Second})
	if e != nil {
		return e
	}
	j := batch.Job{ID: "czce-annual-2021-https", URL: address, Exchange: "CZCE", Year: 2021, Format: "research-annual-zip", Version: "sdk-address-candidate-not-qualified"}
	r, e := batch.Run(context.Background(), filepath.Join(root, "archive"), []batch.Job{j}, d, 16<<20, func(_ batch.Job, b []byte, ref evidence.Ref) error {
		if e := ref.Verify(b); e != nil {
			return e
		}
		return validate(b)
	})
	if e != nil {
		return e
	}
	b, e := json.MarshalIndent(r, "", "  ")
	if e != nil {
		return e
	}
	if e = os.WriteFile(filepath.Join(root, "results.json"), append(b, '\n'), 0600); e != nil {
		return e
	}
	if len(r) != 1 || r[0].Receipt.State != "received" {
		return fmt.Errorf("候选未取得可认年包；停止，不切HTTP")
	}
	return nil
}

func main() {
	root := flag.String("out", "", "全新仓外目录")
	flag.Parse()
	if e := run(*root); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
