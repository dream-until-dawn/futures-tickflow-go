package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/dream-until-dawn/futures-tickflow-go/archive/batch"
)

func TestNoticeVisibleNumberAndUnattemptedContinuation(t *testing.T) {
	bodies := [][]byte{[]byte("<html>公告</html>"), []byte("<html>违规〔2026〕<span>2</span> 59号</html>")}
	if !contentOK(1, bodies[1]) || contentOK(1, []byte("<html><script>259违规</script>shell</html>")) {
		t.Fatal("可见文号/脚本排除失效")
	}
	prior := make([]observation, 6)
	for i := range prior {
		prior[i] = observation{URL: targets[i], State: "blocked_after_host_failure"}
	}
	expected := []string{digest(bodies[0]), digest(bodies[1])}
	for i := 0; i < 2; i++ {
		prior[i].State = "received_original_scope_unreviewed"
		prior[i].SHA = expected[i]
		prior[i].HTTP = batch.Download{Attempted: true, ResponseReceived: true, Complete: true, Status: 200, FinalURL: targets[i]}
	}
	prior[1].State, prior[1].Error = "failed", "expected business text absent; not a verified document"
	raw, _ := json.Marshal(prior)
	p, _ := makePlan([]byte("binary"), []byte(`{"urls":[]}`))
	remaining, err := selectRemaining(p, raw, prior, bodies, expected)
	if err != nil || len(remaining.URLs) != 4 || remaining.StartIndex != 2 || remaining.PriorSHA != digest(raw) {
		t.Fatal(err)
	}
	calls := 0
	contents := []string{"<html>258限制</html>", "<html>交易</html>", "<html>交易时间</html>", "<html>休市</html>"}
	transport := roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() == targets[0] || r.URL.String() == targets[1] {
			t.Fatal("续取重发了前两项")
		}
		body := contents[calls]
		calls++
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	if results, err := run(t.TempDir(), remaining, transport, func() {}); err != nil || calls != 4 || len(results) != 4 {
		t.Fatal(err, calls)
	}
	prior[2].HTTP.Attempted = true
	if _, err := selectRemaining(p, raw, prior, bodies, expected); err == nil {
		t.Fatal("已经请求的后项被放行")
	}
	prior[2].HTTP.Attempted = false
	prior[1].HTTP.Status = 412
	if _, err := selectRemaining(p, raw, prior, bodies, expected); err == nil {
		t.Fatal("真实HTTP失败被当成本地误判")
	}
	prior[1].HTTP.Status = 200
	bodies[1] = []byte("<html>changed</html>")
	if _, err := selectRemaining(p, raw, prior, bodies, expected); err == nil {
		t.Fatal("原字节SHA漂移未拒绝")
	}
}
