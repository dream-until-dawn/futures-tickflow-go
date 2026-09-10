package cffexsource

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
)

// 这一片是【拉取】：把三片接成一个 tickflow.Source。
//
// ⛔ **本源的形状与 sinasource 反过来，而代价是具体的：**
//
//	sinasource   一个合约的全部历史 = **1 次**请求
//	cffexsource  一天的全部合约     = 要 N 个交易日就是 **N 次**请求
//
// 实测：存档回溯到至少 **2016-01-04**（那天 18 条期货、0 条期权），
// 而 2016-01-04 → 2026-09-08 约 **2671 个交易日** ⇒ **全量回补≈2671 次 HTTP 请求**。
// ⇒ 所以本层**必须逐日走，而且必须走对日子** —— 走多了是白打，走漏了是缺口。
// 而铺开区间的正确工具是 `Calendar.Walk`：**它对区间落在覆盖之外会当场炸，不静默少遍历。**

// DefaultBaseURL 是中金所日行情 XML 的目录（probe.md 第七节记的那个）。
//
// 完整形态：`<base>/<YYYYMM>/<DD>/index.xml` —— **日期不写死，由交易日现拼**。
const DefaultBaseURL = "http://www.cffex.com.cn/sj/hqsj/rtj"

// cffexSince 是**存档级**的下界：这个源最早给得出哪一天的文件。
//
// 实测 2026-09-09：`201601/04` HTTP 200，18 条期货、0 条期权。
//
// ⛔⛔ **而 `Capabilities.Since` 的契约是【按品种】给的**（source.go：「该源在**这个品种**上」），
// **本源给八个品种同一个值 —— 对其中三个是【明知的错】。**
//
//	2016-01-04 那天只有  IC / IF / IH / T / TF          —— 五个
//	2026-09-08 那天有    IC / IF / IH / **IM** / T / TF / **TS** / **TL** —— 八个
//	⇒ 对 IM / TS / TL，2016-01-04 **早于它们上市**，那里根本没有它们的数据
//
// ⚠️ 而代价在本源上不是「差一点」，因为它**一天一个请求**（实测）：
//
//	照这个 Since 回补一个后上市的品种 ⇒ **几千次请求换 0 根，而且 err == nil**
//	（实测：区间内该品种不存在 ⇒ 3 天 3 次请求 ⇒ 0 根、无错）
//	⇒ 这正是⑨（缺席静默）×⑯（Since 与上市日之间那一整段）在本源上的落地
//
// ⛔ **登记㉔：按品种给 Since 需要【上市日】。**
// ⚠️ 【2026-09-10 更正】此处原写「而那是 refdata（v0.4）」—— **那是假的**：
// 全量实测天勤 openmd 的 47 个字段里没有上市日（docs/probe.md「复核 v5」）。
// 替代出处已实测并写在那一节，**而它的二分前提未验** ⇒ 今天仍然做不到 ——
// 今天做不到，所以这里写明射程而不是假装它对：
// **它是存档级下界，不是对某个品种的承诺**；㉒ 说的「用上市/到期日把未上市那段减掉」，
// 减的正是这一段。
//
// ⚠️ 而我此前只写了「它是下界，不是从这天起每天都有」—— 那答的是【密度】，
// **而毛病在【维度】：这个数根本不是关于这个品种的。**
// ⇒ 判据（评审方 2026-09-09）：**一个数被问「准不准」之前，先问它「是关于什么的」。**
const cffexSince = tickflow.TradingDay(20160104)

// Client 是中金所源。它实现 tickflow.Source。
//
// ⚠️ **日历是必需的，而且它在这里比在 sinasource 更吃重**：
// 那边日历只用来定 Ts/TsEnd 与判完结；**这边它还决定【要发几个请求、发哪几天】**。
// ⇒ 日历错一天，这边不是「算错一根」，是**多打一次或少打一次网络请求**。
type Client struct {
	http    *http.Client
	baseURL string
	cal     tickflow.Calendar
	now     func() int64
}

// Option 配 Client。
type Option func(*Client)

// WithHTTPClient 换一个 http.Client。
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// WithBaseURL 换端点。**测试拿它指向 httptest**，不打真网。
func WithBaseURL(u string) Option { return func(c *Client) { c.baseURL = u } }

// WithClock 换时钟；判完结要用它，而内部取 time.Now() 的东西没法被测试固定住。
func WithClock(f func() int64) Option { return func(c *Client) { c.now = f } }

// New 造一个中金所源。
func New(cal tickflow.Calendar, opts ...Option) (*Client, error) {
	if cal == nil {
		return nil, fmt.Errorf("cffexsource: New 需要一个日历——" +
			"它不只定 Ts/TsEnd，还决定要发哪几天的请求")
	}
	c := &Client{
		http:    &http.Client{Timeout: 30 * time.Second},
		baseURL: DefaultBaseURL,
		cal:     cal,
		now:     func() int64 { return time.Now().UnixMilli() },
	}
	for _, o := range opts {
		o(c)
	}
	return c, nil
}

var _ tickflow.Source = (*Client)(nil)

