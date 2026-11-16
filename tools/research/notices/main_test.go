package main

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNoticeFailuresAndPriorBlockedURLsNeverRetry(t *testing.T) {
	p, err := makePlan([]byte("binary"), []byte(`{"urls":["https://www.shfe.com.cn/publicnotice/notice/"]}`))
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	transport := roundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.String() == targets[0] {
			t.Fatal("旧失败入口被重试")
		}
		return &http.Response{StatusCode: 412, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("challenge")), Request: r}, nil
	})
	root := t.TempDir()
	results, err := run(root, p, transport, func() {})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(results) != 6 || results[0].HTTP.Attempted || results[1].State != "failed" || results[2].HTTP.Attempted {
		t.Fatal("失败/拒绝清单/同主机停机边界失效")
	}
	if results[1].HTTP.Status != 412 || results[1].HeadersAt == nil || results[1].SHA != digest([]byte("challenge")) {
		t.Fatal("失败响应身份丢失")
	}
	if raw, _ := os.ReadFile(filepath.Join(root, "02.body")); string(raw) != "challenge" {
		t.Fatal("失败原字节未保留")
	}
}

func TestNoticePositiveTransferDoesNotGrantReviewAndChallengeStops(t *testing.T) {
	p, _ := makePlan([]byte("binary"), []byte(`{"urls":[]}`))
	bodies := []string{"<html>公告</html>", "<html>259 违规</html>", "<html>258 限制</html>", "<html>交易</html>", "<html>交易时间</html>", "<html>休市</html>"}
	index := 0
	transport := roundTrip(func(r *http.Request) (*http.Response, error) {
		body := bodies[index]
		index++
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	results, err := run(t.TempDir(), p, transport, func() {})
	if err != nil || len(results) != 6 || index != 6 {
		t.Fatal(err, index)
	}
	for _, r := range results {
		if r.State != "received_original_scope_unreviewed" || !r.HTTP.Complete || r.HeadersAt == nil || r.Finished.Before(*r.HeadersAt) {
			t.Fatal("取得被混为审核或时刻错误")
		}
	}
	if contentOK(0, []byte("<html>challenge</html>")) || contentOK(1, []byte("<html>258 违规</html>")) {
		t.Fatal("挑战/错误编号竟通过")
	}
}
