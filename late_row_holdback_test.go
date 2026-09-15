package tickflow_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/embedded"
	"github.com/dream-until-dawn/futures-tickflow-go/source/pacing"
	"github.com/dream-until-dawn/futures-tickflow-go/store/segfile"
)

// —— 这一组守的是勘误三：源一根没给的一天，什么时候才许登记成「拉过确认没有」——
//
// 由来（2026-09-15 离线实测，v0.5.0 与 d7f2d9a 读数相同；评审方独立复现）：
// 新浪日线收盘后要过一阵才补上当天那一行，而且出现之后还会再消失（2026-09-15 两路探针的读数在 docs/release/v0.6.0.md 勘误三）。
// 那段时间里 To=0 同步 ⇒ 当天 0 根 ⇒ 上一版照登 ⇒ 源补上之后被当成已覆盖跳过、报告全绿 ⇒ 永久缺一天。
//
// 判据（sync.go `fetch` 里「挂起」那一段）：一天源没给根，只有「它之后这次拉到了根」或
// 「它之后已经收盘了 emptyTailGraceDays(=5) 个交易日」才登记；否则挂起、下次重拉。
//
// ⚠️ 六格里有三格在修复之前也绿（块尾接力 · 同块夹心 · 失败块不接），而那是它们的用处：
// 它们挡的是**修过头**的实现 ——「证据只看本块」「一律挂起」「挂起跨过没拉成的块」——不是原缺陷。
// 原缺陷由「晚到的行」两格（To=0 与显式 To）与「年龄边界」一格挡。

var lateDays = []tickflow.TradingDay{
	20200803, 20200804, 20200805, 20200806, 20200807,
	20200810, 20200811, 20200812, 20200813, 20200814,
}

var lateSym = tickflow.Symbol{Exchange: "SHFE", Product: "rb", YearMon: 2101}

// lateSource 按一张可改的表给根，数自己被问了几次；fail 里的交易日在它所在那一块上报错。
type lateSource struct {
	mu    sync.Mutex
	give  map[tickflow.TradingDay]bool
	fail  map[tickflow.TradingDay]bool
	batch int
	calls int
}

func (s *lateSource) Caps(tickflow.ProductKey) tickflow.Capabilities {
	return tickflow.Capabilities{
		Periods:   []tickflow.Period{tickflow.Daily},
		Since:     map[tickflow.Period]tickflow.TradingDay{tickflow.Daily: 20000101},
		MaxBars:   1000,
		BatchDays: s.batch,
		ClientUse: tickflow.ClientUseNone,
	}
}

func (s *lateSource) Bars(_ context.Context, req tickflow.BarRequest) ([]tickflow.Bar, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	var out []tickflow.Bar
	for _, d := range lateDays {
		if d < req.From || d > req.To {
			continue
		}
		if s.fail[d] {
			return nil, errors.New("lateSource: 这一块造的失败")
		}
		if !s.give[d] {
			continue
		}
		ts := dayStartMs(d)
		out = append(out, tickflow.Bar{Ts: ts, TsEnd: ts + int64(24*time.Hour/time.Millisecond) - 1,
			TradingDay: d, Open: 1, High: 1, Low: 1, Close: float64(d % 100), Volume: 1})
	}
	return out, nil
}

func (s *lateSource) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

type lateRig struct {
	t     *testing.T
	store *segfile.Store
	src   *lateSource
	syn   *tickflow.Syncer
}

func newLateRig(t *testing.T, src *lateSource) *lateRig {
	t.Helper()
	cal, err := embedded.New(lateDays)
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
	return &lateRig{t: t, store: store, src: src, syn: syn}
}

func (r *lateRig) sync(label string, from, to tickflow.TradingDay, now int64, maxFails int, allowErr bool) tickflow.SyncReport {
	r.t.Helper()
	rep, err := r.syn.Sync(context.Background(), tickflow.SyncRequest{
		Symbol: lateSym, Period: tickflow.Daily, From: from, To: to, MaxConsecutiveFails: maxFails}, now)
	r.t.Logf("%s：err=%v · Requested=%v · Bars=%d · Halt=%v · Gaps=%v · HeldBack=%q · coverage=%v",
		label, err, rep.Requested, rep.Bars, rep.Halt, rep.Gaps, rep.HeldBack, r.store.Coverage())
	if err != nil && !allowErr {
		r.t.Fatalf("%s 不该出错：%v", label, err)
	}
	return rep
}

// kindOf 返回 d 在缺口里的类型；有数据（不在缺口里）返回 0。
func kindOf(rep tickflow.SyncReport, d tickflow.TradingDay) tickflow.GapKind {
	for _, g := range rep.Gaps {
		if g.From <= d && d <= g.To {
			return g.Kind
		}
	}
	return 0
}

func covered(store *segfile.Store, d tickflow.TradingDay) bool {
	for _, sp := range store.Coverage() {
		if sp.From <= d && d <= sp.To {
			return true
		}
	}
	return false
}

