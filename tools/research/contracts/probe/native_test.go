package main

import (
	"archive/zip"
	"bytes"
	"io"
	"net/http"
	"path/filepath"
	"testing"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCFFEXNativeCandidateExactURLAndMarker(t *testing.T) {
	var body bytes.Buffer
	z := zip.NewWriter(&body)
	f, _ := z.Create("20210104_1.csv")
	f.Write([]byte("fixture"))
	z.Close()
	base := t.TempDir()
	calls := 0
	client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.String() != nativeCFFEXURL || r.Method != "GET" || r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
			t.Fatal("请求身份改变")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(body.Bytes())), Request: r}, nil
	})}
	marker := filepath.Join(base, "started.json")
	if e := captureNative(filepath.Join(base, "one"), marker, client); e != nil {
		t.Fatal(e)
	}
	if e := captureNative(filepath.Join(base, "two"), marker, client); e == nil || calls != 1 {
		t.Fatal("换目录重发", e, calls)
	}
}

func TestCFFEXNativeCandidateDoesNotFollowOrRepair(t *testing.T) {
	for _, status := range []int{302, 403, 200} {
		base := t.TempDir()
		calls := 0
		client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: status, Header: http.Header{"Location": []string{"http://other.invalid/"}}, Body: io.NopCloser(bytes.NewReader([]byte("not a zip"))), Request: r}, nil
		})}
		if err := captureNative(filepath.Join(base, "capture"), filepath.Join(base, "started"), client); err == nil || calls != 1 {
			t.Fatal("跟转/错误被接受", status, err, calls)
		}
	}
}
