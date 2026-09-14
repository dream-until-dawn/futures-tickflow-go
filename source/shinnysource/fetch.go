package shinnysource

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"

	"github.com/coder/websocket"
)

// pageWidth 是一窗要多少根：10000 可、12000 断连（probe.md 6.13 前的翻页读数）。
// 是变量只为一件事：离线测试把它调小，好在几百根上走到翻页。
var pageWidth = 10000

// widthMargin 是按日历分钟数定窗宽时多要的根数：让窗在数据齐全时能盖到 winEnd 之后的第一根，
// 于是一窗就能判「翻完了」，不必再发一窗去确认。
const widthMargin = 16

// viewWidth 按请求里的交易分钟数定一窗要多少根，封顶 pageWidth。
//
// ⛔ 读数（2026-09-14，live_test.go，SHFE.rb2605 五个交易日 1725 根）：窗宽 10000 一次 6.4 s，窗宽 1000 翻两窗 1.1 s ——
// 窗从请求起点往后要满 view_width 根，请求只有五天时多出来的七八千根全是白传。
// ⚠️ 日历的分钟数可能【多于】数据（停夜盘日 embedded 看不见）⇒ 只会多要，不会少要到翻不完：少了照样续翻。
func viewWidth(minutes int) int {
	w := minutes + widthMargin
	if w > pageWidth {
		w = pageWidth
	}
	return w
}

const (
	// readLimit 是单条 websocket 消息的上限。一窗 10000 根的 JSON 在 MiB 量级，留足余量。
	readLimit = 64 << 20
	// maxPages 挡的是「窗一直不前进」那种死循环 —— 按 left_kline_id 严格递增它到不了，到了就是协议变了。
	maxPages = 100000

	chartID = "tickflow"
)

var (
	minuteNs  = int64(60) * 1e9
	minuteKey = strconv.FormatInt(minuteNs, 10)
)

// Row 是 DIFF 快照里的一根 1m，字段照上游原样（Datetime 是开盘时刻，纳秒）。
type Row struct {
	ID       int64
	Datetime int64
	Open     float64
	High     float64
	Low      float64
	Close    float64
	Volume   float64
	CloseOI  float64
}

// dial 新拨一条行情连接。握手被拒（401/403）时作废缓存的 token，重取一次再拨。
func (c *Client) dial(ctx context.Context) (*websocket.Conn, error) {
	for attempt := 0; ; attempt++ {
		tok, md, err := c.session(ctx)
		if err != nil {
			return nil, err
		}
		conn, resp, err := websocket.Dial(ctx, md, &websocket.DialOptions{
			// ⛔ 握手经注入的 client：限流闸门、代理、计数都作用在每一条连接上（probe.md 6.19）。
			HTTPClient: c.cfg.HTTPClient,
			HTTPHeader: http.Header{
				"User-Agent":    {c.cfg.UserAgent},
				"Accept":        {"application/json"},
				"Authorization": {"Bearer " + tok},
			},
			// 压缩协商必须是这一档：design.md 第一节，gorilla 那次 400 就败在这里。
			CompressionMode: websocket.CompressionNoContextTakeover,
		})
		if err == nil {
			conn.SetReadLimit(readLimit)
			return conn, nil
		}
		code := 0
		if resp != nil {
			code = resp.StatusCode
		}
		if attempt == 0 && (code == http.StatusUnauthorized || code == http.StatusForbidden) {
			c.forget()
			continue
		}
		return nil, fmt.Errorf("shinnysource: 连行情 websocket 失败（HTTP %d，第 %d 次）：%w", code, attempt+1, err)
	}
}

