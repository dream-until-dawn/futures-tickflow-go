package sinasource

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/embedded"
	"github.com/dream-until-dawn/futures-tickflow-go/source/pacing"
	"github.com/dream-until-dawn/futures-tickflow-go/store/segfile"
)

// —— 勘误三在新浪日线上的真实形状：收盘后响应里还没有当天那一行 ——
//
// 两种输入形状（评审方要求各一格，底座同一份 testdata，末行 2026-09-08）：
//
//	多出一行        ① 原样（末行 0908）⇒ 0909 收盘后同步    ② 响应里多出 0909 那一行
//	截掉末行后恢复  ① 截掉 0908 那一行 ⇒ 0908 收盘后同步    ② 恢复原样
//
// 修复前（d7f2d9a 与 v0.5.0 离线实测相同）：① 把那一天登记成「拉过确认没有」·
// ② Bars=0、HaltAllCovered、那一天永远读不回来。

// guard: 新浪日线收盘后还没出当天那一行 ⇒ 不登记；出了之后下一次同步读得回来（两种输入形状 × To=0 与显式 To）。
func TestSinaLateDailyRowIsFetchedOnceItAppears(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "daily_RB2610.jsonp"))
	if err != nil {
		t.Fatal(err)
	}
	full := string(raw)
	const tail = "]);"
	i := strings.LastIndex(full, tail)
	if i < 0 || !strings.Contains(full, `{"d":"2026-09-08"`) {
		t.Fatalf("testdata 形状变了（找不到结尾或 0908 那一行）—— 下面的构造不成立")
	}
	row0909 := `{"d":"2026-09-09","o":"3109.000","h":"3120.000","l":"3100.000","c":"3115.000","v":"120000","p":"520000","s":"3110.000"}`
	withExtra := full[:i] + "," + row0909 + full[i:]
	j := strings.LastIndex(full, `,{"d":"2026-09-08"`)
	if j < 0 {
		t.Fatalf("testdata 里 0908 不是末行 —— 下面的构造不成立")
	}
	withoutLast := full[:j] + full[i:]

	shapes := []struct {
		name          string
		before, after string
		day           tickflow.TradingDay
		close         float64
	}{
		{"多出一行", full, withExtra, 20260909, 3115},
		{"截掉末行后恢复", withoutLast, full, 20260908, 3109},
	}
	for _, sh := range shapes {
		for _, explicit := range []bool{false, true} {
			name := sh.name + "/To=0"
			if explicit {
				name = sh.name + "/显式To"
			}
			t.Run(name, func(t *testing.T) {
				var body atomic.Value
				body.Store(sh.before)
				var hits atomic.Int64
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					hits.Add(1)
					io.WriteString(w, body.Load().(string))
				}))
				defer srv.Close()

				days := []tickflow.TradingDay{20260903, 20260904, 20260907, 20260908}
				if sh.day == 20260909 {
					days = append(days, 20260909)
				}
				cal, err := embedded.New(days)
				if err != nil {
					t.Fatal(err)
				}
				st, _, err := segfile.Open(t.TempDir(), tickflow.Daily)
				if err != nil {
					t.Fatal(err)
				}
				defer st.Close()
				sym := tickflow.Symbol{Exchange: tickflow.SHFE, Product: "rb", YearMon: 2610}
				y, m, d := sh.day.Split()
				now := time.Date(y, time.Month(m), d, 16, 0, 0, 0, tickflow.CST).UnixMilli() // 当天 16:00，已收盘

				var nowFn atomic.Int64
				nowFn.Store(now)
				syn, err := tickflow.NewSyncer(tickflow.SyncerConfig{Calendar: cal, Store: st, Pacer: pacing.NoPacing(), Timeout: 5 * time.Second,
					NewSource: func(hc *http.Client) tickflow.Source {
						c, err := New(cal, WithBaseURL(srv.URL), WithHTTPClient(hc), WithClock(nowFn.Load))
						if err != nil {
							t.Fatal(err)
						}
						return c
					}})
				if err != nil {
					t.Fatal(err)
				}
				req := tickflow.SyncRequest{Symbol: sym, Period: tickflow.Daily, From: 20260907}
				if explicit {
					req.To = sh.day
				}
				covered := func() bool {
					for _, sp := range st.Coverage() {
						if sp.From <= sh.day && sh.day <= sp.To {
							return true
						}
					}
					return false
				}

				rep1, err := syn.Sync(context.Background(), req, now)
				t.Logf("①：err=%v · Requested=%v · Bars=%d · Gaps=%v · HeldBack=%q · coverage=%v", err, rep1.Requested, rep1.Bars, rep1.Gaps, rep1.HeldBack, st.Coverage())
				if err != nil || rep1.Requested[1] != sh.day {
					t.Fatalf("① 前提不成立：err=%v 请求末端 %s（期望 %s）", err, rep1.Requested[1], sh.day)
				}
				if covered() {
					t.Errorf("① 响应里还没有 %s 那一行，而它被登记进了 coverage（%v）", sh.day, st.Coverage())
				}
				if len(rep1.HeldBack) != 1 || !strings.Contains(rep1.HeldBack[0], sh.day.String()) {
					t.Errorf("① HeldBack=%q，期望恰好一条且点名 %s", rep1.HeldBack, sh.day)
				}

				// ② 一小时后，新浪补上了那一行。
				body.Store(sh.after)
				nowFn.Store(now + int64(time.Hour/time.Millisecond))
				before := hits.Load()
				rep2, err := syn.Sync(context.Background(), req, nowFn.Load())
				t.Logf("②：err=%v · Requested=%v · Bars=%d · Halt=%v · Gaps=%v · HeldBack=%q · coverage=%v", err, rep2.Requested, rep2.Bars, rep2.Halt, rep2.Gaps, rep2.HeldBack, st.Coverage())
				if err != nil {
					t.Fatalf("② 出错：%v", err)
				}
				if n := hits.Load() - before; n < 1 {
					t.Errorf("② 向新浪要了 %d 次 —— %s 被当成已覆盖跳过了", n, sh.day)
				}
				var got float64
				var ok bool
				for _, sp := range st.Coverage() {
					if err := st.Walk(sp.From, sp.To, func(b tickflow.Bar) bool {
						if b.TradingDay == sh.day {
							got, ok = b.Close, true
						}
						return true
					}); err != nil {
						t.Fatalf("读回失败：%v", err)
					}
				}
				if !ok || got != sh.close {
					t.Errorf("② 库里读不回 %s 那一根（ok=%v close=%v，期望 %v）", sh.day, ok, got, sh.close)
				}
				if !covered() || len(rep2.HeldBack) != 0 || !rep2.Complete() {
					t.Errorf("② coverage=%v HeldBack=%q Complete()=%v，期望登记、无挂起、干净", st.Coverage(), rep2.HeldBack, rep2.Complete())
				}
			})
		}
	}
}

