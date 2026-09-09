package sinasource

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
)

// —— 一、符号映射：本片最容易静默出错的一格 ——

// TestSinaSymbolIsNotInstrument 钉住那条实测出来的分岔。
//
// `Symbol.Instrument()` 给的是【交易所线】格式，郑商所在那边是**三位**年月（TA701），
// 而新浪只认**四位**（TA2701）。用错的后果不是报错，是
// **新浪回 `var _=(null)` ⇒ 解析层报「不认识这个合约」** ——
// 一个确实存在的郑商所合约，得到「不存在」这个答案。
func TestSinaSymbolIsNotInstrument(t *testing.T) {
	czce := tickflow.Symbol{Exchange: tickflow.CZCE, Product: "TA", YearMon: 2701}
	if got, inst := SinaSymbol(czce), czce.Instrument(); got == inst {
		t.Fatalf("郑商所上两者应当【不同】：SinaSymbol=%q Instrument=%q\n"+
			"  ⇒ 若它们相同，说明有人把 SinaSymbol 改成了转发 Instrument()，"+
			"而那会让整个郑商所拿不到数据", got, inst)
	} else {
		t.Logf("CZCE：SinaSymbol=%q（新浪认）vs Instrument=%q（新浪回 null）", got, inst)
	}
}

func TestSinaSymbol(t *testing.T) {
	cases := []struct {
		sym  tickflow.Symbol
		want string
	}{
		{tickflow.Symbol{Exchange: tickflow.SHFE, Product: "rb", YearMon: 2610}, "RB2610"},
		{tickflow.Symbol{Exchange: tickflow.CZCE, Product: "TA", YearMon: 2701}, "TA2701"},
		{tickflow.Symbol{Exchange: tickflow.CFFEX, Product: "T", YearMon: 2612}, "T2612"},
		{tickflow.Symbol{Exchange: tickflow.DCE, Product: "m", YearMon: 2701}, "M2701"},
		// 补零：个位月份不能变成三位
		{tickflow.Symbol{Exchange: tickflow.INE, Product: "sc", YearMon: 601}, "SC0601"},
	}
	for _, c := range cases {
		if got := SinaSymbol(c.sym); got != c.want {
			t.Errorf("SinaSymbol(%s) = %q，期望 %q", c.sym, got, c.want)
		}
	}
}

// —— 二、跑通整条路，但不打真网 ——

// fixtureServer 用本包 testdata 里那三份【真实响应】当后端。
//
// ⚠️ 它同时**记下收到的请求**，好让测试核「我们发出去的是什么」——
// 只核返回值的话，符号拼错但服务端恰好也认，就看不出来了。
type fixtureServer struct {
	*httptest.Server
	gotSymbol  string
	gotReferer string
	status     int
	body       string
}

func newFixtureServer(t *testing.T) *fixtureServer {
	t.Helper()
	fs := &fixtureServer{status: http.StatusOK}
	files := map[string]string{
		"RB2610": "daily_RB2610.jsonp",
		"TA2701": "daily_TA2701.jsonp",
		"T2612":  "daily_T2612.jsonp",
	}
	fs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fs.gotSymbol = r.URL.Query().Get("symbol")
		fs.gotReferer = r.Header.Get("Referer")
		if fs.status != http.StatusOK {
			w.WriteHeader(fs.status)
			return
		}
		if fs.body != "" {
			_, _ = w.Write([]byte(fs.body))
			return
		}
		f, ok := files[fs.gotSymbol]
		if !ok {
			_, _ = w.Write(read(t, "daily_null.jsonp")) // 未知合约：照真接口的形态回 null
			return
		}
		_, _ = w.Write(read(t, f))
	}))
	t.Cleanup(fs.Close)
	return fs
}

func newTestClient(t *testing.T, fs *fixtureServer) *Client {
	t.Helper()
	c, err := New(testCal(t),
		WithBaseURL(fs.URL),
		WithClock(func() int64 { return nowAfter }))
	if err != nil {
		t.Fatalf("New：%v", err)
	}
	return c
}

