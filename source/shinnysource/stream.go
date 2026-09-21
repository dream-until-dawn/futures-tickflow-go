package shinnysource

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"time"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
)

// v0.10 P-b1：实时源的核心状态机（design.md §十五「v0.10 起手」五 L4 / L5 / L6 / L7）。
//
// 它只吃「本机收到时刻 ＋ 一帧 DIFF」，吐出【已完结】的 1m —— 不连网、不读墙钟（本机时刻由调用方传进来），
// 所以能用合成帧与落盘帧离线验证。连推送通道、起步补齐（L12）、重连（L8）在它外面。
//
// ⛔ 它不 import Feed、Feed 也不认识它（L1：源判完结，Feed 只收已完结的根）。
//
// ⛔ 约定（评审方 09-21）：liveCore 任何「不交」都必须归到这几种原因之一 —— 等 k＋1 · 等 C＋G · 早于 startAt ·
// 等起步补齐（L12，P-b2 加：推送里最早的根晚于起始格、历史通道还没补上）；此外的一律报错。
// （5c89c1d 有两处违反：只留最近 8 根交出值 ⇒ 更早的根被改静默吞掉；id 跳号 ⇒ 永远不交、也不报）

var (
	// ErrCorrectedAfterDelivery：一根交出之后，上游对同一个 bar id 又发了不同的值（L5）。
	// 已交出的根不改；报文带上那根的最新值（恢复时要拿它与库里的比，design.md L5 📌）。
	// ⚠️ 它是判据一（「下一根已出现 ⇒ 上一根不再变」，6.36 / 6.37 两天 0 例）的反例 —— 发生了请把这次的记录报回来。
	ErrCorrectedAfterDelivery = errors.New("shinnysource: 一根交出之后又被改了值（判据一的反例）")

	// ErrClockSkew：本机时钟偏快到末根宽限 G 可能不够、会提前交出（L6 甲，评审方 09-21 裁）。
	ErrClockSkew = errors.New("shinnysource: 本机时钟偏快 —— 末根宽限 G 的前提不成立")

	// ErrIDGap：bar id 跳号 —— 推送里 k＋1 没来而更大的 id 已经来了；交出过之后下一根不是 lastID＋1；或历史通道补回来的与推送接不上（L12）。天勤的 id 在一个序列里连续 ⇒ 不猜、不跳过：停下来问（与 Feed 的 ErrPushGap 同一种处置）。
	ErrIDGap = errors.New("shinnysource: bar id 跳号")

	// ErrStartGap：起步补不齐 —— 历史通道补回来的第一根不是起始格（L12；补回来的与推送接不上 ⇒ ErrIDGap，同一根两条通道值不同 ⇒ ErrChannelsDisagree）。
	ErrStartGap = errors.New("shinnysource: 起步补不齐（第一根不是起始格）")

	// ErrChannelsDisagree：起步时历史通道与推送对同一根（两边都已完结）给了不同的值（L12 三）。此时一根都还没交出、Feed 没动过 ⇒
	// 处置是隔一会儿重新起步；与 ErrCorrectedAfterDelivery（交出之后又变，要 Close → Sync → 重建）处置不同，所以不共用名字。
	ErrChannelsDisagree = errors.New("shinnysource: 起步时历史通道与推送对同一根给了不同的值")

	// ErrSuspectedFreeze：日历说在交易时段，而时段内超过 N 没有任何真改动（L7 中途）。
	ErrSuspectedFreeze = errors.New("shinnysource: 交易时段内超过 N 没有任何真改动（疑似推送冻结）")

	// ErrStaleSnapshot：连上时日历说在交易时段，而当前那根的开盘不在 [now − N, now ＋ G] 里（L7 启动自检）。
	ErrStaleSnapshot = errors.New("shinnysource: 连上时的当前那根不在 [now − N, now ＋ G] 里（快照过旧）")
)

// LiveOptions 是实时源的可调项；零值取默认。
type LiveOptions struct {
	// G 是末根宽限（本机时钟）：时段末根在本机时刻 ≥ 收盘 ＋ G 时交出。默认 4 秒（probe.md 6.37：三处末根同值）。
	G time.Duration
	// FreezeN 是冻结判定：时段内超过它没有任何真改动 ⇒ 停。默认 2 分钟（probe.md 6.36 / 6.37：B1s 4005 / 6010 ms ⇒ N 2 分钟）。
	FreezeN time.Duration
}

