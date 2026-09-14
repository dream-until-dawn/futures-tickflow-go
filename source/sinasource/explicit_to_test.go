package sinasource

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/embedded"
	"github.com/dream-until-dawn/futures-tickflow-go/source/pacing"
	"github.com/dream-until-dawn/futures-tickflow-go/store/segfile"
)

// 日线形状：当日收盘前、显式 To=今天 ⇒ 拒；收盘后同一请求 ⇒ 今天那一根拉到。
//
// ⛔ 修之前是 v0.5.0 已发布的缺陷（v0.6.0 发布说明勘误二；RB2610 真实响应，末行 2026-09-08）：
//
//	底座 e553d09（= tag v0.5.0）与 cbd90ff（修之前的 main）逐字相同
//	① 09-08 10:00  err=nil · Bars=1 · Halt=跑完 · Complete=true · Gaps=[2026-09-08 拉过确认没有] · coverage [{09-07 09-08 1 1}]
//	② 09-08 16:00  err=nil · Bars=0 · Halt=区间里的交易日已经全部覆盖过 · Complete=true · 09-08 永远没有根
func TestDailyExplicitToTodayIsRejectedThenFilledAfterClose(t *testing.T) {
	fs := newFixtureServer(t)
	cal, err := embedded.New([]tickflow.TradingDay{20260903, 20260904, 20260907, 20260908})
	if err != nil {
		t.Fatal(err)
	}
	st, _, err := segfile.Open(t.TempDir(), tickflow.Daily)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	at := func(hh int) int64 { return time.Date(2026, 9, 8, hh, 0, 0, 0, tickflow.CST).UnixMilli() }
	sync := func(now int64) (tickflow.SyncReport, error) {
		syn, err := tickflow.NewSyncer(tickflow.SyncerConfig{Calendar: cal, Store: st, Pacer: pacing.NoPacing(), Timeout: 5 * time.Second,
			NewSource: func(hc *http.Client) tickflow.Source {
				c, err := New(cal, WithBaseURL(fs.URL), WithHTTPClient(hc), WithClock(func() int64 { return now }))
				if err != nil {
					t.Fatal(err)
				}
				return c
			}})
		if err != nil {
			t.Fatal(err)
		}
		return syn.Sync(context.Background(), tickflow.SyncRequest{
			Symbol: tickflow.Symbol{Exchange: tickflow.SHFE, Product: "rb", YearMon: 2610},
			Period: tickflow.Daily, From: 20260907, To: 20260908,
		}, now)
	}

	rep, err := sync(at(10))
	if err == nil || !strings.Contains(err.Error(), "还没收盘") {
		t.Fatalf("① 盘中显式 To=今天应报「还没收盘」：err=%v · Bars=%d · Gaps=%v · coverage=%v", err, rep.Bars, rep.Gaps, st.Coverage())
	}
	if cov := st.Coverage(); len(cov) != 0 {
		t.Fatalf("① 被拒之后不许有 coverage：%v", cov)
	}

	rep, err = sync(at(16))
	if err != nil {
		t.Fatalf("② 收盘后同一请求：%v", err)
	}
	cov := st.Coverage()
	if len(cov) != 1 {
		t.Fatalf("② coverage=%v，应为一段", cov)
	}
	days, err := st.DaysWithBars(cov[0])
	if err != nil {
		t.Fatal(err)
	}
	if rep.Bars != 2 || !days[20260908] || !rep.Complete() {
		t.Errorf("② 收盘后应拉到 09-07 与 09-08 两根：Bars=%d 有根的交易日=%v Complete=%v Gaps=%v", rep.Bars, days, rep.Complete(), rep.Gaps)
	}
}
