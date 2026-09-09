package sinasource

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
)

// 这一片是【拉取】：把前三片接成一个 tickflow.Source。
//
//	字节 ← HTTP（本片）
//	行   ← ParseDaily（片二）
//	Bar  ← AssembleDaily（片三，要日历）
//
// ⛔ **本片只做日线，且只做【具体合约】，不做主力连续。**
// 主连（`RB0` / `IF0`）在 tickflow.Symbol 里根本表达不出来 —— 它有 YearMon 而主连没有。
// 那需要先给 Symbol 一个表示形态，不属于这一片。**写下来是为了让「没做」和「做漏了」分得开。**

// —— 端点与符号 ——

// DefaultBaseURL 是新浪日线的 JSONP 端点（probe.md 第一节）。
const DefaultBaseURL = "https://stock2.finance.sina.com.cn/futures/api/jsonp.php/var%20_=/InnerFuturesNewService.getDailyKLine"

// DefaultReferer 少了它新浪不给数据。
const DefaultReferer = "https://finance.sina.com.cn"

// SinaSymbol 把 tickflow.Symbol 变成新浪认的那个符号。
//
// ⛔ **不要用 Symbol.Instrument()。** 那是【交易所线】格式，郑商所在那边是三位年月
// （`TA701`）—— 实测 2026-09-09：
//
//	RB2610 / rb2610 / Rb2610  ⇒ 221 根（**大小写不敏感**）
//	TA2701 / ta2701           ⇒ 156 根
//	**TA701**                 ⇒ **var _=(null)**  ⛔ 三位码新浪不认
//
// ⇒ 而 `null` 会被解析层报成 ErrUnknownSymbol「新浪不认识这个合约」——
// **一个确实存在的郑商所合约，会得到「不存在」这个答案。**
// 这正是本函数与 Instrument() 必须分开的理由：**两者形状像，而一个用错就整类合约拿不到数据。**
//
// 发大写：大小写虽然实测不敏感，**而大写是 probe.md 与本包 fixture 里逐字观察过的那个形态**。
// 不敏感这一条是三组样本上的读数，不是承诺。
func SinaSymbol(s tickflow.Symbol) string {
	return fmt.Sprintf("%s%04d", strings.ToUpper(s.Product), s.YearMon)
}

// —— Client ——

// Client 是新浪源。它实现 tickflow.Source。
//
// ⚠️ **日历由构造时注入，而这里有一个用法陷阱，写在最显眼处：**
//
//	`calendar/embedded` 不自带交易日、要调用方注入，
//	而**新浪日线返回的那串日期本身就是该合约的交易日序列** ——
//	于是「拿新浪的日期喂给日历、再用这个日历去组装新浪的数据」是最顺手的用法。
//
// ⛔ **那种用法下 ErrSinaDisagreesWithCalendar 永远不响**（日历是从同一串日期造的），
// 而用一份**独立**日历的人反而会时不时看见它报错。
//
//	⇒ **这个检查的吵闹程度，和用法的正确性成反比。**
//	  它会给人一个印象：「用官方日历反而一直报错，用新浪的日期就干净」——
//	  **而那正好是一个把人推向循环用法的激励。**
//
// ⇒ 本片没有机械办法分辨日历的来历（日历接口里没有「你从哪来」这一维），
// **所以这里只写明。登记⑬。** 处置方向在 Syncer 那一层：
// 由它持有日历的来历，并在「日历来自本源自身」时把一致性检查标成【未生效】而不是【通过】。
type Client struct {
	http    *http.Client
	baseURL string
	referer string
	cal     tickflow.Calendar
	now     func() int64
}

// Option 配 Client。
type Option func(*Client)

// WithHTTPClient 换一个 http.Client（超时、代理、连接池都在它上面）。
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// WithBaseURL 换端点。**测试拿它指向 httptest**，不打真网。
func WithBaseURL(u string) Option { return func(c *Client) { c.baseURL = u } }

// WithClock 换时钟。**判「已完结」要用它**，而内部取 time.Now() 的东西
// 没法被测试固定住 —— 那样这一层会变成「今天绿明天红」。
func WithClock(f func() int64) Option { return func(c *Client) { c.now = f } }

// New 造一个新浪源。日历是必需的：Ts/TsEnd 与「已完结」都靠它，本包不猜。
func New(cal tickflow.Calendar, opts ...Option) (*Client, error) {
	if cal == nil {
		return nil, fmt.Errorf("sinasource: New 需要一个日历——Ts/TsEnd 与「已完结」都靠它")
	}
	c := &Client{
		http:    &http.Client{Timeout: 30 * time.Second},
		baseURL: DefaultBaseURL,
		referer: DefaultReferer,
		cal:     cal,
		now:     func() int64 { return time.Now().UnixMilli() },
	}
	for _, o := range opts {
		o(c)
	}
	return c, nil
}

var _ tickflow.Source = (*Client)(nil)

