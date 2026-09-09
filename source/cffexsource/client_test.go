package cffexsource

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/embedded"
)

// dayServer 用本包那份真实响应当后端，并**记下每一次请求的路径**。
//
// ⚠️ 记路径是这一片的重点：本源**一天一个请求**，所以要核的不只是「拿回了什么」，
// 更是**「发了几次、发的哪几天」** —— 多发一次是白打，少发一次是缺口，
// 而两者在返回值上都看不出来。
type dayServer struct {
	*httptest.Server
	paths  []string
	status map[string]int // 按路径给特定状态码
	body   map[string]string
}

func newDayServer(t *testing.T) *dayServer {
	t.Helper()
	ds := &dayServer{status: map[string]int{}, body: map[string]string{}}
	real := read(t, "daily_20260908.xml")
	ds.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ds.paths = append(ds.paths, r.URL.Path)
		if code, ok := ds.status[r.URL.Path]; ok {
			w.WriteHeader(code)
			return
		}
		if b, ok := ds.body[r.URL.Path]; ok {
			_, _ = w.Write([]byte(b))
			return
		}
		// 默认：把那份真实响应的交易日改写成 URL 里那一天，
		// 好让「XML 自报的交易日」与日历对得上（那道交叉核对是活的）。
		day := dayFromPath(r.URL.Path)
		// 替换**整段元素**，不是替换那个日期串：
		// 实测 fixture 里 "20260908" 共 714 处、全部在 <tradingday> 里 ⇒ 今天两种写法等价，
		// **而那是被【数据】保证的，不是被【写法】保证的** —— 换一份 fixture 就可能不再等价。
		_, _ = w.Write([]byte(strings.ReplaceAll(string(real),
			"<tradingday>20260908</tradingday>", "<tradingday>"+day+"</tradingday>")))
	}))
	t.Cleanup(ds.Close)
	return ds
}

// dayFromPath 从 /202609/07/index.xml 取出 20260907。
func dayFromPath(p string) string {
	parts := strings.Split(strings.Trim(p, "/"), "/")
	if len(parts) < 3 {
		return ""
	}
	return parts[len(parts)-3] + parts[len(parts)-2]
}

func newTestClient(t *testing.T, ds *dayServer, days []tickflow.TradingDay) *Client {
	t.Helper()
	cal, err := embedded.New(days)
	if err != nil {
		t.Fatalf("造日历：%v", err)
	}
	c, err := New(cal, WithBaseURL(ds.URL), WithClock(func() int64 { return nowAfter }))
	if err != nil {
		t.Fatalf("New：%v", err)
	}
	return c
}

func icReq(from, to tickflow.TradingDay) tickflow.BarRequest {
	return tickflow.BarRequest{Symbol: icSym(), Period: tickflow.Daily, From: from, To: to}
}

// —— 一、发了几次、发的哪几天 ——

// TestOneRequestPerTradingDay 是这一片的头号断言。
//
// 三个交易日 ⇒ **恰好三次请求，且正是那三天** ——
// 多一次是白打，少一次是缺口，而**返回值上都看不出来**。
func TestOneRequestPerTradingDay(t *testing.T) {
	days := []tickflow.TradingDay{20260904, 20260907, 20260908}
	ds := newDayServer(t)
	c := newTestClient(t, ds, days)

	bars, err := c.Bars(context.Background(), icReq(20260904, 20260908))
	if err != nil {
		t.Fatalf("Bars：%v", err)
	}
	if len(ds.paths) != len(days) {
		t.Fatalf("请求次数 %d，期望 %d：%v", len(ds.paths), len(days), ds.paths)
	}
	var got []string
	for _, p := range ds.paths {
		got = append(got, dayFromPath(p))
	}
	sort.Strings(got)
	want := []string{"20260904", "20260907", "20260908"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("请求的日子是 %v，期望 %v", got, want)
		}
	}
	if len(bars) != len(days) {
		t.Fatalf("拿到 %d 根，期望 %d 根（每个交易日一根）", len(bars), len(days))
	}
	// 契约由测试拿 CheckBars 去核，不由实现自己核。
	if err := tickflow.CheckBars(icReq(20260904, 20260908), bars, nowAfter); err != nil {
		t.Fatalf("产物不满足 Source 契约：%v", err)
	}
	t.Logf("%d 个交易日 ⇒ %d 次请求 ⇒ %d 根", len(days), len(ds.paths), len(bars))
}

