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
	cancelOn tickflow.TradingDay // 块首为它的第 cancelNth 次请求里调 cancel（造「重试之间被取消」；cancelNth 为 0 时按 1）
	cancelNth int
	cancel   context.CancelFunc
	seenOn   int
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
	if s.cancel != nil && req.From == s.cancelOn {
		s.seenOn++
		if n := s.cancelNth; s.seenOn == n || (n == 0 && s.seenOn == 1) {
			s.cancel()
		}
	}
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
	return holeSyncCtx(context.Background(), t, syn, store, src, label, to, k)
}

func holeSyncCtx(ctx context.Context, t *testing.T, syn *tickflow.Syncer, store *segfile.Store, src *holeSource, label string, to tickflow.TradingDay, k int) (tickflow.SyncReport, error, [][2]tickflow.TradingDay) {
	t.Helper()
	rep, err := syn.Sync(ctx, tickflow.SyncRequest{
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
	// 重试的请求必须与第一次完全相同（同一块的 From/To），整个请求序列逐项比。
	want1 := [][2]tickflow.TradingDay{{20200803, 20200804}, {20200805, 20200806}, {20200805, 20200806}, {20200807, 20200810}}
	if !sameAsked(asked1, want1) {
		t.Errorf("① 请求序列 %v，期望 %v", asked1, want1)
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

// guard: 预算用完 ⇒ HaltBudget，失败块及其后所有块都不登记；下一次同步从失败块起、能一路前进。
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

func sameAsked(a, b [][2]tickflow.TradingDay) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// guard: 重试之间被取消 ⇒ HaltContext，失败那一块及其后都不请求、不登记；下一次同步从失败那一块起。
func TestContextCanceledBetweenRetriesRegistersNothingAfter(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	src := &holeSource{failLeft: map[tickflow.TradingDay]int{20200805: 1}, cancelOn: 20200805, cancel: cancel}
	syn, store := newHoleRig(t, src)

	rep1, err1, asked1 := holeSyncCtx(ctx, t, syn, store, src, "① 0805 那块失败，且那次请求里被取消", 20200810, 1)
	want := [][2]tickflow.TradingDay{{20200803, 20200804}, {20200805, 20200806}}
	if !sameAsked(asked1, want) {
		t.Errorf("① 请求序列 %v，期望 %v —— 取消之后还在重试或往后请求", asked1, want)
	}
	if !errors.Is(err1, context.Canceled) || rep1.Halt != tickflow.HaltContext {
		t.Errorf("① err=%v Halt=%v，期望 context.Canceled 与 HaltContext", err1, rep1.Halt)
	}
	if !oneSpan(store, 20200803, 20200804, 2) {
		t.Errorf("① 覆盖 %v，期望只到失败块之前 [0803,0804]", store.Coverage())
	}

	src.cancel = nil
	_, err2, asked2 := holeSync(t, syn, store, src, "② 不再取消，To=0811", 20200811, 1)
	if err2 != nil || len(asked2) == 0 || asked2[0] != [2]tickflow.TradingDay{20200805, 20200806} || !oneSpan(store, 20200803, 20200811, 7) {
		t.Errorf("② err=%v 请求序列 %v 覆盖 %v，期望 nil、从 [0805,0806] 起、一段 [0803,0811] 7 根", err2, asked2, store.Coverage())
	}
}

// guard: 一块恒失败、预算很大（K=10）、第 2 次请求里被取消 ⇒ 取消在下一次重试之前生效：HaltContext、那一块恰好请求 2 次、之后不登记。
// ⚠️ 挡的是「重试路径不看 ctx」：那样取消要推迟到预算用完，报的是 HaltBudget。
func TestContextCanceledDuringPersistentFailureStopsBeforeBudget(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	src := &holeSource{failLeft: map[tickflow.TradingDay]int{20200805: 1000}, cancelOn: 20200805, cancelNth: 2, cancel: cancel}
	syn, store := newHoleRig(t, src)
	rep, err, asked := holeSyncCtx(ctx, t, syn, store, src, "0805 恒失败、K=10、第 2 次请求里取消", 20200810, 10)
	want := [][2]tickflow.TradingDay{{20200803, 20200804}, {20200805, 20200806}, {20200805, 20200806}}
	if !sameAsked(asked, want) {
		t.Errorf("请求序列 %v，期望 %v —— 失败块应恰好请求 2 次，取消之后不再重试", asked, want)
	}
	if !errors.Is(err, context.Canceled) || rep.Halt != tickflow.HaltContext {
		t.Errorf("err=%v Halt=%v，期望 context.Canceled 与 HaltContext —— 取消被推迟到了预算用完", err, rep.Halt)
	}
	if !oneSpan(store, 20200803, 20200804, 2) {
		t.Errorf("覆盖 %v，期望只到失败块之前 [0803,0804]", store.Coverage())
	}
}