// Bars 实现 tickflow.Source。
//
// ⚠️ 它**不**在返回前调用 tickflow.CheckBars —— 同 AssembleDaily 那条理由：
// 实现自己核自己，测试就变成同义反复。**契约由测试拿 CheckBars 去核。**
func (c *Client) Bars(ctx context.Context, req tickflow.BarRequest) ([]tickflow.Bar, error) {
	if err := req.Validate(); err != nil {
		return nil, fmt.Errorf("sinasource: 请求不合法：%w", err)
	}
	// ⑮ 主连：显式拒绝，别让它走到「合约不存在」。
	//
	// ⛔ 不拦的话：Symbol{SHFE,"rb",YearMon:0} ⇒ SinaSymbol 给 "RB0000"
	// ⇒ 新浪答 null ⇒ ErrUnknownSymbol「**新浪不认识这个合约**」。
	// 于是想要主连的人拿到的答案是「这个合约不存在」，而真相是「**本源不做主连**」。
	// **文档里分开了，运行时没分开** —— 和 null≠[]、ErrCalendarGap≠没数据同族。
	if req.Symbol.YearMon == 0 {
		return nil, fmt.Errorf("sinasource: %s.%s 看起来是主力连续（YearMon=0），"+
			"而**本源只做具体合约**——主连在 tickflow.Symbol 里还没有表示形态。"+
			"这不是「该合约不存在」", req.Symbol.Exchange, req.Symbol.Product)
	}

	// ⑭ 周期闸【从 Caps 取】，不再各写一份。
	//
	// ⛔ 原来 Bars 里一份、Caps 里一份、两条测试各自钉在测试里的第三份字面量上，
	// **没有任何一处由另一处推出来** —— 实测：让 Bars 也收 Weekly 而 Caps 不变，
	// **一条测试都不红**（评审方 2026-09-09 的对照组，我复现过）。
	// ⇒ 现在只剩一处定义：Caps().Periods。
	if caps := c.Caps(req.Symbol.ProductKey()); !caps.Supports(req.Period) {
		return nil, fmt.Errorf("sinasource: 本源在 %s 上不支持周期 %s，只支持 %v"+
			"（分钟线在新浪有 1023 根硬顶且无法翻页，深度走天勤——见 probe.md 坑一）",
			req.Symbol.ProductKey(), req.Period, caps.Periods)
	}
	body, err := c.fetchDaily(ctx, SinaSymbol(req.Symbol))
	if err != nil {
		return nil, err
	}
	rows, err := ParseDaily(body)
	if err != nil {
		return nil, err
	}
	return AssembleDaily(rows, c.cal, req, c.now())
}

func (c *Client) fetchDaily(ctx context.Context, sym string) ([]byte, error) {
	u := c.baseURL + "?symbol=" + url.QueryEscape(sym)
	rq, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("sinasource: 造请求失败：%w", err)
	}
	rq.Header.Set("Referer", c.referer)
	rs, err := c.http.Do(rq)
	if err != nil {
		return nil, fmt.Errorf("sinasource: 请求 %s 失败：%w", sym, err)
	}
	defer rs.Body.Close()
	if rs.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("sinasource: %s 返回 HTTP %d——"+
			"**不当成「没有数据」**：那会被 coverage 记成「拉过、确认没有」", sym, rs.StatusCode)
	}
	b, err := io.ReadAll(rs.Body)
	if err != nil {
		return nil, fmt.Errorf("sinasource: 读 %s 的响应体失败：%w", sym, err)
	}
	return b, nil
}

// —— Caps ——

// dailySince 是【合约级】日线在新浪上最早给得出的交易日。
//
// probe.md 2026-09-07 实测：合约级日线约从 **2018-05** 起有数据
// （主连口径更深，回到 2009-03-27，而主连本源不做 —— 见包注释）。
//
// ⚠️ **它是产品类的下界，不是对某一份合约的承诺**：
// 对具体合约，真正的下限是**它自己的上市日**
// （本包 fixture：RB2610 首日 2025-10-16、TA2701 首日 2026-01-19 —— 差了三个月）。
// ⇒ 所以 Syncer 拿它当「不必再往前问」的界，而不是「这里一定有数据」。
//
// ✅ 登记⑪ **已消**：这个值原本是 `time.Duration`（「能回溯多久」），
// 那个形状随时间漂移，且零值与「忘了填」不可分辨。
// 换成 TradingDay 之后，**忘了填会被 Capabilities.Validate 当场查出来**。
const dailySince = tickflow.TradingDay(20180501)

// Caps 实现 tickflow.Source。
//
// ⚠️ **HasSettle 按交易所分流，这是实测出来的，不是估的**
// （本包 fixture，三份共 499 根）：
//
//	SHFE  RB2610  221 根 ⇒ 结算价为 0 的 **0** 根
//	CZCE  TA2701  156 根 ⇒ **0** 根
//	CFFEX T2612   122 根 ⇒ **122** 根（**全部**）
//
// ⇒ 中金所系统性缺结算价（probe.md 第一节），它的结算价要走 CFFEX 官网 XML
// （`source/cffexsource`，尚未实现）。
//
// ⛔ **MaxBars = 0 在这里表示「没有观察到硬顶」** —— 日线上 `RB0` 一次给过 4237 根。
// 而 0 同时也是「一根都给不了」的写法：**这两者在这个类型里不可分辨。**
// （新浪的 1023 硬顶是【分钟线】的，本源这一版不给分钟线。）**登记⑫。**
func (c *Client) Caps(k tickflow.ProductKey) tickflow.Capabilities {
	return tickflow.Capabilities{
		Periods:   []tickflow.Period{tickflow.Daily},
		MaxBars:   0,
		Since:     map[tickflow.Period]tickflow.TradingDay{tickflow.Daily: dailySince},
		HasSettle: k.Exchange != tickflow.CFFEX,
		HasOI:     true,
		Realtime:  false, // probe.md 第三节：新浪实时接口自 2024-07-17 冻结，本包不接
	}
}
