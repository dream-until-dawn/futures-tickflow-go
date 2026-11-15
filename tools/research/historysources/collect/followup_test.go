package main

import (
	"testing"
	"time"

	"github.com/dream-until-dawn/futures-tickflow-go/refdata/evidence"
)

func TestFollowupDocumentsRejectMinuteShellAndKeepNewURLs(t *testing.T) {
	jobs := followupCandidates()
	if len(jobs) != 2 || responseLimit(jobs[0])+responseLimit(jobs[1]) != 6<<20 {
		t.Fatal("续查范围或预算漂移", jobs)
	}
	if jobs[0].URL != "https://www.czce.com.cn/cn/uploadfile/2019/10/30/20191030161831109.pdf" || jobs[1].URL != "https://forum.shinnytech.com/question/26588/" {
		t.Fatal("固定新入口被替换")
	}
	for _, tc := range []struct {
		body     string
		accepted bool
	}{
		{"<html>盘前 分钟 合成规则</html>", true},
		{"<html>盘前 导航</html>", false},
		{"<html>分钟 合成规则</html>", false},
		{"<html>盘前 分钟</html>", false},
	} {
		body := []byte(tc.body)
		ref := evidence.Ref{ID: jobs[1].ID, URL: jobs[1].URL, SHA256: digestForTest(body), RetrievedAt: time.Now().UTC()}
		if (validate(jobs[1], body, ref) == nil) != tc.accepted {
			t.Fatalf("指定正文门禁不符: %q", tc.body)
		}
	}
}
