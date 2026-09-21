package shinnysource

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/coder/websocket"
	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
)

// v0.10 P-c：实时推送的联网封装（design.md §十五「v0.10 起手」五 L1 / L8 / L12）。
//
// 判完结、修正、冻结、偏差、起步与重连的接缝全在 liveCore（stream.go）；这里只做三件事：
//
//	连推送通道（subscribe_quote ＋ set_chart ＋ peek_message，与 livetail 同一套行情通道），把每一帧连同【收到那一刻的本机时刻】喂给 liveCore
//	没有新帧时按 liveTick 让时间走（段末根等 G、冻结判定都靠它）
//	liveCore 要补齐时，走历史通道（fetch，与 Bars / Sync 同一条）把 [from, to) 拉回来交给它（L12 起步 · L8 重连共用）
//
// ⛔ 边界：只用行情通道 —— 鉴权 · 名称服务 · websocket 的 set_chart / subscribe_quote / peek_message；不碰交易、下单、资金。
// ⛔ 不做并发安全（§十五 三）：一个 Live 只由一个 goroutine 调 Next。

// ErrDisconnected：推送连接断了，按 LiveOptions.Reconnects 重连都没连上（L8）。已交出的根不受影响；
// 处置与起步相同：从 Feed 最后一根的下一格重新开一个 Live（L12 会把断开期间的根补齐）。
var ErrDisconnected = errors.New("shinnysource: 推送连接断开且重连失败")

var (
	// liveTick 是没有新帧时让时间走的间隔。是变量只为离线测试调小。
	liveTick = 250 * time.Millisecond
	// liveBackoff 是第 i 次重连之前等多久（i 从 0 起）。是变量只为离线测试调成 0。
	liveBackoff = func(i int) time.Duration { return time.Second << i }
)

const (
	// liveWidth 是推送通道的窗宽：连上 / 重连时推送自带最近 liveWidth 根 —— 断开不超过这么多分钟时不必走历史通道补。
	liveWidth = 30
	// liveChartID 与历史通道的 chartID 分开：两条通道不共用一张 chart。
	liveChartID = "tickflow-live"
	// defaultReconnects 是 LiveOptions.Reconnects 为 0 时的重连次数。
	defaultReconnects = 3
)

// Live 是一个合约的 1m 实时推送。用法：
//
//	live, _ := client.Live(sym, feed 最后一根的下一格, shinnysource.LiveOptions{})
//	defer live.Close()
//	for {
//		b, err := live.Next(ctx)
//		if err != nil { ... }            // 一旦报错，之后的 Next 都返回同一个错
//		stepped, err := feed.Push(b)
//	}
type Live struct {
	c    *Client
	sym  tickflow.Symbol
	core *liveCore
	max  int // 断一次最多重连几次

	conn   *websocket.Conn
	frames chan liveFrame
	stop   context.CancelFunc
	q      []tickflow.Bar
	err    error // 粘住：报过一次错之后一直是它
	conns  int   // 连过几次推送连接（诊断用）
	tk     *time.Ticker
}

type liveFrame struct {
	recv int64
	raw  []byte
	err  error // 非 nil ⇒ 这条连接读断了（之后这个通道关闭）
}

// Live 造一个实时推送。**它不连网**：第一次 Next 才连。
//
// startAt 是起始格（毫秒）：Feed 最后一根之后那一格的第一分钟（L12）。只交开盘 ≥ 它的根，而且第一根必须就是它 ——
// 推送里没有它 ⇒ 走历史通道补；补不齐 ⇒ ErrStartGap。
func (c *Client) Live(sym tickflow.Symbol, startAt int64, opt LiveOptions) (*Live, error) {
	if startAt <= 0 {
		return nil, fmt.Errorf("shinnysource: Live 的起始格 %d 不合法 —— 给 Feed 最后一根之后那一格的第一分钟（毫秒）", startAt)
	}
	n := opt.Reconnects
	if n == 0 {
		n = defaultReconnects
	}
	return &Live{c: c, sym: sym, core: newLiveCore(c.cfg.Calendar, sym, startAt, opt), max: n}, nil
}

