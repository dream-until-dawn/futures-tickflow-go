package tickflow_test

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/embedded"
	"github.com/dream-until-dawn/futures-tickflow-go/source/pacing"
	"github.com/dream-until-dawn/futures-tickflow-go/store/segfile"
)

// —— 勘误四：一块失败而预算允许继续时，不许跳过这一块往后登记 ——
//
// 由来（评审方 2026-09-15 在 d7f2d9a 与 e553d09 上复现，读数逐字相同；形状即下面第一格）：
//
//	① 请求块 [0803..0804  0805..0806  0807..0810] · err=nil · 覆盖 [{0803 0804} {0807 0810}]   ← 洞
//	② 请求块 [0805..0806] · err「落盘失败，停在 2020-08-05: segfile: 交易日倒退了……」 ← 0811 根本没被请求
//	③ 与 ② 相同
//
// ⇒ K ≥ 1 ＋ 一次瞬时失败 ⇒ 这个库此后每次同步同处报错，新交易日永远拉不到。
// 修法：失败重试同一块；预算用完 ⇒ HaltBudget，之后一块都不登记。

var holeDays = []tickflow.TradingDay{20200803, 20200804, 20200805, 20200806, 20200807, 20200810, 20200811}

// holeSource 按块给根（每个交易日一根），记下每次被请求的块；failLeft[块首] 次数内对那一块报错。
type holeSource struct {
	mu       sync.Mutex
	failLeft map[tickflow.TradingDay]int
	asked    [][2]tickflow.TradingDay
}

func (s *holeSource) Caps(tickflow.ProductKey) tickflow.Capabilities {
	return tickflow.Capabilities{
		Periods:   []tickflow.Period{tickflow.Daily},
		Since:     map[tickflow.Period]tickflow.TradingDay{tickflow.Daily: 20000101},
		MaxBars:   1000,
		BatchDays: 2,
		ClientUse: tickflow.ClientUseNone,
	}
}

func (s *holeSource) Bars(_ context.Context, req tickflow.BarRequest) ([]tickflow.Bar, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.asked = append(s.asked, [2]tickflow.TradingDay{req.From, req.To})
	if s.failLeft[req.From] > 0 {
		s.failLeft[req.From]--
		return nil, errors.New("holeSource: 造的瞬时失败")
	}
	var out []tickflow.Bar
	for _, d := range holeDays {
		if d < req.From || d > req.To {
			continue
		}
		ts := dayStartMs(d)
		out = append(out, tickflow.Bar{Ts: ts, TsEnd: ts + int64(24*time.Hour/time.Millisecond) - 1,
			TradingDay: d, Open: 1, High: 1, Low: 1, Close: 1, Volume: 1})
	}
	return out, nil
}

func (s *holeSource) take() [][2]tickflow.TradingDay {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.asked
	s.asked = nil
	return a
}

func newHoleRig(t *testing.T, src *holeSource) (*tickflow.Syncer, *segfile.Store) {
	t.Helper()
	cal, err := embedded.New(holeDays)
	if err != nil {
		t.Fatalf("造日历失败：%v", err)
	}
	store, _, err := segfile.Open(t.TempDir(), tickflow.Daily)
	if err != nil {
		t.Fatalf("开库失败：%v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	syn, err := tickflow.NewSyncer(tickflow.SyncerConfig{
		Calendar: cal, Store: store, Pacer: pacing.NoPacing(), Timeout: 5 * time.Second,
		NewSource: func(*http.Client) tickflow.Source { return src },
	})
	if err != nil {
		t.Fatalf("造 Syncer 失败：%v", err)
	}
	return syn, store
}

func holeSync(t *testing.T, syn *tickflow.Syncer, store *segfile.Store, src *holeSource, label string, to tickflow.TradingDay, k int) (tickflow.SyncReport, error, [][2]tickflow.TradingDay) {
	t.Helper()
	rep, err := syn.Sync(context.Background(), tickflow.SyncRequest{
		Symbol: tickflow.Symbol{Exchange: "SHFE", Product: "rb", YearMon: 2101},
		Period: tickflow.Daily, From: 20200803, To: to, MaxConsecutiveFails: k}, dayStartMs(20200901))
	asked := src.take()
	t.Logf("%s：请求块 %v · err=%v · Bars=%d · Halt=%v · Gaps=%v · 覆盖 %v", label, asked, err, rep.Bars, rep.Halt, rep.Gaps, store.Coverage())
	return rep, err, asked
}

func oneSpan(store *segfile.Store, from, to tickflow.TradingDay, bars int) bool {
	cov := store.Coverage()
	return len(cov) == 1 && cov[0].From == from && cov[0].To == to && cov[0].Bars == bars
}

