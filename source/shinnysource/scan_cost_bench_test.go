package shinnysource

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/embedded"
	"github.com/dream-until-dawn/futures-tickflow-go/source/pacing"
	"github.com/dream-until-dawn/futures-tickflow-go/store/segfile"
)

// —— ㉒ 到期那一问的读数：日内落库之后，每次 Sync 的整库扫描受不受得了 ——
//
// hasbars_expiry_test.go 那条到期条件问的是：
//
//	「去核日内落库之后，每次 Sync 的 DaysWithBars 与 VerifyCoverage 两遍整库扫描还受不受得了」
//
// 这里量【真的 Syncer ＋ 真的 segfile ＋ 本源（对离线复刻的天勤）】，不量本地复制的形状：
// Store 外面包一层只计数计时的壳（Syncer 对 Store 没有类型断言，包一层不改变行为）。
//
// 规模：一个合约一年 —— 合成交易日取【周一到周五、不扣节假日】（只为规模，不为日历真值），
// SHFE.rb 每天 345 根。按 probe.md 6.20 的读数，具体合约整个寿命的 id 数是
// SHFE.rb2605 82,769（已到期，全寿命）· CZCE.TA701 145,142（尚未到期，截至 2026-09-14 21:31）⇒ 这一档与之同量级。
//
// 跑法（一次拉 9 万根，固定 1 次）：
//
//	go test ./source/shinnysource/ -run XXX -bench SyncScanCost -benchtime=1x -count=3
//
// ⚠️ 射程：本机、页缓存热、离线复刻（本机回环上的 websocket）、单进程 ⇒ 扫描耗时是下界；
// Sync 总耗时里含复刻服务端造数据与本机回环的开销，**不代表对真天勤的耗时**。

type scanCountingStore struct {
	tickflow.Store
	mu        sync.Mutex
	daysCalls int
	daysDur   time.Duration
	verCalls  int
	verDur    time.Duration
}

func (s *scanCountingStore) DaysWithBars(sp tickflow.Span) (map[tickflow.TradingDay]bool, error) {
	t0 := time.Now()
	m, err := s.Store.DaysWithBars(sp)
	s.mu.Lock()
	s.daysCalls++
	s.daysDur += time.Since(t0)
	s.mu.Unlock()
	return m, err
}

func (s *scanCountingStore) VerifyCoverage() (map[tickflow.SpanKey]error, error) {
	t0 := time.Now()
	m, err := s.Store.VerifyCoverage()
	s.mu.Lock()
	s.verCalls++
	s.verDur += time.Since(t0)
	s.mu.Unlock()
	return m, err
}

func (s *scanCountingStore) take() (dc int, dd time.Duration, vc int, vd time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	dc, dd, vc, vd = s.daysCalls, s.daysDur, s.verCalls, s.verDur
	s.daysCalls, s.daysDur, s.verCalls, s.verDur = 0, 0, 0, 0
	return
}

func weekdays(from, to time.Time) []tickflow.TradingDay {
	var out []tickflow.TradingDay
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		if wd := d.Weekday(); wd == time.Saturday || wd == time.Sunday {
			continue
		}
		out = append(out, tickflow.TradingDay(d.Year()*10000+int(d.Month())*100+d.Day()))
	}
	return out
}

func BenchmarkSyncScanCost(b *testing.B) {
	days := weekdays(time.Date(2025, 9, 1, 0, 0, 0, 0, tickflow.CST), time.Date(2026, 9, 11, 0, 0, 0, 0, tickflow.CST))
	cal, err := embedded.New(days)
	if err != nil {
		b.Fatal(err)
	}
	t := &testing.T{}
	fs := newFakeServer(t)
	defer fs.srv.Close()
	fs.series["SHFE.rb2609"] = minuteBars(t, cal, rbKey, days[0], days[len(days)-1])
	sym := tickflow.Symbol{Exchange: tickflow.SHFE, Product: "rb", YearMon: 2609}
	from, penult, last := days[1], days[len(days)-2], days[len(days)-1]

	for i := 0; i < b.N; i++ {
		raw, _, err := segfile.Open(b.TempDir(), tickflow.MustIntraday(1))
		if err != nil {
			b.Fatal(err)
		}
		st := &scanCountingStore{Store: raw}
		cfg := fs.config(cal, farFuture)
		syn, err := tickflow.NewSyncer(tickflow.SyncerConfig{
			Calendar: cal, Store: st, Pacer: pacing.NoPacing(), Timeout: 30 * time.Second,
			NewSource: func(hc *http.Client) tickflow.Source {
				cfg.HTTPClient = hc
				c, err := New(cfg)
				if err != nil {
					panic(err)
				}
				return c
			},
		})
		if err != nil {
			b.Fatal(err)
		}
		run := func(label string, to tickflow.TradingDay) {
			t0 := time.Now()
			rep, err := syn.Sync(context.Background(), tickflow.SyncRequest{Symbol: sym, Period: tickflow.MustIntraday(1), From: from, To: to}, farFuture)
			wall := time.Since(t0)
			if err != nil {
				b.Fatalf("%s：%v", label, err)
			}
			dc, dd, vc, vd := st.take()
			b.Logf("%s：Sync %v · 本次拉 %d 根 · 库里 coverage %d 段 · DaysWithBars %d 次 %v · VerifyCoverage %d 次 %v · Halt=%v",
				label, wall.Round(time.Millisecond), rep.Bars, len(raw.Coverage()), dc, dd.Round(time.Millisecond), vc, vd.Round(time.Millisecond), rep.Halt)
		}
		run("一 首次回补一年", penult)
		run("二 原样再跑（全已覆盖）", penult)
		run("三 往后多一天（日常增量）", last)
		b.Logf("库里记录 %d 条（%d 个交易日）", countRecords(b, raw), len(days)-1)
		raw.Close()
	}
}

func countRecords(b *testing.B, st *segfile.Store) int {
	n := 0
	for _, sp := range st.Coverage() {
		n += sp.Bars
	}
	return n
}
