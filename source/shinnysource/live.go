package shinnysource

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
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

// ErrConsumerStalled：调用方太久没调 Next，收下还没处理的帧超过了 liveQueueMax。读协程从不因调用方阻塞（帧上的本机时刻
// 必须是真的收到时刻，L6 / L4 都靠它）⇒ 只能靠这个上限挡住内存；到了上限就停，不静默丢帧。
// 处置与 ErrDisconnected 相同：从 Feed 最后一根的下一格重新开一个 Live。
var ErrConsumerStalled = errors.New("shinnysource: 调用方太久没取推送的帧")

var (
	// liveTick 是没有新帧时让时间走的间隔。是变量只为离线测试调小。
	liveTick = 250 * time.Millisecond
	// liveBackoff 是第 i 次重连之前等多久（i 从 0 起）。是变量只为离线测试调成 0。
	liveBackoff = func(i int) time.Duration { return time.Second << i }
	// liveQueueMax 是收下还没处理的帧的上限（交易时段每秒两三帧 ⇒ 约一个多小时不取才到）。是变量只为离线测试调小。
	liveQueueMax = 10000
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

	conn  *websocket.Conn
	fq    *frameQueue
	stop  context.CancelFunc
	q     []tickflow.Bar
	err   error // 粘住：报过一次错之后一直是它
	cause error // 上一条连接断开的原因（重连报错时带上）
	conns int   // 连过几次推送连接（诊断用）
	tk    *time.Ticker
}

type liveFrame struct {
	recv int64
	raw  []byte
	err  error // 非 nil ⇒ 这条连接读断了（或收下的帧超过上限），它是这条连接的最后一帧
}

// frameQueue：读协程往里放、Next 往外取。放永远不阻塞（评审方 09-21：cff2188 用容量 64 的通道，调用方一慢读协程就停、
// peek 也不发 ⇒ 服务器攒帧 ⇒ 之后读到的帧打上的「收到时刻」整段推迟 ⇒ 假的 ErrClockSkew，实测）。
type frameQueue struct {
	mu   sync.Mutex
	buf  []liveFrame
	note chan struct{} // 容量 1：有新帧
}

func newFrameQueue() *frameQueue { return &frameQueue{note: make(chan struct{}, 1)} }

// put 放一帧；超过上限 ⇒ 放一个 ErrConsumerStalled 的收尾帧并返回 false（读协程随之停）。
func (q *frameQueue) put(f liveFrame) bool {
	q.mu.Lock()
	ok := len(q.buf) < liveQueueMax
	if !ok {
		f = liveFrame{err: fmt.Errorf("%w：已收下 %d 帧没处理（上限 liveQueueMax）—— 再收下去内存不封顶，而丢帧会让交出的根不可信", ErrConsumerStalled, len(q.buf))}
	}
	q.buf = append(q.buf, f)
	q.mu.Unlock()
	select {
	case q.note <- struct{}{}:
	default:
	}
	return ok
}