// 本机偏差告警（L6 甲，design.md L6 📌）的参数 —— 评审方直接定，不从读数里挑、不许按读数调。
const (
	guardW    = int64(60000) // 窗口 60 秒，只含当前段
	guardK    = 30           // 样本 < 30 ⇒ 不判
	guardKLag = int64(200)   // 投递延迟余量：K 线帧比报价帧晚（6.37 kLag 99.9 分位 173 ms ⇒ 取 200）
)

// liveCore 是实时源的状态机。
type liveCore struct {
	cal     tickflow.Calendar
	key     tickflow.ProductKey
	ins     string
	g, n    int64 // G、FreezeN（毫秒）
	startAt int64 // 只交开盘时刻 ≥ 它的根（起步接缝由 L12 补齐，这里只管不交更早的）

	snap    map[string]any
	rows    map[int64]Row // 每个 id 最近一次的值
	firstID int64         // 第一根交出的 id（0 ＝ 还没交过）；[firstID, lastID] 里的根都交出过 ⇒ 值变即 L5
	filled  bool          // 起步接缝已接上（L12）：startAt 那一格在 rows 里了（推送自带，或历史通道补上）
	lastID  int64         // 已交出（或早于起点而跳过）的最后一根的 id
	started bool          // 启动自检做过
	lastChg int64         // 最后一次真改动（诞生或值变）的本机时刻

	// L6：只含当前段的样本窗口（升序的 D，与按到达顺序的 [本机时刻, D]）
	win    []int64
	fifo   [][2]int64
	winSeg int64
	warns  int
	lastLo int64
}

func newLiveCore(cal tickflow.Calendar, sym tickflow.Symbol, startAt int64, opt LiveOptions) *liveCore {
	g, n := opt.G, opt.FreezeN
	if g == 0 {
		g = 4 * time.Second
	}
	if n == 0 {
		n = 2 * time.Minute
	}
	return &liveCore{cal: cal, key: sym.ProductKey(), ins: sym.Native(), g: g.Milliseconds(), n: n.Milliseconds(), startAt: startAt,
		snap: map[string]any{}, rows: map[int64]Row{}, winSeg: -1}
}

// feed 吃一帧：更新快照、查修正、喂 L6 的窗口、做启动自检，交出因「下一根出现」而完结的根（L4）。
func (c *liveCore) feed(recv int64, raw []byte) ([]tickflow.Bar, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // 纳秒时间戳超出 float64 的整数精度
	var m struct {
		Aid  string           `json:"aid"`
		Data []map[string]any `json:"data"`
	}
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("shinnysource: 推送帧解不开：%w", err)
	}
	if m.Aid != "rtn_data" {
		return nil, nil
	}
	touched := map[int64]bool{}
	var qdt string
	for _, d := range m.Data {
		if q := obj(d, "quotes", c.ins); q != nil {
			if s, ok := q["datetime"].(string); ok {
				qdt = s
			}
		}
		if kd := obj(d, "klines", c.ins, minuteKey, "data"); kd != nil {
			for k := range kd {
				if id, err := strconv.ParseInt(k, 10, 64); err == nil {
					touched[id] = true
				}
			}
		}
		merge(c.snap, d)
	}
	if qdt != "" {
		if err := c.guard(recv, qdt); err != nil {
			return nil, err
		}
	}
	data := obj(c.snap, "klines", c.ins, minuteKey, "data")
	ids := make([]int64, 0, len(touched))
	for id := range touched {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		b := obj(data, strconv.FormatInt(id, 10))
		if b == nil {
			continue // 离窗（null 删键）：不算改动
		}
		row, err := parseRow(id, b)
		if err != nil {
			return nil, fmt.Errorf("shinnysource: 推送里的根：%w", err)
		}
		old, seen := c.rows[id]
		if seen && sameRow(old, row) {
			continue // 原值重发（6.37：上一段末根在下一段开盘前被整根重发）⇒ 不算改动
		}
		// L5 的射程是所有交出过的根（不是「最近几根」：拿判据一去限定检验判据一的范围是循环论证）。
		// 交出之后 c.rows[id] 就停在交出时的值（之后一改就报、不再写回）⇒ 不必另存一份
		if c.firstID != 0 && id >= c.firstID && id <= c.lastID {
			return nil, fmt.Errorf("%w：id %d（开盘 %s）交出时 %s，现在 %s", ErrCorrectedAfterDelivery, id, fmtTs(row.Datetime/1e6), showRow(old), showRow(row))
		}
		c.rows[id] = row
		c.lastChg = recv
	}
	if !c.started && len(c.rows) > 0 {
		c.started = true
		if err := c.startupCheck(recv); err != nil {
			return nil, err
		}
	}
	return c.deliver(recv)
}