// Next 返回下一根已完结的 1m（按 id 紧接、不重不漏）。阻塞到有根、出错或 ctx 结束。
// 报错之后 Live 不再可用（之后的 Next 都返回同一个错）；ctx 结束不算 Live 的错，可以换一个 ctx 接着调。
func (l *Live) Next(ctx context.Context) (tickflow.Bar, error) {
	for {
		if len(l.q) > 0 {
			b := l.q[0]
			l.q = l.q[1:]
			return b, nil
		}
		if l.err != nil {
			return tickflow.Bar{}, l.err
		}
		if l.conn == nil {
			if err := l.connect(ctx); err != nil {
				if ctx.Err() != nil {
					return tickflow.Bar{}, ctx.Err()
				}
				l.err = err
				continue
			}
		}
		if l.tk == nil {
			// 用 Ticker 不用每轮一个 Timer：帧来得比 liveTick 密时（交易时段报价每 500 ms 左右一帧），
			// 每轮重置的 Timer 可能一直不响 ⇒ 段末根等不到 G、冻结也不判
			l.tk = time.NewTicker(liveTick)
		}
		select {
		case <-ctx.Done():
			return tickflow.Bar{}, ctx.Err()
		case f, ok := <-l.frames:
			if !ok || f.err != nil {
				l.drop()
				if err := l.redial(ctx, f.err); err != nil {
					if ctx.Err() != nil {
						return tickflow.Bar{}, ctx.Err()
					}
					l.err = err
				}
				continue
			}
			bs, err := l.core.feed(f.recv, f.raw)
			l.take(bs, err)
			if l.err == nil {
				l.fill(ctx)
			}
		case <-l.tk.C:
			l.take(l.core.tick(l.c.cfg.Now()))
		}
	}
}

// Warnings：本机偏慢告警的次数与最新的窗口低端（L6：偏慢只告警不停）。
func (l *Live) Warnings() (count int, latestLow int64) { return l.core.warnings() }

// Close 断开推送连接。之后的 Next 报错。
func (l *Live) Close() error {
	l.drop()
	if l.tk != nil {
		l.tk.Stop()
	}
	if l.err == nil {
		l.err = errors.New("shinnysource: Live 已关闭")
	}
	return nil
}

func (l *Live) take(bs []tickflow.Bar, err error) {
	l.q = append(l.q, bs...)
	if err != nil {
		l.err = err
	}
}

// fill：liveCore 要补齐（起步 L12 · 重连 L8）⇒ 走历史通道拉 [from, to)，交给它，再让时间走一格把补上的根交出去。
func (l *Live) fill(ctx context.Context) {
	from, to, need := l.core.needBackfill()
	if !need {
		return
	}
	rows, err := l.c.fetch(ctx, l.core.ins, from, to, viewWidth(int((to-from)/60000)))
	if err != nil {
		l.err = fmt.Errorf("shinnysource: 补齐 [%s, %s) 时历史通道出错：%w", fmtTs(from), fmtTs(to), err)
		return
	}
	if err := l.core.backfill(rows); err != nil {
		l.err = err
		return
	}
	l.take(l.core.tick(l.c.cfg.Now()))
}

// redial（L8）：连接断了 ⇒ 最多重连 max 次（退避 liveBackoff），连上后 liveCore 按「先补齐、再接推送」接上 lastID＋1。
func (l *Live) redial(ctx context.Context, cause error) error {
	var last error
	for i := 0; i < l.max; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(liveBackoff(i)):
		}
		l.core.reconnected(l.c.cfg.Now())
		if last = l.connect(ctx); last == nil {
			return nil
		}
	}
	return fmt.Errorf("%w：断开原因 %v；重连 %d 次，最后一次：%w", ErrDisconnected, cause, l.max, last)
}

// connect 拨一条推送连接、订阅，起一个读 goroutine 把帧（连同收到时刻）送进 frames。
func (l *Live) connect(ctx context.Context) error {
	conn, err := l.c.dial(ctx)
	if err != nil {
		return err
	}
	ins := l.core.ins
	for _, rq := range []map[string]any{
		{"aid": "subscribe_quote", "ins_list": ins},
		{"aid": "set_chart", "chart_id": liveChartID, "ins_list": ins, "duration": minuteNs, "view_width": liveWidth},
		{"aid": "peek_message"},
	} {
		b, _ := json.Marshal(rq)
		if err := conn.Write(ctx, websocket.MessageText, b); err != nil {
			conn.CloseNow()
			return fmt.Errorf("shinnysource: 推送连接上发 %v 失败：%w", rq["aid"], err)
		}
	}
	rctx, stop := context.WithCancel(context.Background())
	frames := make(chan liveFrame, 64)
	go func() {
		defer close(frames)
		peek, _ := json.Marshal(map[string]any{"aid": "peek_message"})
		for {
			_, msg, err := conn.Read(rctx)
			recv := l.c.cfg.Now()
			if err != nil {
				select {
				case frames <- liveFrame{err: err}:
				case <-rctx.Done():
				}
				return
			}
			select {
			case frames <- liveFrame{recv: recv, raw: msg}:
			case <-rctx.Done():
				return
			}
			if bytes.Contains(msg, []byte(`"rtn_data"`)) {
				if err := conn.Write(rctx, websocket.MessageText, peek); err != nil {
					select {
					case frames <- liveFrame{err: err}:
					case <-rctx.Done():
					}
					return
				}
			}
		}
	}()
	l.conn, l.frames, l.stop = conn, frames, stop
	l.conns++
	return nil
}

func (l *Live) drop() {
	if l.stop != nil {
		l.stop()
	}
	if l.conn != nil {
		l.conn.CloseNow()
	}
	l.conn, l.frames, l.stop = nil, nil, nil
}
