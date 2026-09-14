// Package shinnysource 是天勤（快期）行情源：具体合约的 1m 历史。
//
// 三层，各自可以单独测：
//
//	token / mdurl ← HTTPS（auth.go，经注入的 *http.Client）
//	DIFF 快照的行 ← websocket 翻页（fetch.go，握手同样经注入的 *http.Client）
//	Bar           ← 组装（assemble.go，要日历；纯函数）
//
// ⛔ **只做具体合约的 1m**。主连（`KQ.m@…`）拒掉：它在 `tickflow.Symbol` 里表达不出来，
// 同 sinasource 的先例。日线已有新浪、中金所两个源，天勤日线不在这一版。
//
// ⛔ **凭证全部由调用方传入**：本包不读环境变量、不读 .env，也不给 client_id 设默认值
// （用户 2026-09-14 裁定；`SHINNY_CLIENT_SECRET` 永不进本仓）。
//
// 协议事实都有读数，出处在 docs/probe.md：
//
//	6.13  翻页：focus_datetime 定位 ＋ left_kline_id 续翻，view_width 10000
//	6.16  Read 不带期限时没数据会一直等；带期限的 Read 超时之后【连接随之关闭】
//	6.17  内存大头是 DIFF 快照 ⇒ 逐窗转换、逐窗删快照
//	6.18  本地删了快照，同一连接上再要同一段服务端不再发 ⇒ 【重试的单位是连接】⇒ 每次 Bars 新连接
//	6.19  websocket 握手走 DialOptions.HTTPClient ⇒ 本源声明 ClientUseHTTP
//	6.20  窗在末根之后 / 上市之前 ⇒「就绪、0 根」；合约不存在 ⇒「永不就绪、id 全 -1」
package shinnysource

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
)

// —— 端点 ——

// DefaultAuthURL 是快期账户的 token 端点（probe.md 6.1）。
const DefaultAuthURL = "https://auth.shinnytech.com/auth/realms/shinnytech/protocol/openid-connect/token"

// DefaultNSURL 是名称服务：行情 websocket 的地址要问它，不能写死（probe.md 6.1）。
const DefaultNSURL = "https://api.shinnytech.com/ns?stock=false&backtest=false"

const modulePath = "github.com/dream-until-dawn/futures-tickflow-go"

// DefaultUserAgent 是不给 Config.UserAgent 时发的 User-Agent：本库自己的名字（用户 2026-09-14 裁定，probe.md 6.14）。
//
// ⚠️ 版本号从构建信息里取，不写死 —— 写死的版本号在下一次发版那天就是错的。
// 取不到（在本仓里直接跑测试时是 "(devel)"）就写 dev。
var DefaultUserAgent = "futures-tickflow-go/" + moduleVersion() + " (+https://" + modulePath + ")"

func moduleVersion() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	pick := func(v string) string {
		if strings.HasPrefix(v, "v") && !strings.ContainsAny(v, "() ") {
			return strings.TrimPrefix(v, "v")
		}
		return ""
	}
	if bi.Main.Path == modulePath {
		if v := pick(bi.Main.Version); v != "" {
			return v
		}
	}
	for _, d := range bi.Deps {
		if d.Path == modulePath {
			if v := pick(d.Version); v != "" {
				return v
			}
		}
	}
	return "dev"
}

// —— 错误 ——

var (
	// ErrAuth 取 token 失败。
	ErrAuth = errors.New("shinnysource: 取 token 失败")
	// ErrNoMdURL 名称服务没给行情地址。
	ErrNoMdURL = errors.New("shinnysource: 名称服务没给 mdurl")
	// ErrContinuous 请求的是主连（YearMon == 0），本源只做具体合约。
	ErrContinuous = errors.New("shinnysource: 本源只做具体合约，主连在 tickflow.Symbol 里还没有表示形态")
	// ErrNotReady 等不到服务端就绪。
	//
	// ⚠️ 合约不存在时服务端**不报错、只是永不就绪**（probe.md 6.20）⇒ 这一格与「网络卡住」
	// 在本源这一层长得一样。报文会带上 last_id 等读数，**成因留给读的人**。
	ErrNotReady = errors.New("shinnysource: 等不到服务端就绪")
	// ErrCalendarDisagrees 天勤给了一根日历放不下的根（休市时刻、跨时段收盘、落在请求之外的交易日）。
	ErrCalendarDisagrees = errors.New("shinnysource: 天勤给的根与日历对不上")
)

// —— Config ——