// guard: 一块失败一次（K=1）⇒ 重试同一块、登记连续；下一次同步能拉到新交易日；再下一次 0 请求。
func TestFailedChunkIsRetriedNotSkipped(t *testing.T) {
	src := &holeSource{failLeft: map[tickflow.TradingDay]int{20200805: 1}}
	syn, store := newHoleRig(t, src)

	rep1, err1, asked1 := holeSync(t, syn, store, src, "① To=0810，0805 那块失败一次", 20200810, 1)
	n0805 := 0
	for _, b := range asked1 {
		if b[0] == 20200805 {
			n0805++
		}
	}
	if n0805 != 2 {
		t.Errorf("① 0805..0806 那一块被请求了 %d 次，期望 2 次（失败一次 ＋ 重试一次）—— 失败的块被跳过了：%v", n0805, asked1)
	}
	if err1 != nil || rep1.Halt != tickflow.HaltDone {
		t.Errorf("① err=%v Halt=%v，期望 nil 与「跑完」", err1, rep1.Halt)
	}
	if !oneSpan(store, 20200803, 20200810, 6) {
		t.Errorf("① 覆盖 %v，期望一段 [0803,0810] 6 根 —— 失败的那一块被跳过、留了洞", store.Coverage())
	}
	if errs, verr := store.VerifyCoverage(); verr != nil || len(errs) != 1 {
		t.Errorf("① VerifyCoverage=%v,%v，期望一段且无错", errs, verr)
	} else {
		for k, e := range errs {
			if e != nil {
				t.Errorf("① 段 %v 走查没过：%v", k, e)
			}
		}
	}

	rep2, err2, asked2 := holeSync(t, syn, store, src, "② To=0811（新交易日）", 20200811, 1)
	if err2 != nil || len(asked2) != 1 || asked2[0] != [2]tickflow.TradingDay{20200811, 20200811} {
		t.Errorf("② err=%v 请求块 %v，期望 nil 且只请求 [0811,0811] —— 库卡在洞上就拉不到新交易日", err2, asked2)
	}
	if !oneSpan(store, 20200803, 20200811, 7) || rep2.Bars != 1 {
		t.Errorf("② 覆盖 %v Bars=%d，期望一段 [0803,0811] 7 根、本次 1 根", store.Coverage(), rep2.Bars)
	}

	rep3, err3, asked3 := holeSync(t, syn, store, src, "③ 同 ②", 20200811, 1)
	if err3 != nil || len(asked3) != 0 || rep3.Halt != tickflow.HaltAllCovered {
		t.Errorf("③ err=%v 请求块 %v Halt=%v，期望 nil、0 次请求、「全部覆盖过」", err3, asked3, rep3.Halt)
	}
}

// guard: 预算用完 ⇒ HaltBudget，失败块及其后一块都不登记；下一次同步从失败块起、能一路前进。
func TestBudgetExhaustedRegistersNothingAfterAndNextSyncAdvances(t *testing.T) {
	src := &holeSource{failLeft: map[tickflow.TradingDay]int{20200805: 2}}
	syn, store := newHoleRig(t, src)

	rep1, err1, asked1 := holeSync(t, syn, store, src, "① K=1，0805 那块连败两次", 20200810, 1)
	if !errors.Is(err1, tickflow.ErrBudgetExhausted) || rep1.Halt != tickflow.HaltBudget {
		t.Errorf("① err=%v Halt=%v，期望预算耗尽 —— 失败的块被跳过了，第二次失败根本没发生", err1, rep1.Halt)
	}
	for _, b := range asked1 {
		if b[0] == 20200807 {
			t.Errorf("① 预算用完之后还请求了 [0807,0810]：%v", asked1)
		}
	}
	if !oneSpan(store, 20200803, 20200804, 2) {
		t.Errorf("① 覆盖 %v，期望只到失败块之前 [0803,0804]", store.Coverage())
	}

	rep2, err2, asked2 := holeSync(t, syn, store, src, "② 源恢复，To=0811", 20200811, 1)
	if err2 != nil || len(asked2) == 0 || asked2[0][0] != 20200805 {
		t.Errorf("② err=%v 请求块 %v，期望 nil 且从 0805 起", err2, asked2)
	}
	if !oneSpan(store, 20200803, 20200811, 7) || rep2.Halt != tickflow.HaltDone {
		t.Errorf("② 覆盖 %v Halt=%v，期望一段 [0803,0811] 7 根、跑完", store.Coverage(), rep2.Halt)
	}
}

// guard: 「连续」数的是同一块 —— 两块各失败一次（K=1），中间隔着一次成功 ⇒ 不算连续两次，同步跑完且登记连续。
func TestConsecutiveFailsResetAfterARetrySucceeds(t *testing.T) {
	src := &holeSource{failLeft: map[tickflow.TradingDay]int{20200805: 1, 20200807: 1}}
	syn, store := newHoleRig(t, src)
	rep, err, asked := holeSync(t, syn, store, src, "0805 与 0807 两块各失败一次", 20200810, 1)
	if len(asked) != 5 {
		t.Errorf("请求块 %v，期望 5 次（两块各重试一次）", asked)
	}
	if err != nil || rep.Halt != tickflow.HaltDone || !oneSpan(store, 20200803, 20200810, 6) {
		t.Errorf("err=%v Halt=%v 覆盖 %v，期望 nil、跑完、一段 [0803,0810] 6 根 —— 重试成功之后连续失败计数没清零",
			err, rep.Halt, store.Coverage())
	}
}
