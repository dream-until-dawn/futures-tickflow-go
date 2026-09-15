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

// lateSource 按一张可改的表给根，数自己被问了几次、记下每次请求的块。
// fail[d] 是剩余失败次数：块里含 d 的请求在次数用完之前报错（给大数即恒失败）。
// cancelOn 非 0 时，块首为它的那次请求里调 cancel（只调一次），照常返回。
type lateSource struct {
	mu       sync.Mutex
	give     map[tickflow.TradingDay]bool
	fail     map[tickflow.TradingDay]int
	batch    int
	calls    int
	asked    [][2]tickflow.TradingDay
	cancelOn tickflow.TradingDay
	cancel   context.CancelFunc
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
	s.asked = append(s.asked, [2]tickflow.TradingDay{req.From, req.To})
	if s.cancel != nil && req.From == s.cancelOn {
		s.cancel()
		s.cancel = nil
	}
	for _, d := range lateDays {
		if d >= req.From && d <= req.To && s.fail[d] > 0 {
			s.fail[d]--
			return nil, errors.New("lateSource: 这一块造的失败")
		}
	}
	var out []tickflow.Bar
	for _, d := range lateDays {
		if d < req.From || d > req.To {
			continue
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

func (s *lateSource) take() [][2]tickflow.TradingDay {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.asked
	s.asked = nil
	return a
}

func sameBlocks(a, b [][2]tickflow.TradingDay) bool {
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
	return r.syncCtx(context.Background(), label, from, to, now, maxFails, allowErr)
}

func (r *lateRig) syncCtx(ctx context.Context, label string, from, to tickflow.TradingDay, now int64, maxFails int, allowErr bool) tickflow.SyncReport {
	r.t.Helper()
	rep, err := r.syn.Sync(ctx, tickflow.SyncRequest{
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
	if errs, verr := r.store.VerifyCoverage(); verr != nil || len(errs) != 1 {
		t.Errorf("① VerifyCoverage=%v,%v，期望一段且无错 —— 挂起并进下一块之后登记出去的 Bars/Days 要等于盘上", errs, verr)
	} else {
		for k, e := range errs {
			if e != nil {
				t.Errorf("① 段 %v 走查没过：%v —— 挂起并进下一块之后登记出去的 Bars/Days 与盘上不符", k, e)
			}
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

// guard: 一块失败一次而重试成功 ⇒ 挂起不丢，照样被更晚的根夹住登记（勘误四之后失败重试同一块，挂起与下一块仍紧挨）。
func TestHeldBackSurvivesARetriedChunk(t *testing.T) {
	src := &lateSource{batch: 1,
		give: map[tickflow.TradingDay]bool{20200803: true, 20200806: true},
		fail: map[tickflow.TradingDay]int{20200805: 1}}
	r := newLateRig(t, src)
	// 块：0803(有) 0804(空) 0805(失败一次，重试得空) 0806(有)。now＝0807 00:00 UTC ⇒ 年龄帮不上忙。
	rep := r.sync("0805 失败一次", 20200803, 20200806, dayStartMs(20200807), 1, false)
	want := [][2]tickflow.TradingDay{{20200803, 20200803}, {20200804, 20200804}, {20200805, 20200805}, {20200805, 20200805}, {20200806, 20200806}}
	if got := src.take(); !sameBlocks(got, want) {
		t.Errorf("请求序列 %v，期望 %v", got, want)
	}
	if cov := r.store.Coverage(); len(cov) != 1 || cov[0].From != 20200803 || cov[0].To != 20200806 {
		t.Errorf("coverage=%v，期望一段 [0803,0806]", cov)
	}
	for _, d := range []tickflow.TradingDay{20200804, 20200805} {
		if k := kindOf(rep, d); k != tickflow.GapConfirmedEmpty {
			t.Errorf("%s 是 %v，期望「拉过确认没有」（其后 0806 拉到了根）", d, k)
		}
	}
	if len(rep.HeldBack) != 0 {
		t.Errorf("HeldBack=%q，期望空", rep.HeldBack)
	}
}

// guard: 挂起在「预算耗尽」出口上被点名，且不登记（K=1 恒失败、K=0 一次失败各一格）。
func TestHeldBackIsReportedOnBudgetExit(t *testing.T) {
	for _, k := range []int{1, 0} {
		src := &lateSource{batch: 1,
			give: map[tickflow.TradingDay]bool{20200803: true},
			fail: map[tickflow.TradingDay]int{20200805: 1000}}
		r := newLateRig(t, src)
		rep := r.sync("0805 恒失败", 20200803, 20200806, dayStartMs(20200807), k, true)
		if rep.Halt != tickflow.HaltBudget {
			t.Fatalf("K=%d 前提不成立：Halt=%v，期望预算耗尽", k, rep.Halt)
		}
		if covered(r.store, 20200804) || len(rep.HeldBack) != 1 || !strings.Contains(rep.HeldBack[0], "2020-08-04") {
			t.Errorf("K=%d 预算耗尽时 coverage=%v HeldBack=%q，期望 0804 不登记且 HeldBack 恰好一条点名它", k, r.store.Coverage(), rep.HeldBack)
		}
	}
}

// guard: 挂起在「取消」出口上被点名，且不登记。
// ⚠️ 落盘失败、扩 coverage 失败两条出口上的 HeldBack 没有测试（难造）；代码里那两句 holdBack 由读代码核。
func TestHeldBackIsReportedOnCancelExit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	src := &lateSource{batch: 1, give: map[tickflow.TradingDay]bool{20200803: true},
		cancelOn: 20200805, cancel: cancel}
	r := newLateRig(t, src)
	// 块：0803(有) 0804(空，挂起) 0805(空，这次请求里被取消 ⇒ 并进挂起) ⇒ 循环顶看到取消。
	rep := r.syncCtx(ctx, "0805 那次请求里取消", 20200803, 20200806, dayStartMs(20200807), 0, true)
	if rep.Halt != tickflow.HaltContext {
		t.Fatalf("前提不成立：Halt=%v，期望被取消", rep.Halt)
	}
	if covered(r.store, 20200804) || len(rep.HeldBack) != 1 || !strings.Contains(rep.HeldBack[0], "2020-08-04..2020-08-05") {
		t.Errorf("取消时 coverage=%v HeldBack=%q，期望 0804 不登记且 HeldBack 恰好一条点名 0804..0805", r.store.Coverage(), rep.HeldBack)
	}
}

// guard: 挂起不许跨过一个已覆盖的交易日接进下一块 —— 否则登记出去的段与已有 coverage 重叠，而根已经落盘（孤儿记录）。
// 这是勘误四之后「不紧挨」唯一还走得到的来历：请求的 From 早于已有 coverage。
func TestHeldBackDaysDoNotJumpACoveredDay(t *testing.T) {
	src := &lateSource{batch: 1, give: map[tickflow.TradingDay]bool{20200805: true, 20200806: true}}
	r := newLateRig(t, src)
	r.sync("① 先同步 0805", 20200805, 20200805, dayStartMs(20200807), 0, false)
	// ② From=0804：want = [0804 0806]（0805 已覆盖）。0804 空 ⇒ 挂起；0806 与它隔着 0805 ⇒ 丢弃挂起。
	rep := r.sync("② From=0804 To=0806", 20200804, 20200806, dayStartMs(20200807), 0, true)
	if cov := r.store.Coverage(); len(cov) != 1 || cov[0].From != 20200805 || cov[0].To != 20200806 || rep.Halt != tickflow.HaltDone {
		t.Errorf("② coverage=%v Halt=%v，期望一段 [0805,0806]、跑完 —— 挂起跨过了已覆盖的 0805", cov, rep.Halt)
	}
	if len(rep.HeldBack) != 1 || !strings.Contains(rep.HeldBack[0], "2020-08-04") {
		t.Errorf("② HeldBack=%q，期望恰好一条点名 0804", rep.HeldBack)
	}
}

// guard: 整块挂起之后照常请求下一块（循环只在末尾前进；整块挂起那一支漏了前进 ⇒ 同一块无限循环）。
func TestWholeChunkHeldBackAdvancesToNextChunk(t *testing.T) {
	src := &lateSource{batch: 1, give: map[tickflow.TradingDay]bool{20200803: true, 20200805: true}}
	r := newLateRig(t, src)
	rep := r.sync("0804 整块空", 20200803, 20200805, dayStartMs(20200806), 0, false)
	want := [][2]tickflow.TradingDay{{20200803, 20200803}, {20200804, 20200804}, {20200805, 20200805}}
	if got := src.take(); !sameBlocks(got, want) {
		t.Errorf("请求序列 %v，期望 %v", got, want)
	}
	if k := kindOf(rep, 20200804); k != tickflow.GapConfirmedEmpty {
		t.Errorf("0804 是 %v，期望「拉过确认没有」", k)
	}
}

// guard: 年龄数的是 d 之后【请求区间内部也算】的已收盘交易日 —— To=0 时请求末端就是最后一个已收盘日，
// 只数末端之后的话尾巴永远长不到 5，年龄上限在这条主路上整个失效。逐天断言。
func TestEmptyTailAgeCapWithToZero(t *testing.T) {
	src := &lateSource{batch: tickflow.BatchDaysUnbounded, give: map[tickflow.TradingDay]bool{20200803: true}}
	r := newLateRig(t, src)
	// now＝0815 00:00 UTC ⇒ To=0 裁到 0814。尾巴 0804..0814 共 9 个交易日全空。
	// d 之后已收盘：0804 8 · 0805 7 · 0806 6 · 0807 5 · 0810 4 · 0811 3 · 0812 2 · 0813 1 · 0814 0
	rep := r.sync("To=0，尾巴 9 天全空", 20200803, 0, dayStartMs(20200815), 0, false)
	if rep.Requested[1] != 20200814 {
		t.Fatalf("前提不成立：请求末端 %s，期望 0814", rep.Requested[1])
	}
	for _, d := range []tickflow.TradingDay{20200804, 20200805, 20200806, 20200807} {
		if k := kindOf(rep, d); k != tickflow.GapConfirmedEmpty {
			t.Errorf("%s 是 %v，期望「拉过确认没有」（其后已收盘 ≥ 5 个交易日）", d, k)
		}
	}
	for _, d := range []tickflow.TradingDay{20200810, 20200811, 20200812, 20200813, 20200814} {
		if k := kindOf(rep, d); k != tickflow.GapNeverFetched {
			t.Errorf("%s 是 %v，期望「没拉过」（其后已收盘 < 5 个交易日）", d, k)
		}
	}
	if len(rep.HeldBack) != 1 || !strings.Contains(rep.HeldBack[0], "2020-08-10..2020-08-14") {
		t.Errorf("HeldBack=%q，期望恰好一条点名 0810..0814", rep.HeldBack)
	}
}

// guard: 尾巴年龄不够 ⇒ 不登记；再同步一次请求仍包含它们、仍不登记；等年龄够了 ⇒ 登记；之后 0 请求。
func TestHeldBackTailAgesIntoRegistration(t *testing.T) {
	src := &lateSource{batch: tickflow.BatchDaysUnbounded, give: map[tickflow.TradingDay]bool{20200803: true}}
	r := newLateRig(t, src)
	tail := []tickflow.TradingDay{20200804, 20200805, 20200806}
	kinds := func(rep tickflow.SyncReport) []tickflow.GapKind {
		var ks []tickflow.GapKind
		for _, d := range tail {
			ks = append(ks, kindOf(rep, d))
		}
		return ks
	}
	nf := []tickflow.GapKind{tickflow.GapNeverFetched, tickflow.GapNeverFetched, tickflow.GapNeverFetched}
	ce := []tickflow.GapKind{tickflow.GapConfirmedEmpty, tickflow.GapConfirmedEmpty, tickflow.GapConfirmedEmpty}
	same := func(a, b []tickflow.GapKind) bool {
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
		return len(a) == len(b)
	}

	// ① now＝0807 00:00 UTC：0804 之后已收盘 2 个 ⇒ 全挂起。
	rep1 := r.sync("① 年龄不够", 20200803, 20200806, dayStartMs(20200807), 0, false)
	if got := src.take(); !sameBlocks(got, [][2]tickflow.TradingDay{{20200803, 20200806}}) || !same(kinds(rep1), nf) {
		t.Errorf("① 请求 %v 类别 %v，期望 [[0803 0806]] 与三天「没拉过」", got, kinds(rep1))
	}
	// ② 同一时刻再跑：请求块包含尾巴，仍挂起。
	rep2 := r.sync("② 仍不够", 20200803, 20200806, dayStartMs(20200807), 0, false)
	if got := src.take(); !sameBlocks(got, [][2]tickflow.TradingDay{{20200804, 20200806}}) || !same(kinds(rep2), nf) {
		t.Errorf("② 请求 %v 类别 %v，期望 [[0804 0806]] 与三天「没拉过」", got, kinds(rep2))
	}
	// ③ now＝0815 00:00 UTC：0806 之后已收盘 6 个 ⇒ 三天都登记。
	rep3 := r.sync("③ 年龄够了", 20200803, 20200806, dayStartMs(20200815), 0, false)
	if got := src.take(); !sameBlocks(got, [][2]tickflow.TradingDay{{20200804, 20200806}}) || !same(kinds(rep3), ce) || len(rep3.HeldBack) != 0 {
		t.Errorf("③ 请求 %v 类别 %v HeldBack=%q，期望 [[0804 0806]]、三天「拉过确认没有」、无挂起", got, kinds(rep3), rep3.HeldBack)
	}
	// ④ 再跑 ⇒ 0 请求。
	rep4 := r.sync("④ 再跑", 20200803, 20200806, dayStartMs(20200815), 0, false)
	if got := src.take(); len(got) != 0 || rep4.Halt != tickflow.HaltAllCovered {
		t.Errorf("④ 请求 %v Halt=%v，期望 0 次与「全部覆盖过」", got, rep4.Halt)
	}
}

// guard: 年龄只数【日历覆盖之内】的已收盘交易日 —— now 远在日历末端之后，尾巴年龄也涨不过日历末端 ⇒ 仍挂起、每次重拉。
// 方向是吵（多拉），接受；日历要随时间续上，否则尾巴永远重拉。
func TestAgeCapStopsAtCalendarEnd(t *testing.T) {
	src := &lateSource{batch: tickflow.BatchDaysUnbounded, give: map[tickflow.TradingDay]bool{20200803: true}}
	r := newLateRig(t, src)
	// 日历末端 0814；now＝2020-10-01。0810 之后在日历里只有 4 个交易日。
	rep := r.sync("now 远在日历之后", 20200803, 20200814, dayStartMs(20201001), 0, false)
	if k := kindOf(rep, 20200807); k != tickflow.GapConfirmedEmpty {
		t.Errorf("0807 是 %v，期望「拉过确认没有」（日历里其后 5 个）", k)
	}
	if k := kindOf(rep, 20200810); k != tickflow.GapNeverFetched {
		t.Errorf("0810 是 %v，期望「没拉过」—— 日历里其后只有 4 个交易日，日历外的日子不数", k)
	}
}