// fetch 在一条新连接上把 [winStart, winEnd)（毫秒）内合约 ins 的 1m 全部翻出来，按 id 升序。
//
// 翻页：第一窗 focus_datetime＝窗起点、focus_position＝0；之后 left_kline_id＝上一窗 right_id＋1。
// 停：窗里最晚一根已到 winEnd，或 right_id ≥ last_id（窗在末根之后时服务端给的就是这个形状，probe.md 6.20 E1/E5）。
// 每窗读完就删本地快照里的 data（6.17）—— 窗与窗不重叠，所以删了也不需要服务端补发（6.18）。
func (c *Client) fetch(ctx context.Context, ins string, winStart, winEnd int64, width int) ([]Row, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.CloseNow()

	send := func(v any) error {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		return conn.Write(ctx, websocket.MessageText, b)
	}
	snap := map[string]any{}
	readOne := func() error {
		rctx, cancel := context.WithTimeout(ctx, c.cfg.ReadTimeout)
		_, msg, err := conn.Read(rctx)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		dec := json.NewDecoder(bytes.NewReader(msg))
		dec.UseNumber() // ⛔ 纳秒时间戳超出 float64 的整数精度（2^53），不能让 json 包默认转成 float64
		var m struct {
			Aid  string           `json:"aid"`
			Data []map[string]any `json:"data"`
		}
		if err := dec.Decode(&m); err != nil {
			return fmt.Errorf("收到一条解析不了的消息（%d 字节）：%w", len(msg), err)
		}
		if m.Aid == "rtn_data" {
			for _, d := range m.Data {
				merge(snap, d)
			}
		}
		return nil
	}
	readings := func() string {
		ch := obj(snap, "charts", chartID)
		ser := obj(snap, "klines", ins, minuteKey)
		get := func(m map[string]any, k string) string {
			if m == nil {
				return "缺"
			}
			if v, ok := m[k]; ok {
				return fmt.Sprint(v)
			}
			return "缺"
		}
		return fmt.Sprintf("chart ready=%s more_data=%s left_id=%s right_id=%s · klines last_id=%s",
			get(ch, "ready"), get(ch, "more_data"), get(ch, "left_id"), get(ch, "right_id"), get(ser, "last_id"))
	}

	startNs, endNs := winStart*1e6, winEnd*1e6
	var out []Row
	left := int64(-1)
	for page := 0; ; page++ {
		if page >= maxPages {
			return nil, fmt.Errorf("shinnysource: %s 翻了 %d 窗还没翻完——窗没有前进，协议可能变了（%s）", ins, page, readings())
		}
		rq := map[string]any{"aid": "set_chart", "chart_id": chartID, "ins_list": ins, "duration": minuteNs, "view_width": width}
		if left < 0 {
			rq["focus_datetime"], rq["focus_position"] = startNs, 0
		} else {
			rq["left_kline_id"] = left
		}
		if err := send(rq); err != nil {
			return nil, fmt.Errorf("shinnysource: 发 set_chart 失败：%w", err)
		}
		if page == 0 {
			if err := send(map[string]any{"aid": "peek_message"}); err != nil {
				return nil, fmt.Errorf("shinnysource: 发 peek_message 失败：%w", err)
			}
		}
		for {
			ch := obj(snap, "charts", chartID)
			ready, _ := ch["ready"].(bool)
			more, _ := ch["more_data"].(bool)
			l, okl := numInt(ch["left_id"])
			if ready && !more && okl && l >= 0 && (left < 0 || l == left) {
				break
			}
			if err := readOne(); err != nil {
				if ctx.Err() != nil {
					return nil, fmt.Errorf("shinnysource: %s 拉取被取消：%w", ins, err)
				}
				return nil, fmt.Errorf("%w：%s 第 %d 窗（每次读期限 %v）· 读数 %s · 读错误：%w",
					ErrNotReady, ins, page+1, c.cfg.ReadTimeout, readings(), err)
			}
			if err := send(map[string]any{"aid": "peek_message"}); err != nil {
				return nil, fmt.Errorf("shinnysource: 发 peek_message 失败：%w", err)
			}
		}

		ch := obj(snap, "charts", chartID)
		l, _ := numInt(ch["left_id"])
		r, okr := numInt(ch["right_id"])
		ser := obj(snap, "klines", ins, minuteKey)
		lastID, okLast := numInt(ser["last_id"])
		if !okr || !okLast || r < l {
			return nil, fmt.Errorf("shinnysource: %s 第 %d 窗的形状不认识（%s）", ins, page+1, readings())
		}
		data := obj(ser, "data")
		var maxTs int64
		for id := l; id <= r; id++ {
			m, ok := data[strconv.FormatInt(id, 10)].(map[string]any)
			if !ok {
				if id <= lastID {
					// ⛔ 窗里、而且不晚于末根的 id 不在快照里 ⇒ 这一窗没收全。不能当成「那一分钟没有根」。
					return nil, fmt.Errorf("shinnysource: %s 第 %d 窗 [%d, %d] 里 id %d 不在快照里（last_id=%d）——窗没收全",
						ins, page+1, l, r, id, lastID)
				}
				continue
			}
			row, err := parseRow(id, m)
			if err != nil {
				return nil, fmt.Errorf("shinnysource: %s：%w", ins, err)
			}
			if row.Datetime > maxTs {
				maxTs = row.Datetime
			}
			if row.Datetime >= startNs && row.Datetime < endNs {
				out = append(out, row)
			}
		}
		// ⛔ 只删【已经进过窗】的（id ≤ right_id）。服务端每一窗都捎上 last_id 那一根（probe.md 6.13 a），
		// 而同一连接上发过的它不再补发（6.18）⇒ 第一窗就删掉它的话，翻到最后一窗时那一根永远不会再来。
		// （离线复刻按 6.13 a ＋ 6.18 两条读数写，第一次跑就撞上了 —— 被上面「窗没收全」那一格接住，没有静默少一根。）
		for key := range data {
			if id, err := strconv.ParseInt(key, 10, 64); err != nil || id <= r {
				delete(data, key)
			}
		}
		if maxTs >= endNs || r >= lastID {
			return out, nil
		}
		left = r + 1
	}
}

