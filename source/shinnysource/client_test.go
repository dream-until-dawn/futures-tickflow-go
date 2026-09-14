package shinnysource

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
)

// withPageWidth 把一窗的根数调小，好在几百根上走到翻页。
func withPageWidth(t *testing.T, w int) {
	t.Helper()
	old := pageWidth
	pageWidth = w
	t.Cleanup(func() { pageWidth = old })
}

// farFuture 是「所有根都已完结」的 now。
var farFuture = cst(2030, 1, 1, 0, 0)

// —— 端到端：翻页、升序、归日、完结 ——

func TestBarsPagesThroughAndAssignsTradingDays(t *testing.T) {
	fs := newFakeServer(t)
	cal := testCalendar(t)
	fs.series["SHFE.rb2601"] = minuteBars(t, cal, rbKey, 20260903, 20260908)
	withPageWidth(t, 100)

	c, err := New(fs.config(cal, farFuture))
	if err != nil {
		t.Fatal(err)
	}
	req := rbReq(2601, 20260904, 20260907)
	bars, err := c.Bars(context.Background(), req)
	if err != nil {
		t.Fatalf("Bars：%v", err)
	}
	if err := tickflow.CheckBars(req, bars, farFuture); err != nil {
		t.Errorf("CheckBars：%v", err)
	}

	// 手写的格子（不从日历推）：rb 夜盘 21:00–23:00，日盘 09:00–10:15 / 10:30–11:30 / 13:30–15:00。
	// 20260904（周五）：夜盘挂在 09-03 晚上 ⇒ 120 ＋ 225 ＝ 345 根
	// 20260907（周一）：夜盘挂在 09-04（周五）晚上 ⇒ 同样 345 根
	perDay := map[tickflow.TradingDay]int{}
	for _, b := range bars {
		perDay[b.TradingDay]++
	}
	if perDay[20260904] != 345 || perDay[20260907] != 345 || len(perDay) != 2 {
		t.Errorf("每个交易日的根数 = %v，手算应为 {20260904:345, 20260907:345}", perDay)
	}
	cells := []struct {
		name string
		ts   int64
		day  tickflow.TradingDay
	}{
		{"周四夜盘首根归周五", cst(2026, 9, 3, 21, 0), 20260904},
		{"周四夜盘末根归周五", cst(2026, 9, 3, 22, 59), 20260904},
		{"周五日盘末根归周五", cst(2026, 9, 4, 14, 59), 20260904},
		{"周五夜盘首根归周一", cst(2026, 9, 4, 21, 0), 20260907},
		{"周一日盘首根归周一", cst(2026, 9, 7, 9, 0), 20260907},
	}
	byTs := map[int64]tickflow.Bar{}
	for _, b := range bars {
		byTs[b.Ts] = b
	}
	for _, ce := range cells {
		b, ok := byTs[ce.ts]
		if !ok {
			t.Errorf("%s：%s 那一根不在结果里", ce.name, fmtTs(ce.ts))
			continue
		}
		if b.TradingDay != ce.day || b.TsEnd != ce.ts+60000 {
			t.Errorf("%s：TradingDay=%s TsEnd-Ts=%d，应为 %s / 60000", ce.name, b.TradingDay, b.TsEnd-b.Ts, ce.day)
		}
	}
	// 请求之外的两天不许出现：09-07 21:00（归 09-08）与 09-03 日盘（归 09-03）。
	if _, ok := byTs[cst(2026, 9, 7, 21, 0)]; ok {
		t.Error("09-07 21:00 归 20260908，落在请求之外，却出现在结果里")
	}
	if _, ok := byTs[cst(2026, 9, 3, 14, 59)]; ok {
		t.Error("09-03 14:59 归 20260903，落在请求之外，却出现在结果里")
	}
	b0 := bars[0]
	if b0.Flags != tickflow.FlagSrcShinny || !math.IsNaN(b0.Settle) || !math.IsNaN(b0.Turnover) {
		t.Errorf("首根 Flags=%v Settle=%v Turnover=%v，应为 FlagSrcShinny / NaN / NaN", b0.Flags, b0.Settle, b0.Turnover)
	}

	// 翻页的读数：690 根、窗宽 100 ⇒ 至少 7 窗；一次 Bars 只拨一次号。
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if fs.handshakes != 1 {
		t.Errorf("一次 Bars 握手 %d 次，应为 1", fs.handshakes)
	}
	if len(fs.setCharts) < 7 {
		t.Errorf("set_chart 只发了 %d 次 —— 690 根、窗宽 100 应至少 7 窗；翻页没走到", len(fs.setCharts))
	}
	if _, ok := fs.setCharts[0]["focus_datetime"]; !ok {
		t.Errorf("第一窗应按 focus_datetime 定位：%v", fs.setCharts[0])
	}
	for i, m := range fs.setCharts[1:] {
		if _, ok := m["left_kline_id"]; !ok {
			t.Errorf("第 %d 窗应按 left_kline_id 续翻：%v", i+2, m)
		}
	}
}

