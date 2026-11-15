package main

import (
	"testing"
	"time"

	"github.com/dream-until-dawn/futures-tickflow-go/refdata/evidence"
)

func TestCatalogJournalKeepsBoundedNewOriginalAndRejectsHTML(t *testing.T) {
	jobs := catalogEvidenceCandidates()
	if len(jobs) != 1 || jobs[0].URL != "https://www.czce.com.cn/cn/rootfiles/2020/01/13/1572882898738930-1572882898769401.pdf" || responseLimit(jobs[0]) != 4<<20 {
		t.Fatal("新原件清单/预算漂移")
	}
	for _, tc := range []struct {
		body  string
		valid bool
	}{{"%PDF-1.7 synthetic", true}, {"<html>challenge</html>", false}} {
		body := []byte(tc.body)
		ref := evidence.Ref{ID: jobs[0].ID, URL: jobs[0].URL, SHA256: digestForTest(body), RetrievedAt: time.Now().UTC()}
		if (validate(jobs[0], body, ref) == nil) != tc.valid {
			t.Fatal("格式门禁失效")
		}
	}
}