// Config 是构造一个天勤源所需的全部东西。**没有一格有隐藏的默认值**，除了两个端点与 UserAgent。
type Config struct {
	// User / Password 是快期账户（免费申请）。必填。
	User, Password string
	// ClientID / ClientSecret 是 OAuth 客户端凭证。必填，本库不设默认值。
	//
	// ⛔ ClientSecret 永远不要写进代码仓库；token 端点返回非 200 时最可能是它被轮换了。
	ClientID, ClientSecret string

	// UserAgent 为空 ⇒ DefaultUserAgent。
	UserAgent string

	// Calendar 判「归哪个交易日」「收没收盘」都问它。必填。
	//
	// ⛔ **必须与 SyncerConfig.Calendar 是同一个日历**：两份不一致时，源给的 TradingDay
	// 与 Syncer 按日历规划的会错位。本源实现 tickflow.CalendarHolder，NewSyncer 会比对。
	// 将来拿 calendar/derived 当 Syncer 的日历时，这里也得是那一份 derived。
	Calendar tickflow.Calendar

	// HTTPClient 取 token、问名称服务、websocket 握手都经过它。必填。
	//
	// 交给 Syncer 时由 SourceFactory 递进来（限流闸门装在它上面）；自己用的话显式传一个，
	// 例如 http.DefaultClient —— **本包不替你挑超时与代理**。
	// ⚠️ 它的 Timeout 只管握手（coder/websocket 把它挪成握手的期限，probe.md 6.19）；
	// 握手之后每一次读的期限由 ReadTimeout 管。
	HTTPClient *http.Client

	// ReadTimeout 是每一次 websocket 读的期限。必填（> 0），没有「不限」这个选项：
	// 不带期限的读在服务端不回数据时会一直等（probe.md 6.16），而合约不存在时服务端正是不回（6.20）。
	ReadTimeout time.Duration

	// Now 返回墙钟毫秒，判「已完结」用。nil ⇒ time.Now。
	Now func() int64

	// AuthURL / NSURL 为空 ⇒ 默认端点。测试拿它们指向 httptest。
	AuthURL, NSURL string
}

// —— Client ——

// Client 是天勤源。它实现 tickflow.Source 与 tickflow.CalendarHolder。
type Client struct {
	cfg Config

	mu  sync.Mutex
	tok string // 缓存：token 两年有效（probe.md 6.1）；握手 401/403 时作废重取一次
	md  string // 缓存：名称服务给的行情地址
}

var (
	_ tickflow.Source         = (*Client)(nil)
	_ tickflow.CalendarHolder = (*Client)(nil)
)

// New 造一个天勤源。**它不连网**：token 与 mdurl 在第一次 Bars 时才取。
func New(cfg Config) (*Client, error) {
	var errs []error
	for _, f := range []struct{ name, v string }{
		{"User", cfg.User}, {"Password", cfg.Password},
		{"ClientID", cfg.ClientID}, {"ClientSecret", cfg.ClientSecret},
	} {
		if f.v == "" {
			errs = append(errs, fmt.Errorf("%s 为空——凭证由调用方传入，本库不读环境变量、不设默认值", f.name))
		}
	}
	if cfg.Calendar == nil {
		errs = append(errs, errors.New("Calendar 为 nil——归交易日与判完结都靠它，本包不猜"))
	}
	if cfg.HTTPClient == nil {
		errs = append(errs, errors.New("HTTPClient 为 nil——取 token、问名称服务、websocket 握手都经过它；"+
			"交给 Syncer 时用 SourceFactory 递进来的那个，自己用就显式传一个（例如 http.DefaultClient）"))
	}
	if cfg.ReadTimeout <= 0 {
		errs = append(errs, fmt.Errorf("ReadTimeout=%v 不合法——必须为正：不带期限的读在服务端不回数据时会一直等"+
			"（probe.md 6.16），而合约不存在时服务端正是不回（6.20）", cfg.ReadTimeout))
	}
	if err := errors.Join(errs...); err != nil {
		return nil, fmt.Errorf("shinnysource: Config 立不住: %w", err)
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = DefaultUserAgent
	}
	if cfg.Now == nil {
		cfg.Now = func() int64 { return time.Now().UnixMilli() }
	}
	if cfg.AuthURL == "" {
		cfg.AuthURL = DefaultAuthURL
	}
	if cfg.NSURL == "" {
		cfg.NSURL = DefaultNSURL
	}
	return &Client{cfg: cfg}, nil
}

// Calendar 交出构造时注入的日历，让 NewSyncer 核「两边是不是同一个」。
func (c *Client) Calendar() tickflow.Calendar { return c.cfg.Calendar }

// —— Caps ——

// minuteSince 是 1m 在天勤上最早给得出的交易日。
//
// 读数（probe.md 6.13）：`KQ.m@SHFE.rb` / `au` 的首根都是 **2016-01-04 21:00**（周一夜盘）⇒ 属于交易日 2016-01-05。
// ⚠️ 它是**产品类的下界，不是对某一份合约的承诺**：具体合约真正的下限是它自己的上市日，
// 而天勤合约表里没有上市日（登记㉔）⇒ 上市前的日子会被记成「拉过、确认没有」——对一个还不存在的合约，这句话本身是对的。
// ⚠️ 这一版的日历只覆盖 2020-05-06 之后（H2），Syncer 在 Covers 处裁剪，更早的 1m 不会被请求。
const minuteSince = tickflow.TradingDay(20160105)

