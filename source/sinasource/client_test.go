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
		!strings.Contains(err.Error(), "不支持周期") {
		t.Fatalf("分钟线应当被明确拒绝（新浪 1023 硬顶、深度走天勤），得到 %v", err)
	}
	if fs.gotSymbol != "" {
		t.Errorf("被拒的请求不该发出去，而服务端收到了 symbol=%q", fs.gotSymbol)
	}
}

// TestCapsAndBarsAgreeOnPeriods 是登记⑭ 的处置：**把两处副本变成一处定义 ＋ 一条 ⟺ 断言。**
//
// ⛔ 原来 Bars 里一份周期闸、Caps 里一份 Periods、两条测试各自钉在【测试里的第三份字面量】上，
// 没有任何一处由另一处推出来。评审方 2026-09-09 的对照组：
// **让 Bars 也收 Weekly 而 Caps 不变 ⇒ 一条测试都不红**（我复现过 rc=0）。
//
// ⇒ 这一条对一组候选周期逐个断言：**Caps().Supports(p) ⟺ Bars 不因周期而拒绝**。
// 加第三种周期时，只要两边不一致，它就会红。
func TestCapsAndBarsAgreeOnPeriods(t *testing.T) {
	fs := newFixtureServer(t)
	c := newTestClient(t, fs)
	k := rbReq().Symbol.ProductKey()
	caps := c.Caps(k)

	candidates := []tickflow.Period{
		tickflow.Daily, tickflow.Weekly, tickflow.Monthly,
		tickflow.MustIntraday(1), tickflow.MustIntraday(5), tickflow.MustIntraday(60),
	}
	supported, rejected := 0, 0
	for _, p := range candidates {
		req := rbReq()
		req.Period = p
		_, err := c.Bars(context.Background(), req)
		byPeriod := err != nil && strings.Contains(err.Error(), "不支持周期")

		// ⚠️ 断言的是【Caps 与真能力】，不是【Caps 与那道闸】。
		//
		// 只比「闸放不放行」会留一个洞：往 Caps 里多加一个周期，闸就放行了，
		// 而请求会在更后面（AssembleDaily）失败 —— ⟺ 仍然成立，**而这个源在说谎**。
		// ⇒ 所以支持的那一侧要求【整条路走通】，不只是「没被闸挡住」。
		switch {
		case caps.Supports(p) && err != nil:
			t.Errorf("Caps 说支持 %s，而 Bars 拿不到数据：%v"+
				"  ⇒ 声称的能力必须真的给得出来，不只是「没被那道闸挡住」", p, err)
		case !caps.Supports(p) && !byPeriod:
			t.Errorf("Caps 没说支持 %s，而 Bars 没有因周期拒绝它（err=%v）——"+
				"**这正是那次静默分叉的形状**", p, err)
		}
		if caps.Supports(p) {
			supported++
		} else {
			rejected++
		}
	}
	// 两侧都要有样本，否则这条断言在退化的集合上恒真。
	if supported == 0 || rejected == 0 {
		t.Fatalf("候选集退化了：支持 %d 个 / 拒绝 %d 个 —— 两侧都得有，"+
			"否则 ⟺ 只验到了一半", supported, rejected)
	}
	t.Logf("候选 %d 个：支持 %d / 因周期拒绝 %d，两侧一致", len(candidates), supported, rejected)
}

// TestMainContinuousIsRejectedAsUnsupported 是登记⑮ 的处置。
//
// ⛔ 不拦的话：YearMon=0 ⇒ SinaSymbol 给 "RB0000" ⇒ 新浪答 null
// ⇒ ErrUnknownSymbol「新浪不认识这个合约」。
// **想要主连的人拿到的是「这个合约不存在」，而真相是「本源不做主连」。**
func TestMainContinuousIsRejectedAsUnsupported(t *testing.T) {
	fs := newFixtureServer(t)
	c := newTestClient(t, fs)
	req := rbReq()
	req.Symbol.YearMon = 0

	_, err := c.Bars(context.Background(), req)
	if err == nil {
		t.Fatal("YearMon=0（主连）应当被拒")
	}
	if errors.Is(err, ErrUnknownSymbol) {
		t.Fatalf("被报成「合约不存在」了 —— 这正是⑮ 要分开的那两件事：%v", err)
	}
	if !strings.Contains(err.Error(), "主力连续") {
		t.Fatalf("拒绝理由应当点明是主连，得到：%v", err)
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
		// Since 是【绝对起点】：它必须是一个合法交易日，而不是零值。
		// 这一格在旧形状（Duration）下查不出来 —— 任何 Duration 都「合法」，包括 0。
		if since, ok := caps.Since[tickflow.Daily]; !ok || !since.Valid() {
			t.Errorf("%s 的 Caps 没给出合法的日线起点：ok=%v since=%d", k, ok, int32(since))
		}
	}
}

// —— 五、真网：默认不跑 ——

// TestProbe_LiveDailyAgainstSina 是一个【探针】，不是一条【测试】。
//
// ⛔ 这个区分不是措辞，它决定该拿哪条规矩衡量它（评审方 2026-09-09 指出）：
//
//	测试  断言【本库的行为】     ⇒ 必过；一个会红的必过项迟早被加 `|| true`
//	探针  断言【外部世界现在什么样】⇒ **本来就不在必过集合里**，默认跳过是它的正常形态
//
// ⚠️ 我此前拿「测试」的判据去衡量它，于是在两条方向相反的规矩之间摇摆 ——
// **摇摆不是规矩冲突，是它被放进了错的那一栏。**
//
// ⇒ 要跑：`SINASOURCE_LIVE=1 go test ./source/sinasource/ -run Probe -v`
func TestProbe_LiveDailyAgainstSina(t *testing.T) {
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