func TestBarsEndToEnd(t *testing.T) {
	fs := newFixtureServer(t)
	c := newTestClient(t, fs)
	req := rbReq()

	bars, err := c.Bars(context.Background(), req)
	if err != nil {
		t.Fatalf("Bars：%v", err)
	}
	if len(bars) == 0 {
		t.Fatal("一根都没拿到 —— 下面那句 CheckBars 会在空切片上恒绿")
	}
	if err := tickflow.CheckBars(req, bars, nowAfter); err != nil {
		t.Fatalf("拿回来的东西不满足 Source 契约：%v", err)
	}

	// 核【发出去的】，不只核拿回来的
	if fs.gotSymbol != "RB2610" {
		t.Errorf("发出去的 symbol=%q，期望 RB2610", fs.gotSymbol)
	}
	if fs.gotReferer != DefaultReferer {
		t.Errorf("Referer=%q，期望 %q —— 少了它新浪不给数据", fs.gotReferer, DefaultReferer)
	}
	t.Logf("拿到 %d 根，symbol=%q referer=%q", len(bars), fs.gotSymbol, fs.gotReferer)
}

// TestBarsAsksForFourDigitCZCE 是符号那一格的**端到端**一侧。
//
// 上面那条只比了两个函数的返回值；这一条核的是**真正发出去的那个查询串**。
func TestBarsAsksForFourDigitCZCE(t *testing.T) {
	fs := newFixtureServer(t)
	c := newTestClient(t, fs)
	req := rbReq()
	req.Symbol = tickflow.Symbol{Exchange: tickflow.CZCE, Product: "TA", YearMon: 2701}

	if _, err := c.Bars(context.Background(), req); err != nil {
		t.Fatalf("Bars：%v", err)
	}
	if fs.gotSymbol != "TA2701" {
		t.Fatalf("发出去的是 %q —— 新浪对三位码 TA701 回 null，整个郑商所会拿不到数据", fs.gotSymbol)
	}
}

// —— 三、四条不静默 ——

func TestNon200IsAnErrorNotEmpty(t *testing.T) {
	fs := newFixtureServer(t)
	fs.status = http.StatusInternalServerError
	c := newTestClient(t, fs)

	bars, err := c.Bars(context.Background(), rbReq())
	if err == nil {
		t.Fatalf("HTTP 500 应当报错，却给了 %d 根 —— 空结果会被 coverage 记成「拉过、确认没有」", len(bars))
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("错误信息里应当带状态码，实际：%v", err)
	}
}

func TestUnknownSymbolSurfacesAsSuch(t *testing.T) {
	fs := newFixtureServer(t)
	c := newTestClient(t, fs)
	req := rbReq()
	req.Symbol = tickflow.Symbol{Exchange: tickflow.SHFE, Product: "zz", YearMon: 9999}

	_, err := c.Bars(context.Background(), req)
	if !errors.Is(err, ErrUnknownSymbol) {
		t.Fatalf("服务端回 null 时应当报 ErrUnknownSymbol，得到 %v", err)
	}
}

func TestContextCancelStopsIt(t *testing.T) {
	fs := newFixtureServer(t)
	c := newTestClient(t, fs)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 先取消再调，确保它真的看 ctx

	_, err := c.Bars(ctx, rbReq())
	if err == nil {
		t.Fatal("ctx 已取消，Bars 应当报错")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("应当能 errors.Is 到 context.Canceled，实际：%v", err)
	}
}

func TestIntradayIsRejected(t *testing.T) {
	fs := newFixtureServer(t)
	c := newTestClient(t, fs)
	req := rbReq()
	req.Period = tickflow.MustIntraday(1)

	if _, err := c.Bars(context.Background(), req); err == nil ||
		!strings.Contains(err.Error(), "只给日线") {
		t.Fatalf("分钟线应当被明确拒绝（新浪 1023 硬顶、深度走天勤），得到 %v", err)
	}
	if fs.gotSymbol != "" {
		t.Errorf("被拒的请求不该发出去，而服务端收到了 symbol=%q", fs.gotSymbol)
	}
}

func TestNewRequiresCalendar(t *testing.T) {
	if _, err := New(nil); err == nil {
		t.Fatal("没有日历时 New 应当报错 —— Ts/TsEnd 与「已完结」都靠它，不猜")
	}
}

// —— 四、Caps 的声明要和数据对得上 ——