func parseRow(id int64, m map[string]any) (Row, error) {
	row := Row{ID: id}
	ts, ok := numInt(m["datetime"])
	if !ok {
		return row, fmt.Errorf("id %d 的 datetime 缺失或不是整数（%v）", id, m["datetime"])
	}
	row.Datetime = ts
	for _, f := range []struct {
		key string
		dst *float64
	}{
		{"open", &row.Open}, {"high", &row.High}, {"low", &row.Low}, {"close", &row.Close},
		{"volume", &row.Volume}, {"close_oi", &row.CloseOI},
	} {
		v, ok := numFloat(m[f.key])
		if !ok {
			return row, fmt.Errorf("id %d 的 %s 缺失或不是数（%v）", id, f.key, m[f.key])
		}
		*f.dst = v
	}
	return row, nil
}

// numInt 读一个整数：json.Number（UseNumber 之后的形状）或整数值的 float64。
func numInt(v any) (int64, bool) {
	switch x := v.(type) {
	case json.Number:
		n, err := x.Int64()
		return n, err == nil
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1<<53 {
			return int64(x), true
		}
	}
	return 0, false
}

// numFloat 读一个数。字符串 "NaN" 照原样读成 NaN。
// ⚠️ 「上游会不会发字符串 NaN」**本仓没有读数**（6.13–6.20 量到的 kline 字段全是数）；
// 收下它是因为另一个选择（报「不是数」）会让一根只缺持仓量的根把整块拉取打断。
func numFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	case float64:
		return x, true
	case string:
		if x == "NaN" {
			return math.NaN(), true
		}
	}
	return 0, false
}

// merge 是 DIFF 协议的 JSON merge-patch：null 删键，对象递归合并。
func merge(dst, src map[string]any) {
	for k, v := range src {
		if v == nil {
			delete(dst, k)
			continue
		}
		if sm, ok := v.(map[string]any); ok {
			dm, ok := dst[k].(map[string]any)
			if !ok {
				dm = map[string]any{}
				dst[k] = dm
			}
			merge(dm, sm)
			continue
		}
		dst[k] = v
	}
}

func obj(m map[string]any, path ...string) map[string]any {
	for _, p := range path {
		if m == nil {
			return nil
		}
		next, _ := m[p].(map[string]any)
		m = next
	}
	return m
}