// TestWeekendIsNotRequested 钉住「走对日子」那一半。
//
// 2026-09-05/06 是周末：日历里没有它们 ⇒ **一次请求都不该发**。
// ⇒ 这一条挡的是「按自然日铺开」那种写法 —— 它的返回值和正确实现一模一样，
// 只是**多打了两次网络请求**，而那在结果里看不见。
func TestWeekendIsNotRequested(t *testing.T) {
	ds := newDayServer(t)
	c := newTestClient(t, ds, []tickflow.TradingDay{20260904, 20260907, 20260908})

	if _, err := c.Bars(context.Background(), icReq(20260904, 20260908)); err != nil {
		t.Fatalf("%v", err)
	}
	for _, p := range ds.paths {
		if d := dayFromPath(p); d == "20260905" || d == "20260906" {
			t.Fatalf("请求了周末 %s —— 日历里没有它，说明区间是按自然日铺的", d)
		}
	}
	if len(ds.paths) != 3 {
		t.Fatalf("请求次数 %d，期望 3", len(ds.paths))
	}
}

// —— 二、四条不静默 ——

// TestUncoveredRangeIsRefusedBeforeAnyRequest 是 Walk 那条护栏的兑现。
//
// ⛔ 区间落在日历覆盖之外 ⇒ **当场炸，而且一次请求都不发**。
// 静默少遍历的后果是缺口，而缺口会被 coverage 记成「拉过、确认没有」。
func TestUncoveredRangeIsRefusedBeforeAnyRequest(t *testing.T) {
	ds := newDayServer(t)
	c := newTestClient(t, ds, []tickflow.TradingDay{20260907, 20260908})

	bars, err := c.Bars(context.Background(), icReq(20200101, 20260908))
	if err == nil {
		t.Fatalf("区间超出日历覆盖，应当报错，却给了 %d 根", len(bars))
	}
	if len(ds.paths) != 0 {
		t.Fatalf("被拒的请求不该发出去，而服务端收到了 %d 次：%v", len(ds.paths), ds.paths)
	}
}

// TestMidwayFailureStopsAndReturnsNothing 钉住「不返回半截」。
func TestMidwayFailureStopsAndReturnsNothing(t *testing.T) {
	days := []tickflow.TradingDay{20260904, 20260907, 20260908}
	ds := newDayServer(t)
	c := newTestClient(t, ds, days)
	ds.status["/202609/07/index.xml"] = http.StatusInternalServerError

	bars, err := c.Bars(context.Background(), icReq(20260904, 20260908))
	if err == nil {
		t.Fatalf("中途 500 应当报错，却给了 %d 根", len(bars))
	}
	if len(bars) != 0 {
		t.Fatalf("报错的同时还给了 %d 根 —— 半截结果会被记成「拉过、确认没有」", len(bars))
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("错误里应当带状态码：%v", err)
	}
	// 中止之后不该继续打后面的日子
	last := dayFromPath(ds.paths[len(ds.paths)-1])
	if last != "20260907" {
		t.Errorf("失败后仍继续请求到 %s —— 应当当场中止", last)
	}
}