func (q *frameQueue) takeAll() []liveFrame {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := q.buf
	q.buf = nil
	return out
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
// 报错之后 Live 不再可用（之后的 Next 都返回同一个错）；ctx 结束不算 Live 的错，可以换一个 ctx 接着调
// （重连退避中、补齐中 ctx 到期都一样：下一次 Next 从断下的地方接着来）。
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
		if err := ctx.Err(); err != nil {
			return tickflow.Bar{}, err
		}
		if l.conn == nil {
			if err := l.redial(ctx); err != nil {
				if ctx.Err() != nil {
					return tickflow.Bar{}, ctx.Err()
				}
				l.err = err
				continue
			}
		}
		// 每一轮都问要不要补齐（不只在喂完一帧之后）：补齐中 ctx 到期的话，下一次 Next 不必等下一帧来才重试
		if err := l.fill(ctx); err != nil {
			return tickflow.Bar{}, err
		}
		if l.err != nil || len(l.q) > 0 {
			continue
		}
		if l.tk == nil {
			// 没有帧时靠它醒来让时间走（段末根等 G、冻结）。有帧时每次醒来也都 tick（见下）⇒ 帧再密冻结也照判
			// （cff2188 只在计时器响时 tick、且每轮重置一个 Timer ⇒ 帧比 liveTick 密时冻结永远不判，突变实测）
			l.tk = time.NewTicker(liveTick)
		}
		select {
		case <-ctx.Done():
			return tickflow.Bar{}, ctx.Err()
		case <-l.fq.note:
		case <-l.tk.C:
		}
		// 先把收下的帧按收到顺序全喂完，再拿此刻让时间走 —— 否则 tick 看到的是还没喂进去的那些帧之前的状态（例如冻结计时）
		for _, f := range l.fq.takeAll() {
			if f.err != nil {
				l.drop()
				if errors.Is(f.err, ErrConsumerStalled) {
					l.err = f.err
				} else {
					l.cause = f.err
				}
				break
			}
			bs, err := l.core.feed(f.recv, f.raw)
			l.take(bs, err)
			if l.err != nil {
				break
			}
		}
		if l.err == nil && l.conn != nil {
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
// ctx 到期 ⇒ 返回 ctx 的错、不粘（下一次 Next 会再问一次要不要补齐）；其余的错粘在 l.err 上。
func (l *Live) fill(ctx context.Context) error {
	from, to, need := l.core.needBackfill()
	if !need {
		return nil
	}
	rows, err := l.c.fetch(ctx, l.core.ins, from, to, viewWidth(int((to-from)/60000)))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		l.err = fmt.Errorf("shinnysource: 补齐 [%s, %s) 时历史通道出错：%w", fmtTs(from), fmtTs(to), err)
		return nil
	}
	if err := l.core.backfill(rows); err != nil {
		l.err = err
		return nil
	}
	l.take(l.core.tick(l.c.cfg.Now()))
	return nil
}

// redial：连推送连接。第一次直接连；之后（L8，上一条断了）最多试 max 次、每次之前退避 liveBackoff。
// ctx 到期 ⇒ 返回 ctx 的错，下一次 Next 再从这里来（liveCore 那边「换了连接」在 connect 里说，不在这个循环里）。
func (l *Live) redial(ctx context.Context) error {
	if l.conns == 0 {
		return l.connect(ctx)
	}
	var last error
	for i := 0; i < l.max; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(liveBackoff(i)):
		}
		if last = l.connect(ctx); last == nil {
			return nil
		}
	}
	return fmt.Errorf("%w：断开原因 %v；重连 %d 次，最后一次：%w", ErrDisconnected, l.cause, l.max, last)
}

// connect 拨一条推送连接、订阅，起一个读 goroutine 把帧（连同收到时刻）送进 frames。
//
// 「换了连接」绑在这里（评审方 09-21）：只要之前连上过，先告诉 liveCore（它的 reconnected 可以重复调）——
// 绑在重连循环里的话，退避中 ctx 一到期、下一次 Next 直接连上，liveCore 就不知道换过连接（cff2188，实测跳号）。
func (l *Live) connect(ctx context.Context) error {
	if l.conns > 0 {
		l.core.reconnected(l.c.cfg.Now())
	} else {
		// 第一次连：冻结计时从连上这一刻起（不从段首 —— 段中连上时还一帧没收到，从段首算会在第一个 tick 就判冻结；
		// cff2188 只在 Ticker 响时 tick、第一帧几乎总先到，这个竞态藏着；改成每次醒来都 tick 之后实测撞上）
		l.core.lastChg = l.c.cfg.Now()
	}
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
	fq := newFrameQueue()
	go func() {
		peek, _ := json.Marshal(map[string]any{"aid": "peek_message"})
		for {
			_, msg, err := conn.Read(rctx)
			recv := l.c.cfg.Now()
			if err != nil {
				if rctx.Err() == nil {
					fq.put(liveFrame{err: err})
				}
				return
			}
			if !fq.put(liveFrame{recv: recv, raw: msg}) {
				return
			}
			if bytes.Contains(msg, []byte(`"rtn_data"`)) {
				if err := conn.Write(rctx, websocket.MessageText, peek); err != nil {
					if rctx.Err() == nil {
						fq.put(liveFrame{err: err})
					}
					return
				}
			}
		}
	}()
	l.conn, l.fq, l.stop = conn, fq, stop
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
	l.conn, l.stop = nil, nil
}
