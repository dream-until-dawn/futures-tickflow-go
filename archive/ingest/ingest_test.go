package ingest

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dream-until-dawn/futures-tickflow-go/archive/batch"
	"github.com/dream-until-dawn/futures-tickflow-go/refdata/evidence"
)

const official = `{"o_code":"0000","report_date":"20261104","o_curinstrument":[{"PRODUCTID":"ad_f","DELIVERYMONTH":"2612","OPENPRICE":"20000","HIGHESTPRICE":"20100","LOWESTPRICE":"19900","CLOSEPRICE":"20010","SETTLEMENTPRICE":"20020","PRESETTLEMENTPRICE":"20000","VOLUME":"1","OPENINTEREST":"2"}]}`

func plan(t *testing.T) Plan {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "original.json")
	if e := os.WriteFile(path, []byte(official), 0600); e != nil {
		t.Fatal(e)
	}
	return Plan{Schema: "historical-archive-v1", From: 20210104, To: 20261104, MaxRequests: 1, MaxBytes: 1 << 20, IntervalMS: 1, TimeoutMS: 1000, MaxLocalBytes: 1 << 20, Jobs: []batch.Job{{ID: "official-day", Exchange: "SHFE", Product: "ad", Year: 2026, Format: "shfe-json", Day: 20261104, URL: "https://www.shfe.com.cn/data/tradedata/future/dailydata/kx20261104.dat", Local: path, ExpectedSHA: hash([]byte(official)), RetrievedAt: time.Now().UTC()}}}
}
func encode(t *testing.T, p Plan) []byte {
	t.Helper()
	raw, e := json.Marshal(p)
	if e != nil {
		t.Fatal(e)
	}
	return raw
}
func TestOfflineArchiveNeedsNoProductCalendarAndResumes(t *testing.T) {
	p := plan(t)
	out := filepath.Join(t.TempDir(), "archive")
	raw := encode(t, p)
	r, e := Run(context.Background(), raw, out, false)
	if e != nil || r.Status != "received_all_planned_originals" || r.Requests != 0 || r.Results[0].Receipt.BodyState != "complete" {
		t.Fatalf("%+v %v", r, e)
	}
	if e = os.Remove(p.Jobs[0].Local); e != nil {
		t.Fatal(e)
	}
	r, e = Run(context.Background(), raw, out, false)
	if e != nil || !r.Results[0].Resumed {
		t.Fatal(e)
	}
	p.MaxRequests++
	if _, e = Run(context.Background(), encode(t, p), out, false); e == nil {
		t.Fatal("冻结预算被悄悄改写")
	}
	if e = os.WriteFile(filepath.Join(out, "originals/official-day.body"), []byte("tampered"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = Run(context.Background(), raw, out, false); e == nil {
		t.Fatal("归档篡改恢复通过")
	}
}
func TestPlanRejectsBlockedRoutesAndUnsafeBudgets(t *testing.T) {
	for _, kind := range []string{"id", "budget", "date", "host", "dce", "format"} {
		p := plan(t)
		switch kind {
		case "id":
			p.Jobs[0].ID = "../escape"
		case "budget":
			p.MaxBytes = 0
		case "date":
			p.Jobs[0].Day = 20261105
		case "host":
			p.Jobs[0].URL = "https://example.org/official"
		case "dce":
			p.Jobs[0].Exchange = "DCE"
			p.Jobs[0].URL = "https://www.dce.com.cn/"
			p.Jobs[0].Local = ""
		case "format":
			p.Jobs[0].Format = "unknown"
		}
		if _, e := Parse(encode(t, p)); e == nil {
			t.Fatal("错误计划通过", kind)
		}
	}
}
func TestBlockedAndNoNetworkAreNotSuccess(t *testing.T) {
	p := plan(t)
	p.Jobs = append(p.Jobs, batch.Job{ID: "blocked-dce", URL: "https://www.dce.com.cn/", Exchange: "DCE", Year: 2026, Blocked: "既有SSL失败，未核新路线"})
	r, e := Run(context.Background(), encode(t, p), t.TempDir(), false)
	if e != nil || r.Status != "incomplete" || r.Results[1].Receipt.State != "blocked" || r.Requests != 0 {
		t.Fatalf("%+v %v", r, e)
	}
	p = plan(t)
	p.Jobs[0].Local = ""
	r, e = Run(context.Background(), encode(t, p), t.TempDir(), false)
	if e != nil || r.Results[0].Receipt.State != "failed" || r.Results[0].Receipt.BodyState != "none" || r.Requests != 0 {
		t.Fatal("默认离线发起请求或冒充成功")
	}
}
func TestAnnualContainerDateAndTruncation(t *testing.T) {
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	f, e := z.Create("official2025.1.xlsx")
	if e != nil {
		t.Fatal(e)
	}
	f.Write([]byte("container only, row parsing not certified"))
	z.Close()
	raw := b.Bytes()
	ref := evidence.Ref{ID: "year", URL: "https://www.shfe.com.cn/year.zip", SHA256: hash(raw), RetrievedAt: time.Now()}
	job := batch.Job{Format: "shfe-annual-zip", Year: 2025}
	if e = ValidateOriginal(job, raw, ref); e != nil {
		t.Fatal(e)
	}
	job.Year = 2024
	if e = ValidateOriginal(job, raw, ref); e == nil {
		t.Fatal("工作簿错年通过")
	}
	job.Year = 2025
	bad := raw[:len(raw)-5]
	ref.SHA256 = hash(bad)
	if e = ValidateOriginal(job, bad, ref); e == nil {
		t.Fatal("截断ZIP通过")
	}
}
func TestPriorUnknownNetworkBudgetPreventsNewAttempt(t *testing.T) {
	p := plan(t)
	p.Jobs[0].Local = ""
	p.Jobs = append(p.Jobs, p.Jobs[0])
	p.Jobs[1].ID = "next"
	out := t.TempDir()
	os.Mkdir(filepath.Join(out, "originals"), 0700)
	receipt, _ := json.Marshal(batch.Receipt{State: "in_progress", BodyState: "none"})
	os.WriteFile(filepath.Join(out, "originals/official-day.json"), receipt, 0600)
	if _, e := Run(context.Background(), encode(t, p), out, true); e == nil {
		t.Fatal("未知网络预算被当零")
	}
}
