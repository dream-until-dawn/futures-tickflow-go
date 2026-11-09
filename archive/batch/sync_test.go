package batch

import (
	"context"
	"net/http"
	"testing"
	"time"

	tf "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/embedded"
	"github.com/dream-until-dawn/futures-tickflow-go/source/pacing"
	"github.com/dream-until-dawn/futures-tickflow-go/store/segfile"
)

func TestFrozenSyncRejectsCalendarClippingBeforeSource(t *testing.T) {
	cal, e := embedded.New([]tf.TradingDay{20261102, 20261103})
	if e != nil {
		t.Fatal(e)
	}
	store, _, e := segfile.Open(t.TempDir(), tf.Daily)
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	called := false
	cfg := tf.SyncerConfig{Calendar: cal, Store: store, NewSource: func(*http.Client) tf.Source { called = true; return nil }, Pacer: pacing.NoPacing(), Timeout: time.Second}
	sym, _ := tf.ParseSymbol("SHFE.rb2701")
	_, e = SyncFrozen(context.Background(), cfg, tf.SyncRequest{Symbol: sym, Period: tf.Daily, From: 20261103, To: 20261104}, time.Now().UnixMilli())
	if e == nil || called || len(store.Coverage()) != 0 {
		t.Fatalf("超覆盖请求触发源/写入 %v %v", called, e)
	}
}
func TestPartitionReportsDoNotCertifyCounts(t *testing.T) {
	jobs := []Job{{ID: "a", Exchange: "DCE", Year: 2021}, {ID: "b", Exchange: "SHFE", Product: "rb", Year: 2025}, {ID: "c", Exchange: "SHFE", Product: "rb", Year: 2025}, {ID: "d", Exchange: "CZCE", Year: 2026}}
	results := []Result{{ID: "a", Receipt: Receipt{State: "blocked"}}, {ID: "b", Receipt: Receipt{State: "received"}}, {ID: "c", Receipt: Receipt{State: "in_progress"}}}
	r, e := Summarize(jobs, results)
	if e != nil || len(r) != 3 {
		t.Fatalf("%+v %v", r, e)
	}
	for _, p := range r {
		if p.CoverageStatus != "unverified_calendar_and_roster" {
			t.Fatal("计数冒充认证")
		}
	}
	if _, e = Summarize(jobs, append(results, results[0])); e == nil {
		t.Fatal("重复结果通过")
	}
	if _, e = Summarize(jobs, []Result{{ID: "extra", Receipt: Receipt{State: "received"}}}); e == nil {
		t.Fatal("计划外成功通过")
	}
}
