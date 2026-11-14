package batch

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/dream-until-dawn/futures-tickflow-go/refdata/evidence"
)

func TestSinaVendorBusinessFailureStopsBatchAndResume(t *testing.T) {
	root := t.TempDir()
	calls := 0
	jobs := []Job{{ID: "map", URL: SinaProductMapURL, Version: SinaVendorVersion}, {ID: "list", URL: sinaList, Version: SinaVendorVersion}}
	d := sinaDown(t, []string{SinaProductMapURL, sinaList}, 200, "data", &calls)
	reject := func(Job, []byte, evidence.Ref) error { return fmt.Errorf("内容不是批准数据") }
	rr, e := Run(context.Background(), root, jobs, d, 1<<20, reject)
	if e != nil || len(rr) != 2 || calls != 1 || rr[1].Receipt.HTTP.Attempted {
		t.Fatalf("业务失败后仍发请求 %+v %v %d", rr, e, calls)
	}
	third := strings.Replace(sinaList, "node=qh_c", "node=qh_m", 1)
	jobs = append(jobs, Job{ID: "third", URL: third, Version: SinaVendorVersion})
	d = sinaDown(t, []string{SinaProductMapURL, sinaList, third}, 200, "data", &calls)
	rr, e = Run(context.Background(), root, jobs, d, 1<<20, func(Job, []byte, evidence.Ref) error { return nil })
	if e != nil || calls != 1 || rr[2].Receipt.HTTP.Attempted || rr[2].Receipt.State != "failed" {
		t.Fatalf("业务失败恢复丢失停主机 %+v %v %d", rr, e, calls)
	}
}