// Bars 实现 tickflow.Source：逐个交易日取，拼成一段。
//
// ⚠️ **一天一个请求** —— 区间越长请求越多（全量回补≈2671 次，见包注释）。
// 调用方要控制节奏的话，从 `WithHTTPClient` 传一个带限流的 `http.Client` 进来；
// **本层不自己限流**，因为限流策略属于调用方（同步任务 vs 交互式补一天，节奏不同）。
//
// ⛔ **中途失败即中止，不返回半截。** 与 sinasource 那边 ErrCalendarGap 同一条理由：
// 半截结果会被 coverage 记成「拉过、确认没有」，而那一段将来不再重拉。
func (c *Client) Bars(ctx context.Context, req tickflow.BarRequest) ([]tickflow.Bar, error) {
	if err := req.Validate(); err != nil {
		return nil, fmt.Errorf("cffexsource: 请求不合法：%w", err)
	}
	if req.Symbol.Exchange != tickflow.CFFEX {
		return nil, fmt.Errorf("%w：收到 %s", ErrNotCFFEX, req.Symbol)
	}
	k := req.Symbol.ProductKey()
	if caps := c.Caps(k); !caps.Supports(req.Period) {
		return nil, fmt.Errorf("cffexsource: 本源在 %s 上不支持周期 %s，只支持 %v"+
			"（这份 XML 是日行情，没有更细的粒度）", k, req.Period, caps.Periods)
	}

	var out []tickflow.Bar
	var walkErr error
	// Walk 会在区间任一端落在日历覆盖之外时【当场报错】——
	// 这正是我们要的：**绝不静默少遍历**，因为少走一天就是少一根，而缺口是静默的。
	err := c.cal.Walk(k, req.From, req.To, func(day tickflow.Day) bool {
		body, err := c.fetchDay(ctx, day.Num)
		if err != nil {
			walkErr = err
			return false
		}
		rows, err := ParseDaily(body)
		if err != nil {
			walkErr = fmt.Errorf("%s：%w", day.Num, err)
			return false
		}
		bar, found, err := AssembleDay(rows, day, req.Symbol, c.now())
		if err != nil {
			walkErr = err
			return false
		}
		if found {
			out = append(out, bar)
		}
		return true
	})
	if walkErr != nil {
		return nil, walkErr
	}
	if err != nil {
		return nil, fmt.Errorf("cffexsource: 遍历交易日失败：%w", err)
	}
	return out, nil
}

// fetchDay 取某一个交易日的 XML。
//
// URL 由交易日现拼：`<base>/<YYYYMM>/<DD>/index.xml`。
// **日期不写死** —— probe.md 那条探针也是这么做的，因为它会随时间前滚。
func (c *Client) fetchDay(ctx context.Context, d tickflow.TradingDay) ([]byte, error) {
	y, m, day := d.Split()
	u := fmt.Sprintf("%s/%04d%02d/%02d/index.xml", c.baseURL, y, m, day)
	rq, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("cffexsource: 造请求失败：%w", err)
	}
	rs, err := c.http.Do(rq)
	if err != nil {
		return nil, fmt.Errorf("cffexsource: 请求 %s 失败：%w", d, err)
	}
	defer rs.Body.Close()
	if rs.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cffexsource: %s 返回 HTTP %d——"+
			"不当成「那天没有数据」：那会被 coverage 记成「拉过、确认没有」；"+
			"probe.md 记着四家交易所官网里三家被 WAF 挡，而本源是中金所结算价的唯一来源",
			d, rs.StatusCode)
	}
	b, err := io.ReadAll(rs.Body)
	if err != nil {
		return nil, fmt.Errorf("cffexsource: 读 %s 的响应体失败：%w", d, err)
	}
	return b, nil
}

// Caps 实现 tickflow.Source。
//
// ⚠️ HasSettle **恒为 true**，而这不是乐观：本源存在的全部理由就是它 ——
// 新浪对中金所八个品种系统性不给结算价，而这里实测 28/28 全有。
//
// ⛔ **Since 对八个品种给同一个值，而其中三个是明知的错**（登记㉔，见 cffexSince）：
// 那是**存档级**下界，不是按品种的答案；按品种要上市日 ——
// ⛔ 【2026-09-10 更正】而它**不在 refdata 里**（全量 47 个字段实测），
// 替代出处见 docs/probe.md「复核 v5」那一节，且其前提未验。
//
// ⛔ MaxBars = 0：表示「没有观察到硬顶」。而 0 同时也是「一根都给不了」的写法，
// 两者不可分辨（登记⑫，与 sinasource 同一格）。
// **本源的真实约束不是根数，是【请求数】：一天一根、一天一请求。**
func (c *Client) Caps(k tickflow.ProductKey) tickflow.Capabilities {
	return tickflow.Capabilities{
		Periods: []tickflow.Period{tickflow.Daily},
		MaxBars: 0,
		// **一天一个请求** ⇒ 切到一天，失败最多丢一天。
		// 不切的话，一次失败丢 2671 天（全量回补的请求数，见包注释）。
		BatchDays: 1,
		// 本源走 HTTP（`c.http.Do`）⇒ 闸门计数为 0 是一个该出声的读数。
		ClientUse: tickflow.ClientUseHTTP,
		Since:     map[tickflow.Period]tickflow.TradingDay{tickflow.Daily: cffexSince},
		HasSettle: k.Exchange == tickflow.CFFEX,
		HasOI:     true,
		Realtime:  false,
	}
}
