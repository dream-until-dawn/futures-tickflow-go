package main

import (
	"crypto/sha256"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/dream-until-dawn/futures-tickflow-go/refdata/evidence"
)

func digestForTest(body []byte) string { return fmt.Sprintf("%x", sha256.Sum256(body)) }

func TestSemanticsCandidatesKeepExactBudgetAndRejectShells(t *testing.T) {
	jobs := semanticsCandidates()
	if len(jobs) != 5 {
		t.Fatal(len(jobs))
	}
	seen := map[string]bool{}
	var total int64
	for _, j := range jobs {
		u, e := url.Parse(j.URL)
		if e != nil || u.Scheme != "https" || u.User != nil || seen[j.URL] {
			t.Fatal("非精确匿名HTTPS新候选", j)
		}
		seen[j.URL] = true
		total += responseLimit(j)
	}
	if total != 14<<20 {
		t.Fatal("候选预算漂移", total)
	}
	for i, body := range []string{"<html>除日线以外 连续交易时段</html>", "<html>Kline open_oi close_oi</html>", "<html>行权 自对冲 3,575,857</html>"} {
		j := jobs[i]
		b := []byte(body)
		ref := evidence.Ref{ID: j.ID, URL: j.URL, SHA256: digestForTest(b), RetrievedAt: time.Now().UTC()}
		if e := validate(j, b, ref); e != nil {
			t.Fatal(e)
		}
		b = []byte("<html>navigation or challenge</html>")
		ref.SHA256 = digestForTest(b)
		if validate(j, b, ref) == nil {
			t.Fatal("仅200空壳被算成业务正文")
		}
		b = []byte([]string{"<html>除日线以外</html>", "<html>open_oi</html>", "<html>自对冲</html>"}[i])
		ref.SHA256 = digestForTest(b)
		if validate(j, b, ref) == nil {
			t.Fatal("只有一个标题词被当完整指定正文")
		}
	}
}
