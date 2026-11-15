package main

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sampleEntry = `{"class":"FUTURE","exchange_id":"CZCE","product_id":"SR","delivery_year":2021,"delivery_month":5,"expire_datetime":1621407600.0}`

func compressed(raw string) []byte {
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	gz.Write([]byte(raw))
	gz.Close()
	return out.Bytes()
}

func TestHistoricalDirectoryIdentityAndCompleteEnvelope(t *testing.T) {
	raw := `{"CZCE.SR105":` + sampleEntry + `,"other":{"class":"OPTION","delivery_year":"not relevant"}}`
	result, err := decodeGzip(bytes.NewReader(compressed(raw)), 1<<20)
	if err != nil || len(result.Contracts) != 1 || result.Contracts[0].Canonical != "CZCE.SR2105" || result.ByClass["OPTION"] != 1 {
		t.Fatal(result, err)
	}
	for _, input := range []string{
		raw[:len(raw)-1], raw + `{}`, `{"CZCE.SR105":` + sampleEntry + `,"CZCE.SR105":` + sampleEntry + `}`,
		strings.Replace(raw, `"delivery_year":2021`, `"delivery_year":2022`, 1),
		strings.Replace(raw, `"product_id":"SR"`, `"product_id":"MA"`, 1),
	} {
		if got, err := decodeGzip(bytes.NewReader(compressed(input)), 1<<20); err == nil || len(got.Contracts) != 0 {
			t.Fatal("错误输入不得发布部分目录", got, err)
		}
	}
	bad := compressed(raw)
	bad[len(bad)-8] ^= 1
	for _, input := range [][]byte{bad, compressed(raw)[:len(compressed(raw))-8]} {
		if _, err := decodeGzip(bytes.NewReader(input), 1<<20); err == nil {
			t.Fatal("gzip校验尾未验证")
		}
	}
	if _, err := decodeGzip(bytes.NewReader(compressed(raw)), 16); err == nil {
		t.Fatal("解压预算未执行")
	}
}

func TestHistoricalDirectorySingleRequestAndPriorRootRefused(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Accept-Encoding") != "gzip" || r.Header.Get("Range") != "" || r.Header.Get("Cookie") != "" {
			t.Error("请求身份不符")
		}
		w.Header().Set("Content-Encoding", "gzip")
		w.Write(compressed(`{"CZCE.SR105":` + sampleEntry + `}`))
	}))
	defer server.Close()
	client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	root := filepath.Join(t.TempDir(), "new")
	if err := acquire(root, client, server.URL); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "inventory.json")); err != nil {
		t.Fatal(err)
	}
	if err := acquire(root, client, server.URL); err == nil || calls != 1 {
		t.Fatal("旧启动被重试", calls, err)
	}
}

func TestHistoricalDirectoryBadResponseDoesNotPublishInventory(t *testing.T) {
	for _, mode := range []string{"identity", "crc", "partial", "redirect"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if mode == "redirect" {
					w.Header().Set("Location", "/must-not-follow")
					w.WriteHeader(302)
					return
				}
				if mode == "identity" {
					io.WriteString(w, `{"CZCE.SR105":`+sampleEntry+`}`)
					return
				}
				w.Header().Set("Content-Encoding", "gzip")
				body := compressed(`{"CZCE.SR105":` + sampleEntry + `}`)
				if mode == "crc" {
					body[len(body)-8] ^= 1
				} else {
					body = body[:len(body)-8]
				}
				w.Write(body)
			}))
			defer server.Close()
			client := &http.Client{Transport: &http.Transport{DisableCompression: true}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			root := filepath.Join(t.TempDir(), "new")
			if err := acquire(root, client, server.URL); err == nil || calls != 1 {
				t.Fatal("无效响应被接受/重试", calls, err)
			}
			if _, err := os.Stat(filepath.Join(root, "inventory.json")); !os.IsNotExist(err) {
				t.Fatal("错误目录不得发布")
			}
		})
	}
}

func TestHistoricalDirectoryOfflineProductConflictsRemainSeparate(t *testing.T) {
	raw := `{"CZCE.SR105":` + sampleEntry + `,"CZCE.TC604":` + strings.ReplaceAll(strings.ReplaceAll(sampleEntry, `"product_id":"SR"`, `"product_id":"ZC"`), `"delivery_year":2021,"delivery_month":5`, `"delivery_year":2016,"delivery_month":4`) + `}`
	if _, err := decodeGzip(bytes.NewReader(compressed(raw)), 1<<20); err == nil {
		t.Fatal("严格旧入口不可改成默许")
	}
	result, err := decodeGzipWithPolicy(bytes.NewReader(compressed(raw)), 1<<20, true)
	if err != nil || len(result.Contracts) != 1 || len(result.Conflicts) != 1 || result.Conflicts[0].SourceKey != "CZCE.TC604" || result.ByClass["FUTURE"] != 2 {
		t.Fatal("冲突不能丢失或纳入可选候选", result, err)
	}
	if _, err = decodeGzipWithPolicy(bytes.NewReader(compressed(raw[:len(raw)-1])), 1<<20, true); err == nil {
		t.Fatal("离线隔离也必须完整")
	}
}