// guard: 新浪那一行出现之后又消失（来回翻）⇒ 撞上旧的那份仍挂起、不登记；拿到新的那份才登记并读得回；之后 0 请求。
//
// 形状取自 2026-09-15 两路探针的读数（RB2610 当日行 16:03:57 有、16:05:16 无、16:10:17 有……），评审方在 4dc6549 上离线量过同形四步。
// ⚠️ 射程：只覆盖「旧的一份少末尾几行」。旧响应【中间】缺行时，那一天会被更晚的根夹住、登记成「拉过确认没有」，
// 本版挡不住（评审方离线构造：响应缺 0907、有 0908 ⇒ 0907 登记，完整响应回来后 0 请求、永久缺）——见 v0.6.0 勘误三。
func TestSinaFlippingDailyRowIsRegisteredOnlyWhenItStays(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "daily_RB2610.jsonp"))
	if err != nil {
		t.Fatal(err)
	}
	full := string(raw)
	i := strings.LastIndex(full, "]);")
	j := strings.LastIndex(full, `,{"d":"2026-09-08"`)
	if i < 0 || j < 0 {
		t.Fatalf("testdata 形状变了（找不到结尾或末行 0908）—— 下面的构造不成立")
	}
	stale := full[:j] + full[i:]

	var body atomic.Value
	body.Store(stale)
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		io.WriteString(w, body.Load().(string))
	}))
	defer srv.Close()

	cal, err := embedded.New([]tickflow.TradingDay{20260903, 20260904, 20260907, 20260908})
	if err != nil {
		t.Fatal(err)
	}
	st, _, err := segfile.Open(t.TempDir(), tickflow.Daily)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var nowMs atomic.Int64
	syn, err := tickflow.NewSyncer(tickflow.SyncerConfig{Calendar: cal, Store: st, Pacer: pacing.NoPacing(), Timeout: 5 * time.Second,
		NewSource: func(hc *http.Client) tickflow.Source {
			c, err := New(cal, WithBaseURL(srv.URL), WithHTTPClient(hc), WithClock(nowMs.Load))
			if err != nil {
				t.Fatal(err)
			}
			return c
		}})
	if err != nil {
		t.Fatal(err)
	}
	const day = tickflow.TradingDay(20260908)
	req := tickflow.SyncRequest{Symbol: tickflow.Symbol{Exchange: tickflow.SHFE, Product: "rb", YearMon: 2610}, Period: tickflow.Daily, From: 20260907}
	at := func(d, hh int) int64 { return time.Date(2026, 9, d, hh, 0, 0, 0, tickflow.CST).UnixMilli() }
	kind := func(rep tickflow.SyncReport) tickflow.GapKind {
		for _, g := range rep.Gaps {
			if g.From <= day && day <= g.To {
				return g.Kind
			}
		}
		return 0
	}
	step := func(label, b string, now int64) (tickflow.SyncReport, int64) {
		body.Store(b)
		nowMs.Store(now)
		before := hits.Load()
		rep, err := syn.Sync(context.Background(), req, now)
		n := hits.Load() - before
		t.Logf("%s：请求 %d · err=%v · Bars=%d · Halt=%v · Gaps=%v · HeldBack=%q · coverage=%v", label, n, err, rep.Bars, rep.Halt, rep.Gaps, rep.HeldBack, st.Coverage())
		if err != nil {
			t.Fatalf("%s 出错：%v", label, err)
		}
		return rep, n
	}
	oneSpanTo := func(to tickflow.TradingDay) bool {
		cov := st.Coverage()
		return len(cov) == 1 && cov[0].From == 20260907 && cov[0].To == to
	}

	rep1, n1 := step("① 0908 16:00 旧份（缺 0908）", stale, at(8, 16))
	if n1 < 1 || kind(rep1) != tickflow.GapNeverFetched || len(rep1.HeldBack) != 1 || !oneSpanTo(20260907) {
		t.Errorf("① 请求 %d kind=%v HeldBack=%q coverage=%v，期望 ≥1、「没拉过」、挂起一条、[0907,0907]", n1, kind(rep1), rep1.HeldBack, st.Coverage())
	}
	rep2, n2 := step("② 0909 10:00 新份出现过又消失，这次仍拿到旧份", stale, at(9, 10))
	if n2 < 1 || kind(rep2) != tickflow.GapNeverFetched || len(rep2.HeldBack) == 0 || !oneSpanTo(20260907) {
		t.Errorf("② 请求 %d kind=%v HeldBack=%q coverage=%v，期望 ≥1、仍「没拉过」、挂起非空、coverage 没往后长", n2, kind(rep2), rep2.HeldBack, st.Coverage())
	}
	rep3, n3 := step("③ 0909 11:00 新份", full, at(9, 11))
	var got float64
	var ok bool
	for _, sp := range st.Coverage() {
		if err := st.Walk(sp.From, sp.To, func(b tickflow.Bar) bool {
			if b.TradingDay == day {
				got, ok = b.Close, true
			}
			return true
		}); err != nil {
			t.Fatalf("读回失败：%v", err)
		}
	}
	if n3 < 1 || kind(rep3) != 0 || !oneSpanTo(day) || !ok || got != 3109 {
		t.Errorf("③ 请求 %d kind=%v coverage=%v 读回 ok=%v close=%v，期望 ≥1、无缺口、[0907,0908]、close 3109", n3, kind(rep3), st.Coverage(), ok, got)
	}
	_, n4 := step("④ 0909 12:00 再跑", full, at(9, 12))
	if n4 != 0 {
		t.Errorf("④ 请求 %d，期望 0", n4)
	}
}