func closeOn(t *testing.T, store *segfile.Store, d tickflow.TradingDay) (float64, bool) {
	t.Helper()
	var c float64
	var ok bool
	for _, sp := range store.Coverage() {
		if err := store.Walk(sp.From, sp.To, func(b tickflow.Bar) bool {
			if b.TradingDay == d {
				c, ok = b.Close, true
			}
			return true
		}); err != nil {
			t.Fatalf("读回 %v 失败：%v", sp, err)
		}
	}
	return c, ok
}

// guard: 收盘后源还没出当天那一行 ⇒ 那一天不登记；源补上之后的下一次同步把它拉回来（To=0 与显式 To 各一遍）。
func TestLateRowIsNotRegisteredAndIsFetchedLater(t *testing.T) {
	const late = tickflow.TradingDay(20200806)
	for _, explicit := range []bool{false, true} {
		name := "To=0"
		if explicit {
			name = "显式To"
		}
		t.Run(name, func(t *testing.T) {
			src := &lateSource{batch: tickflow.BatchDaysUnbounded,
				give: map[tickflow.TradingDay]bool{20200803: true, 20200804: true, 20200805: true}}
			r := newLateRig(t, src)
			to := func(d tickflow.TradingDay) tickflow.TradingDay {
				if explicit {
					return d
				}
				return 0
			}

			// ① 0806 收盘之后、源还没给那一行（now＝0807 00:00 UTC ⇒ 0806 已收盘、0807 未收盘）。
			rep1 := r.sync("① 0806 收盘后，源没有 0806", 20200803, to(late), dayStartMs(20200807), 0, false)
			if rep1.Requested[1] != late {
				t.Fatalf("前提不成立：请求末端是 %s，不是 %s", rep1.Requested[1], late)
			}
			if covered(r.store, late) {
				t.Errorf("① 源一根没给、其后没有更晚的根、收盘后也不到 5 个交易日的 %s 被登记进了 coverage（%v）\n"+
					"  ⇒ 它会被当成「拉过确认没有」，源补上之后也不再拉", late, r.store.Coverage())
			}
			if k := kindOf(rep1, late); k != tickflow.GapNeverFetched {
				t.Errorf("① %s 在 Gaps 里是 %v，期望「没拉过」", late, k)
			}
			if len(rep1.HeldBack) != 1 || !strings.Contains(rep1.HeldBack[0], late.String()) {
				t.Errorf("① HeldBack=%q，期望恰好一条且点名 %s —— 否则「拉过、在等」与「没拉」共用一句话", rep1.HeldBack, late)
			}
			if !covered(r.store, 20200805) {
				t.Errorf("① 有根的 0805 没登记（%v）—— 挂起挂过头了", r.store.Coverage())
			}

			// ② 第二天，源补上了 0806。
			src.mu.Lock()
			src.give[late] = true
			src.mu.Unlock()
			before := src.count()
			// now 往后挪 6 小时（0807 14:00 CST）：0807 仍未收盘 ⇒ To=0 的末端仍是 0806，两格问的是同一天。
			rep2 := r.sync("② 源补上 0806 之后", 20200803, to(late), dayStartMs(20200807)+6*3600*1000, 0, false)
			if rep2.Requested[1] != late {
				t.Fatalf("② 前提不成立：请求末端是 %s，不是 %s", rep2.Requested[1], late)
			}
			if n := src.count() - before; n < 1 {
				t.Errorf("② 向源要了 %d 次 —— %s 被当成已覆盖跳过了", n, late)
			}
			if c, ok := closeOn(t, r.store, late); !ok || c != float64(late%100) {
				t.Errorf("② 库里读不回 %s 那一根（ok=%v close=%v）", late, ok, c)
			}
			if k := kindOf(rep2, late); k != 0 {
				t.Errorf("② %s 仍在缺口里：%v", late, k)
			}
			if !rep2.Complete() || len(rep2.HeldBack) != 0 {
				t.Errorf("② Complete()=%v HeldBack=%q，期望干净", rep2.Complete(), rep2.HeldBack)
			}
		})
	}
}

// guard: 年龄上限的边界 —— 之后已收盘 5 个交易日 ⇒ 登记；4 个 ⇒ 挂起。
func TestEmptyTailAgeCapBoundary(t *testing.T) {
	src := &lateSource{batch: tickflow.BatchDaysUnbounded, give: map[tickflow.TradingDay]bool{20200803: true}}
	r := newLateRig(t, src)
	// now＝0815 00:00 UTC ⇒ 0814 已收盘。0807 之后已收盘：0810..0814 共 5 个；0810 之后：4 个。
	rep := r.sync("尾巴 0804..0810 全空", 20200803, 20200810, dayStartMs(20200815), 0, false)
	if !covered(r.store, 20200807) || kindOf(rep, 20200807) != tickflow.GapConfirmedEmpty {
		t.Errorf("0807 之后已收盘 5 个交易日，应登记成「拉过确认没有」；实得 coverage=%v kind=%v",
			r.store.Coverage(), kindOf(rep, 20200807))
	}
	if covered(r.store, 20200810) || kindOf(rep, 20200810) != tickflow.GapNeverFetched {
		t.Errorf("0810 之后只收盘 4 个交易日，应挂起；实得 coverage=%v kind=%v",
			r.store.Coverage(), kindOf(rep, 20200810))
	}
}