// 窗宽按日历分钟数定（加余量），不是一律 10000：请求五天时多要七八千根是白传（fetch.go viewWidth 的读数）。
func TestViewWidthFollowsCalendarMinutes(t *testing.T) {
	fs := newFakeServer(t)
	cal := testCalendar(t)
	fs.series["SHFE.rb2601"] = minuteBars(t, cal, rbKey, 20260903, 20260908)
	c, _ := New(fs.config(cal, farFuture))
	bars, err := c.Bars(context.Background(), rbReq(2601, 20260904, 20260907))
	if err != nil || len(bars) != 690 {
		t.Fatalf("bars=%d err=%v", len(bars), err)
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	// 两天 × 345 分钟 ＋ 余量 16 ＝ 706；而且一窗就判完（窗盖到了 winEnd 之后那一根）。
	if len(fs.setCharts) != 1 || int(fs.setCharts[0]["view_width"].(float64)) != 690+widthMargin {
		t.Errorf("set_chart %d 次、第一窗 view_width=%v，应为 1 次、%d", len(fs.setCharts), fs.setCharts[0]["view_width"], 690+widthMargin)
	}
}

// 前一窗恰好收在 winEnd 之前一根 / 恰好到 winEnd ⇒ 两种都要停在对的地方。
func TestBarsStopsExactlyAtWindowEnd(t *testing.T) {
	cal := testCalendar(t)
	all := minuteBars(t, cal, rbKey, 20260903, 20260908)
	for _, w := range []int{344, 345, 346, 689, 690, 691} {
		fs := newFakeServer(t)
		fs.series["SHFE.rb2601"] = all
		withPageWidth(t, w)
		c, _ := New(fs.config(cal, farFuture))
		bars, err := c.Bars(context.Background(), rbReq(2601, 20260904, 20260907))
		if err != nil {
			t.Fatalf("窗宽 %d：%v", w, err)
		}
		if len(bars) != 690 {
			t.Errorf("窗宽 %d：%d 根，应为 690", w, len(bars))
		}
	}
}

// —— 6.20 的三种「没有」——

func TestWindowAfterLastBarIsEmptyNotError(t *testing.T) {
	fs := newFakeServer(t)
	cal := testCalendar(t)
	fs.series["SHFE.rb2601"] = minuteBars(t, cal, rbKey, 20260903, 20260903) // 过期：最后一根在请求之前
	c, _ := New(fs.config(cal, farFuture))
	bars, err := c.Bars(context.Background(), rbReq(2601, 20260907, 20260908))
	if err != nil {
		t.Fatalf("窗在末根之后应是空切片，得到错误：%v", err)
	}
	if bars == nil || len(bars) != 0 {
		t.Errorf("得到 %v（nil=%v），应为非 nil 的空切片", bars, bars == nil)
	}
}

func TestWindowBeforeListingIsEmptyNotError(t *testing.T) {
	fs := newFakeServer(t)
	cal := testCalendar(t)
	fs.series["SHFE.rb2601"] = minuteBars(t, cal, rbKey, 20260908, 20260908) // 上市在请求之后
	c, _ := New(fs.config(cal, farFuture))
	bars, err := c.Bars(context.Background(), rbReq(2601, 20260904, 20260904))
	if err != nil || len(bars) != 0 {
		t.Fatalf("窗在上市之前应是空切片：bars=%d err=%v", len(bars), err)
	}
}

func TestUnknownContractIsNotReadyNotEmpty(t *testing.T) {
	fs := newFakeServer(t)
	cal := testCalendar(t)
	cfg := fs.config(cal, farFuture)
	cfg.ReadTimeout = 300 * time.Millisecond
	c, _ := New(cfg)
	t0 := time.Now()
	// 外层 10s 兜底：读期限没生效时这里报「被取消」而不是挂住整个测试进程。
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	bars, err := c.Bars(ctx, rbReq(9901, 20260904, 20260904))
	if !errors.Is(err, ErrNotReady) {
		t.Fatalf("不存在的合约应报 ErrNotReady（不能是空切片——那会被记成「拉过、确认没有」）：bars=%d err=%v", len(bars), err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("错误链里应带着读超时：%v", err)
	}
	if !strings.Contains(err.Error(), "last_id=-1") {
		t.Errorf("报文应带上 last_id=-1 这个读数：%v", err)
	}
	if d := time.Since(t0); d > 3*time.Second {
		t.Errorf("用了 %v，ReadTimeout 是 300ms —— 读期限没有生效", d)
	}
}

func TestStallMidPagingIsNotReady(t *testing.T) {
	fs := newFakeServer(t)
	cal := testCalendar(t)
	fs.series["SHFE.rb2601"] = minuteBars(t, cal, rbKey, 20260903, 20260908)
	fs.stallAfterPage = 2
	withPageWidth(t, 100)
	cfg := fs.config(cal, farFuture)
	cfg.ReadTimeout = 300 * time.Millisecond
	c, _ := New(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second) // 读期限没生效时报「被取消」，不挂住进程
	defer cancel()
	_, err := c.Bars(ctx, rbReq(2601, 20260904, 20260907))
	if !errors.Is(err, ErrNotReady) || !strings.Contains(err.Error(), "第 3 窗") {
		t.Fatalf("第 3 窗起服务端不再回，应报 ErrNotReady 并点名第 3 窗：%v", err)
	}
}

func TestMissingIDInsideWindowIsAnError(t *testing.T) {
	fs := newFakeServer(t)
	cal := testCalendar(t)
	fs.series["SHFE.rb2601"] = minuteBars(t, cal, rbKey, 20260903, 20260908)
	fs.dropID, fs.dropIDSet = 400, true
	c, _ := New(fs.config(cal, farFuture))
	_, err := c.Bars(context.Background(), rbReq(2601, 20260904, 20260907))
	if err == nil || !strings.Contains(err.Error(), "id 400 不在快照里") {
		t.Fatalf("窗里漏了一根，应报「窗没收全」而不是少一根照常返回：%v", err)
	}
}

// —— 完结的边界 ——

func TestUnfinishedBarsAreDroppedAtExactBoundary(t *testing.T) {
	cal := testCalendar(t)
	all := minuteBars(t, cal, rbKey, 20260903, 20260908)
	open := cst(2026, 9, 7, 9, 30) // 这一根 TsEnd = 09:31
	cells := []struct {
		name    string
		now     int64
		wantEnd int64 // 结果里最后一根的 TsEnd
	}{
		{"now 恰为收盘 ⇒ 这一根算完结", open + 60000, open + 60000},
		{"now 比收盘早 1ms ⇒ 这一根丢掉", open + 59999, open},
	}
	for _, ce := range cells {
		fs := newFakeServer(t)
		fs.series["SHFE.rb2601"] = all
		c, _ := New(fs.config(cal, ce.now))
		bars, err := c.Bars(context.Background(), rbReq(2601, 20260904, 20260907))
		if err != nil {
			t.Fatalf("%s：%v", ce.name, err)
		}
		if len(bars) == 0 || bars[len(bars)-1].TsEnd != ce.wantEnd {
			got := int64(0)
			if len(bars) > 0 {
				got = bars[len(bars)-1].TsEnd
			}
			t.Errorf("%s：末根 TsEnd=%s，应为 %s", ce.name, fmtTs(got), fmtTs(ce.wantEnd))
		}
		if err := tickflow.CheckBars(rbReq(2601, 20260904, 20260907), bars, ce.now); err != nil {
			t.Errorf("%s：CheckBars：%v", ce.name, err)
		}
	}
}

// —— 日历放不下的根 ⇒ 报错，不丢 ——

func TestBarTheCalendarCannotPlaceIsAnError(t *testing.T) {
	cal := testCalendar(t)
	good := minuteBars(t, cal, rbKey, 20260904, 20260904)
	lunch := fakeBar{dt: cst(2026, 9, 4, 12, 0) * 1e6, open: 1, high: 1, low: 1, close: 1}
	var withLunch []fakeBar
	for _, b := range good {
		if b.dt > lunch.dt && len(withLunch) > 0 && withLunch[len(withLunch)-1].dt < lunch.dt {
			withLunch = append(withLunch, lunch)
		}
		withLunch = append(withLunch, b)
	}
	fs := newFakeServer(t)
	fs.series["SHFE.rb2601"] = withLunch
	c, _ := New(fs.config(cal, farFuture))
	_, err := c.Bars(context.Background(), rbReq(2601, 20260904, 20260904))
	if !errors.Is(err, ErrCalendarDisagrees) || !errors.Is(err, tickflow.ErrClosed) {
		t.Fatalf("午休 12:00 的一根应报 ErrCalendarDisagrees（链里带 ErrClosed）：%v", err)
	}
}

// 一根跨过所在时段收盘的根（开在 10:14:30，收在 10:15:30）⇒ 报错。
// ⛔ 与上面午休那一格不是同一条路：午休那根在 DayAt 就被拒（ErrClosed），这一根 DayAt 答得出来、要靠「收盘不越过时段」那一格。
func TestBarCrossingSessionCloseIsAnError(t *testing.T) {
	cal := testCalendar(t)
	good := minuteBars(t, cal, rbKey, 20260904, 20260904)
	odd := fakeBar{dt: (cst(2026, 9, 4, 10, 14) + 30000) * 1e6, open: 1, high: 1, low: 1, close: 1}
	var rows []fakeBar
	for _, b := range good {
		if b.dt > odd.dt && len(rows) > 0 && rows[len(rows)-1].dt < odd.dt {
			rows = append(rows, odd)
		}
		rows = append(rows, b)
	}
	fs := newFakeServer(t)
	fs.series["SHFE.rb2601"] = rows
	c, _ := New(fs.config(cal, farFuture))
	_, err := c.Bars(context.Background(), rbReq(2601, 20260904, 20260904))
	if !errors.Is(err, ErrCalendarDisagrees) || !strings.Contains(err.Error(), "越过了它所在时段的收盘") {
		t.Fatalf("10:14:30 开、10:15:30 收的一根应报「越过时段收盘」：%v", err)
	}
}

// —— 鉴权 ——

func TestHandshakeRejectedOnceRefreshesToken(t *testing.T) {
	fs := newFakeServer(t)
	cal := testCalendar(t)
	fs.series["SHFE.rb2601"] = minuteBars(t, cal, rbKey, 20260903, 20260904)
	fs.rejectFirstTok = true
	c, _ := New(fs.config(cal, farFuture))
	bars, err := c.Bars(context.Background(), rbReq(2601, 20260904, 20260904))
	if err != nil || len(bars) != 345 {
		t.Fatalf("握手 401 一次之后应重取 token 再拨：bars=%d err=%v", len(bars), err)
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if fs.authHits != 2 || fs.handshakes != 2 {
		t.Errorf("token 取了 %d 次、握手 %d 次，应为 2 / 2", fs.authHits, fs.handshakes)
	}
}

// 握手经注入的 client：token 缓存之后，每次 Bars 仍然经过它至少一次（probe.md 6.19）。
// ⛔ 必须数到【token 已缓存】之后那一次：第一次 Bars 里取 token、问名称服务也走这个 client，
// 只数第一次的话，握手不走它也照样有计数（变异验收 M2 当场存活过）。
func TestHandshakeGoesThroughInjectedClient(t *testing.T) {
	fs := newFakeServer(t)
	cal := testCalendar(t)
	fs.series["SHFE.rb2601"] = minuteBars(t, cal, rbKey, 20260903, 20260908)
	rt := &countingRT{next: http.DefaultTransport}
	cfg := fs.config(cal, farFuture)
	cfg.HTTPClient = &http.Client{Transport: rt, Timeout: 5 * time.Second}
	c, _ := New(cfg)
	for i, d := range []tickflow.TradingDay{20260904, 20260907} {
		before := rt.load()
		if _, err := c.Bars(context.Background(), rbReq(2601, d, d)); err != nil {
			t.Fatal(err)
		}
		got := rt.load() - before
		want := 1 // 第二次：token 与 mdurl 已缓存，只剩握手
		if i == 0 {
			want = 3 // token ＋ 名称服务 ＋ 握手
		}
		if got != want {
			t.Errorf("第 %d 次 Bars 经注入的 client %d 次，应为 %d", i+1, got, want)
		}
	}
}

func TestTokenIsCachedAcrossBars(t *testing.T) {
	fs := newFakeServer(t)
	cal := testCalendar(t)
	fs.series["SHFE.rb2601"] = minuteBars(t, cal, rbKey, 20260903, 20260908)
	c, _ := New(fs.config(cal, farFuture))
	for _, d := range []tickflow.TradingDay{20260904, 20260907} {
		if _, err := c.Bars(context.Background(), rbReq(2601, d, d)); err != nil {
			t.Fatal(err)
		}
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if fs.authHits != 1 || fs.nsHits != 1 || fs.handshakes != 2 {
		t.Errorf("两次 Bars：token %d 次、名称服务 %d 次、握手 %d 次，应为 1 / 1 / 2（token 缓存，连接每次新拨）",
			fs.authHits, fs.nsHits, fs.handshakes)
	}
}

func TestAuthFailureNamesTheLikelyCauseWithoutEchoingSecret(t *testing.T) {
	fs := newFakeServer(t)
	cal := testCalendar(t)
	fs.authStatus = http.StatusUnauthorized
	cfg := fs.config(cal, farFuture)
	c, _ := New(cfg)
	_, err := c.Bars(context.Background(), rbReq(2601, 20260904, 20260904))
	if !errors.Is(err, ErrAuth) || !strings.Contains(err.Error(), "client_secret") {
		t.Fatalf("token 端点 401 应报 ErrAuth 并点名 client_secret：%v", err)
	}
	if strings.Contains(err.Error(), cfg.ClientSecret) {
		t.Errorf("报文里出现了 ClientSecret 的值（对端把它回显在响应体里）：%v", err)
	}
}

func TestUserAgentDefaultAndOverride(t *testing.T) {
	cal := testCalendar(t)
	for _, ua := range []string{"", "my-app/1.0"} {
		fs := newFakeServer(t)
		fs.series["SHFE.rb2601"] = minuteBars(t, cal, rbKey, 20260903, 20260904)
		cfg := fs.config(cal, farFuture)
		cfg.UserAgent = ua
		c, _ := New(cfg)
		if _, err := c.Bars(context.Background(), rbReq(2601, 20260904, 20260904)); err != nil {
			t.Fatal(err)
		}
		want := ua
		if want == "" {
			want = DefaultUserAgent
		}
		fs.mu.Lock()
		if len(fs.uas) != 2 || fs.uas[0] != want || fs.uas[1] != want {
			t.Errorf("UserAgent=%q：token 与握手发的是 %q，应都是 %q", ua, fs.uas, want)
		}
		fs.mu.Unlock()
	}
	if !strings.HasPrefix(DefaultUserAgent, "futures-tickflow-go/") {
		t.Errorf("DefaultUserAgent=%q 应以本库名开头", DefaultUserAgent)
	}
}

// —— 请求形状 ——

func TestContinuousIsRejectedWithoutTouchingNetwork(t *testing.T) {
	fs := newFakeServer(t)
	c, _ := New(fs.config(testCalendar(t), farFuture))
	_, err := c.Bars(context.Background(), rbReq(0, 20260904, 20260904))
	if !errors.Is(err, ErrContinuous) {
		t.Fatalf("YearMon=0 应报 ErrContinuous：%v", err)
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if fs.authHits+fs.nsHits+fs.handshakes != 0 {
		t.Errorf("主连应在连网之前拒掉，而对端收到了 %d/%d/%d 次请求", fs.authHits, fs.nsHits, fs.handshakes)
	}
}

func TestCZCEAsksForThreeDigitYearMonth(t *testing.T) {
	fs := newFakeServer(t)
	cal := testCalendar(t)
	taKey := tickflow.ProductKey{Exchange: tickflow.CZCE, Product: "TA"}
	fs.series["CZCE.TA701"] = minuteBars(t, cal, taKey, 20260903, 20260904)
	c, _ := New(fs.config(cal, farFuture))
	req := tickflow.BarRequest{Symbol: tickflow.Symbol{Exchange: tickflow.CZCE, Product: "TA", YearMon: 2701},
		Period: tickflow.MustIntraday(1), From: 20260904, To: 20260904}
	bars, err := c.Bars(context.Background(), req)
	if err != nil || len(bars) == 0 {
		t.Fatalf("CZCE.TA2701 应按 CZCE.TA701 去要（probe.md 6.20 E4）：bars=%d err=%v", len(bars), err)
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if got := fs.setCharts[0]["ins_list"]; got != "CZCE.TA701" {
		t.Errorf("ins_list=%v，应为 CZCE.TA701", got)
	}
}

func TestOtherPeriodsAreRejected(t *testing.T) {
	fs := newFakeServer(t)
	c, _ := New(fs.config(testCalendar(t), farFuture))
	for _, p := range []tickflow.Period{tickflow.MustIntraday(5), tickflow.Daily} {
		req := rbReq(2601, 20260904, 20260904)
		req.Period = p
		if _, err := c.Bars(context.Background(), req); err == nil || !strings.Contains(err.Error(), "不支持周期") {
			t.Errorf("周期 %v 应被拒：%v", p, err)
		}
	}
}

func TestCancelIsNotReportedAsNotReady(t *testing.T) {
	fs := newFakeServer(t)
	cal := testCalendar(t)
	cfg := fs.config(cal, farFuture)
	cfg.ReadTimeout = 5 * time.Second
	c, _ := New(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err := c.Bars(ctx, rbReq(9901, 20260904, 20260904)) // 永不就绪，等的过程中被取消
	if err == nil || errors.Is(err, ErrNotReady) {
		t.Fatalf("调用方取消应报取消，不是 ErrNotReady：%v", err)
	}
}

// —— 构造 ——

func TestNewRejectsEachMissingField(t *testing.T) {
	fs := newFakeServer(t)
	cal := testCalendar(t)
	cells := []struct {
		name   string
		break_ func(*Config)
		want   string
	}{
		{"User", func(c *Config) { c.User = "" }, "User 为空"},
		{"Password", func(c *Config) { c.Password = "" }, "Password 为空"},
		{"ClientID", func(c *Config) { c.ClientID = "" }, "ClientID 为空"},
		{"ClientSecret", func(c *Config) { c.ClientSecret = "" }, "ClientSecret 为空"},
		{"Calendar", func(c *Config) { c.Calendar = nil }, "Calendar 为 nil"},
		{"HTTPClient", func(c *Config) { c.HTTPClient = nil }, "HTTPClient 为 nil"},
		{"ReadTimeout 0", func(c *Config) { c.ReadTimeout = 0 }, "ReadTimeout=0s"},
		{"ReadTimeout 负", func(c *Config) { c.ReadTimeout = -1 }, "ReadTimeout=-1ns"},
	}
	// 标定格：完整的配置必须造得出来 —— 否则下面每一格的「报错」都可能是别的原因。
	if _, err := New(fs.config(cal, farFuture)); err != nil {
		t.Fatalf("标定格：完整配置造不出来：%v", err)
	}
	for _, ce := range cells {
		cfg := fs.config(cal, farFuture)
		ce.break_(&cfg)
		_, err := New(cfg)
		if err == nil || !strings.Contains(err.Error(), ce.want) {
			t.Errorf("%s：%v，应含 %q", ce.name, err, ce.want)
		}
	}
}

func TestCapsValidateAndDeclareHTTP(t *testing.T) {
	fs := newFakeServer(t)
	c, _ := New(fs.config(testCalendar(t), farFuture))
	caps := c.Caps(rbKey)
	if err := caps.Validate(); err != nil {
		t.Fatalf("Caps 自己不自洽：%v", err)
	}
	if caps.ClientUse != tickflow.ClientUseHTTP || caps.BatchDays != 20 || !caps.Supports(tickflow.MustIntraday(1)) {
		t.Errorf("Caps = %+v，应为 ClientUseHTTP / BatchDays 20 / 支持 1m", caps)
	}
}