// BatchDays 是一次 Bars 覆盖的交易日数（probe.md 6.17：每块 20 个交易日，rb 峰值 11.2 MiB、au 18.8 MiB）。
const BatchDays = 20

// Caps 实现 tickflow.Source。
func (c *Client) Caps(k tickflow.ProductKey) tickflow.Capabilities {
	return tickflow.Capabilities{
		Periods: []tickflow.Period{tickflow.MustIntraday(1)},
		// 能翻页，没有观察到硬顶。
		MaxBars:   0,
		BatchDays: BatchDays,
		// websocket 握手经注入的 client ⇒ 每次 Bars 至少过闸 1 次，计数为 0 是该出声的读数。
		//
		// 读数（probe.md 6.19，探针 d98c354，2026-09-14 21:22:34，coder/websocket v1.8.15，形状复刻不经真 NewSyncer）：
		//
		//	A 不给 HTTPClient                           握手 101 · chart 5 根
		//	B 计数 → http.Transport                     握手 101 · 握手后计数 1 · 收完仍是 1
		//	C 计数 → 限流形状 → Transport，Timeout=5s   握手 101 · 计数 1 · 等 8s（过了 Timeout）同连接开新 chart 照样收到
		//	D 计数 → 把 resp.Body 包成只读（必须红）     握手失败：
		//	  「failed to WebSocket dial: response body is not a io.ReadWriteCloser: struct { io.ReadCloser }」
		//
		// ⛔ **D 格是写给将来改包装的人的**：coder/websocket 要求 Transport 返回【可写】的 body（dial.go 注释，实测会查）。
		// 限流、计数、代理那一层只能**转发** resp，**不许包 body、不许换 resp** —— 否则每一次 Bars 都握手失败。
		// 经真 NewSyncer 的那一格在 syncer_test.go（离线，含 token 已缓存之后那一次）与 live_test.go。
		ClientUse: tickflow.ClientUseHTTP,
		Since:     map[tickflow.Period]tickflow.TradingDay{tickflow.MustIntraday(1): minuteSince},
		HasSettle: false, // 分钟线没有结算价
		HasOI:     true,  // close_oi
		Realtime:  false, // 推送通道在 v0.10
	}
}

// —— Bars ——

// Bars 实现 tickflow.Source：返回 [From, To] 内【已完结】的 1m，按 Ts 升序。
//
// 每次调用新拨一条连接、拉完关掉（probe.md 6.18：同一段要重拉必须新连接；6.16：读超时会关连接）。
// ⇒ Syncer 那一块失败后再调一次 Bars，拿到的是一条干净的连接。
//
// ⚠️ 它**不**在返回前调用 tickflow.CheckBars —— 同 sinasource 的理由：实现自己核自己，测试就变成同义反复。
// ⛔ 而「只返回已完结」这一条**本源自己做**（丢 TsEnd > Now 的根）：CheckBars 在 Syncer 的生产路径上没有调用点。
func (c *Client) Bars(ctx context.Context, req tickflow.BarRequest) ([]tickflow.Bar, error) {
	if err := req.Validate(); err != nil {
		return nil, fmt.Errorf("shinnysource: 请求不合法：%w", err)
	}
	if req.Symbol.YearMon == 0 {
		return nil, fmt.Errorf("%w（收到 %s.%s，YearMon=0）——这不是「该合约不存在」",
			ErrContinuous, req.Symbol.Exchange, req.Symbol.Product)
	}
	k := req.Symbol.ProductKey()
	if caps := c.Caps(k); !caps.Supports(req.Period) {
		return nil, fmt.Errorf("shinnysource: 本源在 %s 上不支持周期 %s，只支持 %v", k, req.Period, caps.Periods)
	}

	var days []tickflow.Day
	if err := c.cfg.Calendar.Walk(k, req.From, req.To, func(d tickflow.Day) bool {
		if len(d.Sessions) > 0 {
			days = append(days, d)
		}
		return true
	}); err != nil {
		return nil, fmt.Errorf("shinnysource: 铺开 [%s, %s] 的交易日失败：%w", req.From, req.To, err)
	}
	if len(days) == 0 {
		return []tickflow.Bar{}, nil
	}
	winStart := days[0].Sessions[0].Start
	last := days[len(days)-1]
	winEnd := last.Sessions[len(last.Sessions)-1].End

	ins := req.Symbol.Native() // 郑商所三位年月（CZCE.TA701），其余四位 —— 与天勤合约表的形状一致（probe.md 6.20 E4）
	minutes := 0
	for _, d := range days {
		minutes += d.Minutes()
	}
	rows, err := c.fetch(ctx, ins, winStart, winEnd, viewWidth(minutes))
	if err != nil {
		return nil, err
	}
	return Assemble(rows, c.cfg.Calendar, req, c.cfg.Now())
}