func TestNonCFFEXIsRejectedBeforeAnyRequest(t *testing.T) {
	ds := newDayServer(t)
	c := newTestClient(t, ds, []tickflow.TradingDay{20260908})
	req := icReq(20260908, 20260908)
	req.Symbol = tickflow.Symbol{Exchange: tickflow.SHFE, Product: "rb", YearMon: 2610}

	if _, err := c.Bars(context.Background(), req); !errors.Is(err, ErrNotCFFEX) {
		t.Fatalf("非中金所应当被拒，得到 %v", err)
	}
	if len(ds.paths) != 0 {
		t.Fatalf("被拒的请求不该发出去，服务端收到 %d 次", len(ds.paths))
	}
}

func TestIntradayIsRejected(t *testing.T) {
	ds := newDayServer(t)
	c := newTestClient(t, ds, []tickflow.TradingDay{20260908})
	req := icReq(20260908, 20260908)
	req.Period = tickflow.MustIntraday(1)

	if _, err := c.Bars(context.Background(), req); err == nil ||
		!strings.Contains(err.Error(), "不支持周期") {
		t.Fatalf("这份 XML 是日行情，分钟线应当被拒，得到 %v", err)
	}
	if len(ds.paths) != 0 {
		t.Errorf("被拒的请求不该发出去，服务端收到 %d 次", len(ds.paths))
	}
}

func TestNewRequiresCalendar(t *testing.T) {
	if _, err := New(nil); err == nil {
		t.Fatal("没有日历时 New 应当报错 —— 它还决定要发哪几天的请求")
	}
}

// —— 三、Caps 与 Bars 不能分叉（沿用 sinasource 那条 ⟺ 的形状）——

func TestCapsAndBarsAgreeOnPeriods(t *testing.T) {
	ds := newDayServer(t)
	c := newTestClient(t, ds, []tickflow.TradingDay{20260908})
	caps := c.Caps(icSym().ProductKey())

	candidates := []tickflow.Period{
		tickflow.Daily, tickflow.Weekly, tickflow.Monthly,
		tickflow.MustIntraday(1), tickflow.MustIntraday(60),
	}
	supported, rejected := 0, 0
	for _, p := range candidates {
		req := icReq(20260908, 20260908)
		req.Period = p
		_, err := c.Bars(context.Background(), req)
		byPeriod := err != nil && strings.Contains(err.Error(), "不支持周期")
		switch {
		case caps.Supports(p) && err != nil:
			t.Errorf("Caps 说支持 %s，而 Bars 拿不到数据：%v", p, err)
		case !caps.Supports(p) && !byPeriod:
			t.Errorf("Caps 没说支持 %s，而 Bars 没有因周期拒绝它（err=%v）", p, err)
		}
		if caps.Supports(p) {
			supported++
		} else {
			rejected++
		}
	}
	if supported == 0 || rejected == 0 {
		t.Fatalf("候选集退化了：支持 %d / 拒绝 %d —— 两侧都得有", supported, rejected)
	}
}

func TestCapsIsSelfConsistent(t *testing.T) {
	ds := newDayServer(t)
	c := newTestClient(t, ds, []tickflow.TradingDay{20260908})
	caps := c.Caps(icSym().ProductKey())
	if err := caps.Validate(); err != nil {
		t.Errorf("Caps 自己不自洽：%v", err)
	}
	if !caps.HasSettle {
		t.Error("HasSettle 应当为 true —— 本源存在的全部理由就是它")
	}
	since, ok := caps.Since[tickflow.Daily]
	if !ok || !since.Valid() {
		t.Errorf("没给出合法的日线起点：ok=%v since=%d", ok, int32(since))
	}
	// 起点是【实测的下界】：2016-01-04 那天的存档取得到（18 条期货、0 条期权）。
	if since != 20160104 {
		t.Errorf("Since = %s，而实测的下界是 2016-01-04", since)
	}
}

// —— 四、真网：默认跳过，它是【探针】不是【测试】 ——