// guard: 块尾空、下一块有根 ⇒ 块尾那几天在【同一次】同步里被夹住登记；第二次 0 请求。
// ⚠️ 挡的是「证据只看本块」：那样块尾会挂起而下一块照登 ⇒ coverage 留洞，而洞补不进去。
func TestEmptyChunkTailIsRegisteredByLaterChunk(t *testing.T) {
	src := &lateSource{batch: 2, give: map[tickflow.TradingDay]bool{20200803: true, 20200806: true}}
	r := newLateRig(t, src)
	// 块：[0803,0804] [0805,0806]。now＝0807 00:00 UTC ⇒ 0804 之后只收盘 2 个交易日，年龄上限帮不上忙。
	rep := r.sync("① 两块", 20200803, 20200806, dayStartMs(20200807), 0, false)
	if cov := r.store.Coverage(); len(cov) != 1 || cov[0].From != 20200803 || cov[0].To != 20200806 {
		t.Errorf("① coverage=%v，期望一段 [0803,0806]", cov)
	}
	for _, d := range []tickflow.TradingDay{20200804, 20200805} {
		if k := kindOf(rep, d); k != tickflow.GapConfirmedEmpty {
			t.Errorf("① %s 是 %v，期望「拉过确认没有」（其后 0806 拉到了根）", d, k)
		}
	}
	before := src.count()
	rep2 := r.sync("② 同一请求再跑", 20200803, 20200806, dayStartMs(20200807), 0, false)
	if n := src.count() - before; n != 0 || rep2.Halt != tickflow.HaltAllCovered {
		t.Errorf("② 向源要了 %d 次、Halt=%v，期望 0 次与「全部覆盖过」", n, rep2.Halt)
	}
}

// guard: 同一块里被夹住的空日子第一次就登记（挡「一律挂起」）。
func TestEmptyDaySandwichedInOneChunkIsRegistered(t *testing.T) {
	src := &lateSource{batch: tickflow.BatchDaysUnbounded, give: map[tickflow.TradingDay]bool{20200803: true, 20200805: true}}
	r := newLateRig(t, src)
	rep := r.sync("0804 夹在中间", 20200803, 20200805, dayStartMs(20200806), 0, false)
	if k := kindOf(rep, 20200804); k != tickflow.GapConfirmedEmpty || len(rep.HeldBack) != 0 {
		t.Errorf("0804 是 %v、HeldBack=%q，期望「拉过确认没有」且无挂起", k, rep.HeldBack)
	}
}

// guard: 挂起不许接过一块没拉成的 —— 否则没拉成的那天会被一起登记成「拉过确认没有」。
func TestHeldBackDaysDoNotJumpAFailedChunk(t *testing.T) {
	src := &lateSource{batch: 1,
		give: map[tickflow.TradingDay]bool{20200803: true, 20200806: true},
		fail: map[tickflow.TradingDay]bool{20200805: true}}
	r := newLateRig(t, src)
	// 块：0803(有) 0804(空) 0805(失败，预算 1 次) 0806(有)。now＝0807 00:00 UTC ⇒ 年龄帮不上忙。
	rep := r.sync("中间一块失败", 20200803, 20200806, dayStartMs(20200807), 1, true)
	if covered(r.store, 20200805) {
		t.Errorf("没拉成的 0805 被登记进了 coverage（%v）—— 挂起的 0804 接过了失败的那一块", r.store.Coverage())
	}
	if covered(r.store, 20200804) {
		t.Errorf("0804 被登记了（%v）—— 紧接着它的那一块没拉成，它没有更晚的证据", r.store.Coverage())
	}
	if len(rep.HeldBack) != 1 || !strings.Contains(rep.HeldBack[0], "2020-08-04") {
		t.Errorf("HeldBack=%q，期望恰好一条且点名 2020-08-04", rep.HeldBack)
	}

	// 预算耗尽那条出口：挂起同样不登记，而且报告里要说出来（出口不走循环末尾那一句）。
	src2 := &lateSource{batch: 1,
		give: map[tickflow.TradingDay]bool{20200803: true},
		fail: map[tickflow.TradingDay]bool{20200805: true}}
	r2 := newLateRig(t, src2)
	rep2 := r2.sync("0805 失败而预算为 0", 20200803, 20200806, dayStartMs(20200807), 0, true)
	if rep2.Halt != tickflow.HaltBudget {
		t.Fatalf("前提不成立：Halt=%v，期望预算耗尽", rep2.Halt)
	}
	if covered(r2.store, 20200804) || len(rep2.HeldBack) != 1 || !strings.Contains(rep2.HeldBack[0], "2020-08-04") {
		t.Errorf("预算耗尽时 coverage=%v HeldBack=%q，期望 0804 不登记且 HeldBack 恰好一条点名它", r2.store.Coverage(), rep2.HeldBack)
	}
}