// TestCapsSettleMatchesTheFixtures 把 Caps 的声明拿【数据】核一遍。
//
// ⚠️ 期望值**不写死**：从 fixture 里现算「有几根有结算价」，再和 Caps 的声明比。
// 写死的话，这条测的就是「我抄对了没有」，而不是「声明和数据一致没有」。
func TestCapsSettleMatchesTheFixtures(t *testing.T) {
	fs := newFixtureServer(t)
	c := newTestClient(t, fs)

	cases := []struct {
		file string
		sym  tickflow.Symbol
	}{
		{"daily_RB2610.jsonp", tickflow.Symbol{Exchange: tickflow.SHFE, Product: "rb", YearMon: 2610}},
		{"daily_TA2701.jsonp", tickflow.Symbol{Exchange: tickflow.CZCE, Product: "TA", YearMon: 2701}},
		{"daily_T2612.jsonp", tickflow.Symbol{Exchange: tickflow.CFFEX, Product: "T", YearMon: 2612}},
	}
	for _, tc := range cases {
		t.Run(tc.sym.Exchange, func(t *testing.T) {
			rows, err := ParseDaily(read(t, tc.file))
			if err != nil {
				t.Fatalf("%v", err)
			}
			if len(rows) == 0 {
				t.Fatal("fixture 是空的 —— 下面的比较恒真")
			}
			withSettle := 0
			for _, r := range rows {
				if !math.IsNaN(r.Settle) {
					withSettle++
				}
			}
			claimed := c.Caps(tc.sym.ProductKey()).HasSettle
			actual := withSettle > 0
			if claimed != actual {
				t.Fatalf("Caps 声称 HasSettle=%v，而 fixture 里 %d/%d 根有结算价",
					claimed, withSettle, len(rows))
			}
			t.Logf("%s：声称 %v，数据 %d/%d 根有结算价 ⇒ 一致", tc.sym.Exchange, claimed, withSettle, len(rows))
		})
	}
}

func TestCapsIsSelfConsistent(t *testing.T) {
	fs := newFixtureServer(t)
	c := newTestClient(t, fs)
	for _, k := range []tickflow.ProductKey{
		{Exchange: tickflow.SHFE, Product: "rb"},
		{Exchange: tickflow.CFFEX, Product: "T"},
	} {
		caps := c.Caps(k)
		if err := caps.Validate(); err != nil {
			t.Errorf("%s 的 Caps 自己不自洽：%v", k, err)
		}
		if !caps.Supports(tickflow.Daily) {
			t.Errorf("%s 的 Caps 不支持日线，而 Bars 只给日线", k)
		}
		if caps.Supports(tickflow.MustIntraday(1)) {
			t.Errorf("%s 的 Caps 声称支持分钟线，而 Bars 会拒绝它", k)
		}
		if caps.Realtime {
			t.Errorf("%s 的 Caps 声称有实时 —— 新浪实时接口自 2024-07-17 冻结，本包不接", k)
		}
	}
}

// —— 五、真网：默认不跑 ——

// TestLiveDailyAgainstSina 打真接口。**默认跳过**，靠环境变量开。
//
// ⚠️ 它不进常规回归，理由是本仓那条：一个依赖外网的测试**会因为与被测代码无关的原因红**，
// 而那种红最终会让人给整条测试加跳过。
// ⇒ 要跑：`SINASOURCE_LIVE=1 go test ./source/sinasource/ -run Live -v`
func TestLiveDailyAgainstSina(t *testing.T) {
	if os.Getenv("SINASOURCE_LIVE") == "" {
		t.Skip("未设 SINASOURCE_LIVE=1，跳过真网测试")
	}
	c, err := New(testCal(t), WithClock(func() int64 { return time.Now().UnixMilli() }))
	if err != nil {
		t.Fatalf("New：%v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	req := rbReq()
	bars, err := c.Bars(ctx, req)
	if err != nil {
		t.Fatalf("真网拉取失败：%v", err)
	}
	if len(bars) == 0 {
		t.Fatal("真网拿到 0 根")
	}
	if err := tickflow.CheckBars(req, bars, time.Now().UnixMilli()); err != nil {
		t.Fatalf("真网数据不满足 Source 契约：%v", err)
	}
	t.Logf("真网拿到 %d 根，首 %s 末 %s", len(bars), bars[0].TradingDay, bars[len(bars)-1].TradingDay)
}
