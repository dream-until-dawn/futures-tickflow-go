package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProviderRefreshWindowRejectsExpiredDate(t *testing.T) {
	for value, want := range map[string]bool{"2026-11-17T05:00:00Z": true, "2026-11-17T07:00:00Z": false, "2026-11-18T05:00:00Z": false} {
		now, _ := time.Parse(time.RFC3339, value)
		if providerWindow(now) != want {
			t.Fatal("指定日盘窗口判断错误", value)
		}
	}
}

func TestDirectProviderPreservesExplicitBytesAndSingleAttempt(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Encoding", "gzip")
		w.Write([]byte("raw-gzip-fixture"))
	}))
	defer server.Close()
	base := t.TempDir()
	marker := filepath.Join(base, "once")
	root := filepath.Join(base, "received")
	if e := fetchProvider(root, marker, providerClient(), server.URL); e != nil {
		t.Fatal(e)
	}
	raw, _ := os.ReadFile(filepath.Join(root, "symbols-response.gz"))
	if string(raw) != "raw-gzip-fixture" {
		t.Fatal("改写原件")
	}
	if fetchProvider(filepath.Join(base, "changed-root"), marker, providerClient(), server.URL) == nil || requests != 1 {
		t.Fatal("重复请求未被启动标记阻止")
	}
}

func TestDirectProviderRejectsRedirectAndWrongEncoding(t *testing.T) {
	for _, mode := range []string{"redirect", "plain"} {
		t.Run(mode, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if mode == "redirect" {
					w.Header().Set("Location", "/target")
					w.WriteHeader(302)
				} else {
					w.Write([]byte("plain"))
				}
			}))
			defer server.Close()
			root := t.TempDir()
			e := fetchProvider(filepath.Join(root, "out"), filepath.Join(root, "once"), providerClient(), server.URL)
			if e == nil || !strings.Contains(e.Error(), "provider_response_boundary") || requests != 1 {
				t.Fatal("重定向或非gzip未按目标原因拒绝", e, requests)
			}
		})
	}
}

func TestProviderSlowBodyIsBoundedAndPartialPreserved(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		for i := 0; i < 30; i++ {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(10 * time.Millisecond):
			}
			if _, e := w.Write([]byte("partial")); e != nil {
				return
			}
			w.(http.Flusher).Flush()
		}
	}))
	defer server.Close()
	root := t.TempDir()
	client := providerClient()
	client.Timeout = 70 * time.Millisecond
	err := fetchProvider(filepath.Join(root, "out"), filepath.Join(root, "once"), client, server.URL)
	if err == nil || !strings.Contains(err.Error(), "Client.Timeout") {
		t.Fatal("慢流未按总时限拒绝", err)
	}
	raw, e := os.ReadFile(filepath.Join(root, "out", "symbols-response.gz"))
	if e != nil || len(raw) == 0 || len(raw) >= 30*len("partial") {
		t.Fatal("没有保留真实部分响应", e, len(raw))
	}
}