// TestProbe_LiveCFFEXArchive 打真接口。**默认跳过**。
//
// ⛔ 它是探针不是测试（分类决定该拿哪条判据衡量它）：
// **探针断言【外部世界现在什么样】；测试断言【本库的行为】。**
// 而本源是中金所结算价的唯一来源，probe.md 记着「四家官网三家被 WAF 挡」——
// **中金所今天开着不代表明天开着**，所以这条探针有存在的必要。
//
// 跑：`CFFEXSOURCE_LIVE=1 go test ./source/cffexsource/ -run Probe -v`
func TestProbe_LiveCFFEXArchive(t *testing.T) {
	if os.Getenv("CFFEXSOURCE_LIVE") == "" {
		t.Skip("未设 CFFEXSOURCE_LIVE=1，跳过真网探针")
	}
	cal, err := embedded.New([]tickflow.TradingDay{20260908})
	if err != nil {
		t.Fatalf("%v", err)
	}
	c, err := New(cal, WithClock(func() int64 { return time.Now().UnixMilli() }))
	if err != nil {
		t.Fatalf("%v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	bars, err := c.Bars(ctx, icReq(20260908, 20260908))
	if err != nil {
		t.Fatalf("真网取 2026-09-08 失败：%v", err)
	}
	if len(bars) != 1 {
		t.Fatalf("期望 1 根，得到 %d", len(bars))
	}
	if !bars[0].HasSettle() {
		t.Fatal("真网数据没有结算价 —— 本源存在的理由没了")
	}
	t.Logf("真网：IC2609 %s 结=%v", bars[0].TradingDay, bars[0].Settle)
}

// TestSinceIsArchiveLevelAndKnowinglyWrongForLateProducts 钉住登记㉔ 的【射程】。
//
// ⛔ 它断言的是「八个品种拿到同一个值」—— **而那正是缺陷本身，不是它被修好了。**
// 名字里写着 KnowinglyWrong，因为**绿色测试是被【按名字】读的**：
// 输出里只剩一行名字，说明写得再全也在摘要之外。
//
//	Capabilities.Since 的契约是【按品种】给（source.go）
//	而本源给的是【存档级】下界：2016-01-04 那天只有 IC/IF/IH/T/TF 五个品种
//	⇒ 对 IM / TS / TL，这个值**早于它们上市**
//
// ⚠️ 而它在本源上的代价不是「差一点」：一天一个请求 ⇒
// 照它回补一个后上市的品种是**几千次请求换 0 根，且 err == nil**（⑨×⑯ 的落地）。
//
// ⇒ 修好它需要【上市日】= refdata（v0.4）。那一天到了，这条测试会红，
// 而**红了之后要做的是把它改成「按品种各不相同」，并关掉㉔**。
func TestSinceIsArchiveLevelAndKnowinglyWrongForLateProducts(t *testing.T) {
	ds := newDayServer(t)
	c := newTestClient(t, ds, []tickflow.TradingDay{20260908})

	// 2016-01-04 那天真实存在的五个（本包 client.go 的注释与探针读数）
	early := []string{"IC", "IF", "IH", "T", "TF"}
	// 后上市的三个 —— 对它们，这个 Since 是明知的错
	late := []string{"IM", "TS", "TL"}

	seen := map[tickflow.TradingDay]int{}
	for _, prod := range append(append([]string{}, early...), late...) {
		k := tickflow.ProductKey{Exchange: tickflow.CFFEX, Product: prod}
		since, ok := c.Caps(k).Since[tickflow.Daily]
		if !ok || !since.Valid() {
			t.Fatalf("%s 没给出合法起点", prod)
		}
		seen[since]++
	}
	if len(seen) != 1 {
		t.Fatalf("Since 已经按品种分开了（%d 个不同的值）——"+
			"若这是有意的，请把这条测试改成逐品种断言，并关掉登记㉔：%v", len(seen), seen)
	}
	if n := seen[cffexSince]; n != len(early)+len(late) {
		t.Fatalf("八个品种应当都拿到存档级下界 %s，实得 %d 个", cffexSince, n)
	}
	t.Logf("八个品种同拿 %s —— 其中 %v 三个【早于上市】，这是登记㉔ 的已知缺陷",
		cffexSince, late)
}