// tick 让时间走：交出等够了 G 的时段末根（L4），并查冻结（L7 中途）。没有新帧时由调用方按时调用。
func (c *liveCore) tick(now int64) ([]tickflow.Bar, error) {
	out, err := c.deliver(now)
	if err != nil {
		return out, err
	}
	if err := c.freezeCheck(now); err != nil {
		return out, err
	}
	return out, nil
}

// deliver 按 id 升序交出已完结的根：k＋1 已出现，或 k 是时段末根且 now ≥ 收盘 ＋ G（L4）。第一根不完结就停。
func (c *liveCore) deliver(now int64) ([]tickflow.Bar, error) {
	var ids []int64
	for id := range c.rows {
		if id > c.lastID {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	if !c.startReady() {
		return nil, nil // 等起步补齐（L12）
	}
	var out []tickflow.Bar
	for _, id := range ids {
		row := c.rows[id]
		b, err := rowBar(row, c.cal, c.key)
		if err != nil {
			return out, err
		}
		if b.Ts < c.startAt {
			c.lastID = id // 早于起点的（view_width 带来的历史）不交，也不再看
			continue
		}
		// 交出过之后，下一根必须是 lastID＋1（评审方 09-21 补：段末根按 C＋G 交出之后，下一段来的若不是 lastID＋1，
		// 「当前这一根的 k＋1」那条检查照不到它 —— 跳过的那根从没被查过）
		if c.firstID != 0 && id != c.lastID+1 {
			return out, fmt.Errorf("%w：上一根交出的是 id %d，下一根来的是 id %d（开盘 %s），缺 id %d", ErrIDGap, c.lastID, id, fmtTs(b.Ts), c.lastID+1)
		}
		_, nextSeen := c.rows[id+1]
		done := nextSeen
		if !done && ids[len(ids)-1] > id+1 {
			return out, fmt.Errorf("%w：id %d（开盘 %s）之后缺 id %d，而已经来了 id %d", ErrIDGap, id, fmtTs(b.Ts), id+1, ids[len(ids)-1])
		}
		if !done {
			end, err := c.isSegmentEnd(b)
			if err != nil {
				return out, err
			}
			done = end && now >= b.TsEnd+c.g
		}
		if !done {
			break
		}
		out = append(out, b)
		c.lastID = id
		if c.firstID == 0 {
			c.firstID = id
		}
	}
	return out, nil
}

// startReady（L12）：起步接缝接上了没有。没给起始格（startAt 为 0）⇒ 接上；
// 否则 rows 里开盘 ≥ startAt 的最早那根：就是 startAt ⇒ 接上；更晚 ⇒ 等历史通道补（needBackfill / backfill）；一根都没有 ⇒ 等推送。
func (c *liveCore) startReady() bool {
	if c.filled || c.startAt == 0 {
		return true
	}
	min, ok := c.earliestFrom(c.startAt)
	if ok && min == c.startAt {
		c.filled = true
	}
	return c.filled
}

// earliestFrom：rows 里开盘 ≥ from 的最早那根的开盘时刻。
func (c *liveCore) earliestFrom(from int64) (int64, bool) {
	best, ok := int64(0), false
	for _, r := range c.rows {
		if ts := r.Datetime / 1e6; ts >= from && (!ok || ts < best) {
			best, ok = ts, true
		}
	}
	return best, ok
}

// needBackfill（L12 第二步）：推送里开盘 ≥ 起始格的最早那根晚于起始格 ⇒ 要向历史通道要 [from, to)（毫秒）。
func (c *liveCore) needBackfill() (from, to int64, need bool) {
	if c.filled || c.startAt == 0 {
		return 0, 0, false
	}
	min, ok := c.earliestFrom(c.startAt)
	if !ok || min == c.startAt {
		return 0, 0, false
	}
	return c.startAt, min, true
}

// backfill（L12 第二、三步）：把历史通道拉回来的行并进来。
//
//	只收开盘在 [startAt, 推送最早那根) 里的；更早的、更新的（推送还没发到）都不要；落在推送已有的 id 上的 ⇒ 两边都已完结（推送里 id＋1 已在）才逐位比，不等报 ErrChannelsDisagree；推送里还在变的当前根跳过
//	补回来的第一根必须是起始格（否则 ErrStartGap）；补回来的 id 必须连续、且最后一根接上推送最早那根（否则 ErrIDGap）
//
// ⚠️ 起始格必须是一个交易分钟（Feed 最后一根之后那一格的第一分钟，由 Feed 给出）；给了休市时刻 ⇒ 永远等不到它 ⇒ 这里报 ErrStartGap。
// ⚠️ 前提：历史通道与推送通道的 bar id 是同一套编号（都来自天勤同一条 K 线序列；推论，实盘那一次要验）。
func (c *liveCore) backfill(rows []Row) error {
	from, to, need := c.needBackfill()
	if !need {
		return nil
	}
	pushFirst := int64(-1)
	for id, r := range c.rows {
		if r.Datetime/1e6 == to {
			pushFirst = id
		}
	}
	sorted := append([]Row(nil), rows...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	var fill []Row
	for _, r := range sorted {
		ts := r.Datetime / 1e6
		if ts < from {
			continue
		}
		if old, ok := c.rows[r.ID]; ok {
			// 只比两边都已完结的（推送里 id＋1 已在 ⇒ 判据一）；推送里还在变的当前根跳过、以推送为准 ——
			// 真的 fetch 拉到「此刻」，给回来的最后一根几乎总是那根当前根，两边取自不同时刻，不等是常态（评审方 09-21 实测误停）。
			// 跳过不漏：它交出之后若再变，L5 的 [firstID, lastID] 那条查得到。
			if _, done := c.rows[r.ID+1]; done && !sameRow(old, r) {
				return fmt.Errorf("%w：id %d（开盘 %s）历史通道 %s，推送 %s", ErrChannelsDisagree, r.ID, fmtTs(ts), showRow(r), showRow(old))
			}
			continue
		}
		if ts >= to {
			continue // 比推送最早那根还新、推送还没发到的：不从历史通道收，等推送（只收 [from, to)）
		}
		fill = append(fill, r)
	}
	if len(fill) == 0 || fill[0].Datetime/1e6 != from {
		first := "（一根都没有）"
		if len(fill) > 0 {
			first = fmtTs(fill[0].Datetime / 1e6)
		}
		return fmt.Errorf("%w：历史通道补回来的第一根是 %s，起始格是 %s", ErrStartGap, first, fmtTs(from))
	}
	for i := 1; i < len(fill); i++ {
		if fill[i].ID != fill[i-1].ID+1 {
			return fmt.Errorf("%w：历史通道补回来的 id %d 之后是 %d", ErrIDGap, fill[i-1].ID, fill[i].ID)
		}
	}
	if last := fill[len(fill)-1].ID; last+1 != pushFirst {
		return fmt.Errorf("%w：历史通道补回来的最后一根 id %d，推送最早那根 id %d —— 接不上", ErrIDGap, last, pushFirst)
	}
	for _, r := range fill {
		c.rows[r.ID] = r
	}
	c.filled = true
	return nil
}

// isSegmentEnd：b 的收盘时刻是它那个交易日某一段的收盘（10:15 · 11:30 · 15:00 · 23:00 …，按日历认，不写死）。
func (c *liveCore) isSegmentEnd(b tickflow.Bar) (bool, error) {
	d, err := c.cal.DayOf(c.key, b.TradingDay)
	if err != nil {
		return false, fmt.Errorf("shinnysource: 问不到 %s 的时段：%w", b.TradingDay, err)
	}
	for _, s := range d.Sessions {
		if s.End == b.TsEnd {
			return true, nil
		}
	}
	return false, nil
}

// segmentAt：本机时刻 now 落在哪一段（日历）；不在任何段里 ⇒ ok 为假。
func (c *liveCore) segmentAt(now int64) (tickflow.Session, bool) {
	d, err := c.cal.DayAt(c.key, now)
	if err != nil {
		return tickflow.Session{}, false
	}
	for _, s := range d.Sessions {
		if s.Contains(now) {
			return s, true
		}
	}
	return tickflow.Session{}, false
}

// startupCheck（L7 启动自检）：日历说此刻在交易时段 ⇒ 当前那根（最大的 id）的开盘必须在 [now − N, now ＋ G] 里。
// 上端放宽 G：下一段的第一根可以在开盘前就诞生（6.37：13:30 那根在本机 13:29:59.850），再加本机偏差。
func (c *liveCore) startupCheck(now int64) error {
	if _, in := c.segmentAt(now); !in {
		return nil
	}
	var cur int64 = -1
	for id := range c.rows {
		cur = max(cur, id)
	}
	ts := c.rows[cur].Datetime / 1e6
	if ts < now-c.n || ts > now+c.g {
		return fmt.Errorf("%w：本机 %s 连上，当前那根（id %d）开盘 %s", ErrStaleSnapshot, fmtMs(now), cur, fmtTs(ts))
	}
	return nil
}

// freezeCheck（L7 中途）：只算时段内的时间 —— 计时起点 ＝ max(最后一次真改动, 当前段段首)（评审方 09-21：跨休息计时是 B1s 午休同一个病）。
func (c *liveCore) freezeCheck(now int64) error {
	s, in := c.segmentAt(now)
	if !in {
		return nil
	}
	from := max(c.lastChg, s.Start)
	if now-from > c.n {
		return fmt.Errorf("%w：本机 %s，时段内自 %s 起没有真改动", ErrSuspectedFreeze, fmtMs(now), fmtMs(from))
	}
	return nil
}

// guard（L6 甲）：D ＝ 本机收到 − quote.datetime，只取 quote.datetime 不早于当前段段首的；窗口 60 秒、只含当前段；
// 样本 ≥ 30 才判：高端（第 min(ceil(0.99n), n−1) 个）＋ 200 > G/2 ⇒ ErrClockSkew；低端（第 max(ceil(0.01n), 2) 个）< −G/2 ⇒ 告警计数。
func (c *liveCore) guard(recv int64, qdt string) error {
	t, err := time.ParseInLocation("2006-01-02 15:04:05.000000", qdt, tickflow.CST)
	if err != nil {
		return nil // 读不出的 datetime 不进样本（不因诊断量打断取数）
	}
	s, in := c.segmentAt(recv)
	if !in || t.UnixMilli() < s.Start {
		return nil
	}
	if s.Start != c.winSeg { // 段首一到清空
		c.win, c.fifo, c.winSeg = c.win[:0], c.fifo[:0], s.Start
	}
	for len(c.fifo) > 0 && c.fifo[0][0] <= recv-guardW {
		old := c.fifo[0][1]
		j := sort.Search(len(c.win), func(j int) bool { return c.win[j] >= old })
		c.win = append(c.win[:j], c.win[j+1:]...)
		c.fifo = c.fifo[1:]
	}
	d := recv - t.UnixMilli()
	j := sort.Search(len(c.win), func(j int) bool { return c.win[j] >= d })
	c.win = append(c.win, 0)
	copy(c.win[j+1:], c.win[j:])
	c.win[j] = d
	c.fifo = append(c.fifo, [2]int64{recv, d})
	n := len(c.win)
	if n < guardK {
		return nil
	}
	hi := c.win[min(int(math.Ceil(0.99*float64(n))), n-1)-1] + guardKLag
	lo := c.win[max(int(math.Ceil(0.01*float64(n))), 2)-1]
	if hi > c.g/2 {
		return fmt.Errorf("%w：本机 %s，窗口高端 %d ms（含投递余量 %d）> G/2 ＝ %d ms", ErrClockSkew, fmtMs(recv), hi, guardKLag, c.g/2)
	}
	if lo < -c.g/2 {
		c.warns++
		c.lastLo = lo
	}
	return nil
}

// warnings：偏慢告警的次数与最新的窗口低端（L6：告警走「次数 ＋ 最新值」，不逐次报）。
func (c *liveCore) warnings() (int, int64) { return c.warns, c.lastLo }

func sameRow(a, b Row) bool {
	x := [...]float64{a.Open, a.High, a.Low, a.Close, a.Volume, a.CloseOI}
	y := [...]float64{b.Open, b.High, b.Low, b.Close, b.Volume, b.CloseOI}
	for i := range x {
		if math.Float64bits(x[i]) != math.Float64bits(y[i]) {
			return false
		}
	}
	return a.Datetime == b.Datetime
}

func showRow(r Row) string {
	return fmt.Sprintf("O %g H %g L %g C %g V %g OI %g", r.Open, r.High, r.Low, r.Close, r.Volume, r.CloseOI)
}

func fmtMs(ms int64) string {
	return time.UnixMilli(ms).In(tickflow.CST).Format("2006-01-02 15:04:05.000")
}
