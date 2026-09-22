package shinnysource

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
)

// v0.10 P-b1：实时源核心状态机（stream.go）的合成测试。设计 design.md §十五「v0.10 起手」五 L4 / L5 / L6 / L7。
// 日历 testCalendar（rb，20260903 – 0908；0907 周一的夜盘在周五 0904 晚上 21:00–23:00）。

var liveSym = tickflow.Symbol{Exchange: tickflow.SHFE, Product: "rb", YearMon: 2610}

func at2(y, mo, d, h, m, s, ms int) int64 {
	return time.Date(y, time.Month(mo), d, h, m, s, ms*1e6, tickflow.CST).UnixMilli()
}

// kFrame 造一帧 K 线：id 那根开盘 open（毫秒），收盘价 c（其余字段由 c 推出）；quote 非空 ⇒ 同帧带 quote.datetime。
func kFrame(id, open int64, c float64, quote string) []byte {
	d := map[string]any{"klines": map[string]any{liveSym.Native(): map[string]any{minuteKey: map[string]any{"data": map[string]any{
		strconv.FormatInt(id, 10): map[string]any{"datetime": open * 1e6, "open": c, "high": c + 1, "low": c - 1, "close": c, "volume": 1.0, "close_oi": 100.0}}}}}}
	if quote != "" {
		d["quotes"] = map[string]any{liveSym.Native(): map[string]any{"datetime": quote}}
	}
	b, _ := json.Marshal(map[string]any{"aid": "rtn_data", "data": []any{d}})
	return b
}

// qFrame 造一帧只带报价的：quote.datetime ＝ srv（毫秒）。
func qFrame(srv int64) []byte {
	b, _ := json.Marshal(map[string]any{"aid": "rtn_data", "data": []any{map[string]any{
		"quotes": map[string]any{liveSym.Native(): map[string]any{"datetime": time.UnixMilli(srv).In(tickflow.CST).Format("2006-01-02 15:04:05.000000")}}}}})
	return b
}

func mustFeed(t *testing.T, c *liveCore, recv int64, raw []byte) []tickflow.Bar {
	t.Helper()
	out, err := c.feed(recv, raw)
	if err != nil {
		t.Fatalf("feed %s：%v", fmtMs(recv), err)
	}
	return out
}

func mustTick(t *testing.T, c *liveCore, now int64) []tickflow.Bar {
	t.Helper()
	out, err := c.tick(now)
	if err != nil {
		t.Fatalf("tick %s：%v", fmtMs(now), err)
	}
	return out
}

// guard: L4 —— k 在 k＋1 出现时交出（不早）；交出的根按日历归日、字段照 rowBar；早于起点的不交。
func TestLiveDeliversWhenNextAppears(t *testing.T) {
	c := newLiveCore(testCalendar(t), liveSym, at2(2026, 9, 4, 9, 31, 0, 0), LiveOptions{})
	m0 := at2(2026, 9, 4, 9, 30, 0, 0)
	if out := mustFeed(t, c, m0+100, kFrame(100, m0, 3000, "")); len(out) != 0 {
		t.Fatalf("只有 9:30 那根：交出 %d 根，应为 0", len(out))
	}
	if out := mustFeed(t, c, m0+60100, kFrame(101, m0+60000, 3001, "")); len(out) != 0 {
		t.Fatalf("9:31 那根出现：9:30 那根早于起点 9:31、不交；交出 %d 根", len(out))
	}
	mustFeed(t, c, m0+90000, kFrame(101, m0+60000, 3002, "")) // 9:31 那根还在变
	out := mustFeed(t, c, m0+120100, kFrame(102, m0+120000, 3003, ""))
	if len(out) != 1 || out[0].Ts != m0+60000 || out[0].Close != 3002 || out[0].TradingDay != 20260904 {
		t.Fatalf("9:32 那根出现：交出 %+v，应只交 9:31 那根（收 3002、交易日 0904）", out)
	}
}

// guard: L4 时段末根 —— 10:14 那根（收盘 10:15）没有「下一根」可等：本机时刻 ≥ 10:15 ＋ G 才交；差 1 ms 不交。
func TestLiveSegmentEndWaitsG(t *testing.T) {
	c := newLiveCore(testCalendar(t), liveSym, 0, LiveOptions{})
	m := at2(2026, 9, 4, 10, 14, 0, 0)
	mustFeed(t, c, m+100, kFrame(200, m, 3000, ""))
	if out := mustTick(t, c, at2(2026, 9, 4, 10, 15, 3, 999)); len(out) != 0 {
		t.Fatalf("10:15:03.999：交出 %d 根，应为 0（G ＝ 4 秒）", len(out))
	}
	if out := mustTick(t, c, at2(2026, 9, 4, 10, 15, 4, 0)); len(out) != 1 || out[0].Ts != m {
		t.Fatalf("10:15:04：交出 %+v，应交 10:14 那根", out)
	}
	// 对照：不是时段末根（9:30 那根）⇒ 等多久都不交
	d := newLiveCore(testCalendar(t), liveSym, 0, LiveOptions{})
	n := at2(2026, 9, 4, 9, 30, 0, 0)
	mustFeed(t, d, n+100, kFrame(300, n, 3000, ""))
	if out := mustTick(t, d, n+60000+30000); len(out) != 0 {
		t.Fatalf("对照：9:30 那根不是时段末根，却交了 %d 根", len(out))
	}
}

// guard: L5 —— 原值重发不报（6.37 的形状）；交出之后同一根值变了 ⇒ ErrCorrectedAfterDelivery，报文带最新值。
func TestLiveCorrectionAfterDelivery(t *testing.T) {
	c := newLiveCore(testCalendar(t), liveSym, 0, LiveOptions{})
	m := at2(2026, 9, 4, 9, 30, 0, 0)
	mustFeed(t, c, m+100, kFrame(100, m, 3000, ""))
	if out := mustFeed(t, c, m+60100, kFrame(101, m+60000, 3001, "")); len(out) != 1 {
		t.Fatalf("前提没成立：9:30 那根没交出")
	}
	mustFeed(t, c, m+70000, kFrame(100, m, 3000, "")) // 原值重发 ⇒ 不报
	_, err := c.feed(m+80000, kFrame(100, m, 3009, ""))
	if !errors.Is(err, ErrCorrectedAfterDelivery) || !strings.Contains(err.Error(), "C 3009") {
		t.Errorf("交出后改值：%v，应 Is ErrCorrectedAfterDelivery 且报文带最新值 C 3009", err)
	}
}

// guard: L5 的射程是【所有交出过的根】，不是「最近几根」（评审方 09-21 实测：5c89c1d 只留最近 8 根，连交 10 根后改最早那根 ⇒ 不报）——
// 拿判据一（「交出后不会再变」）去限定检验判据一的范围，是循环论证。对照：早于起点、从没交出的根被改了值 ⇒ 不报。
func TestLiveCorrectionOfOldDeliveredBar(t *testing.T) {
	m := at2(2026, 9, 4, 9, 30, 0, 0)
	c := newLiveCore(testCalendar(t), liveSym, m+60000, LiveOptions{})
	mustFeed(t, c, m+100, kFrame(99, m, 2999, "")) // 早于起点（view_width 带来的历史那种）
	for i := int64(0); i <= 10; i++ {
		mustFeed(t, c, m+60000+i*60000+100, kFrame(100+i, m+60000+i*60000, 3000+float64(i), ""))
	}
	if c.lastID != 109 {
		t.Fatalf("前提没成立：已交出到 id %d，应到 109", c.lastID)
	}
	if _, err := c.feed(m+12*60000, kFrame(99, m, 2000, "")); err != nil {
		t.Errorf("对照：从没交出的 id 99 改了值：%v，应不报", err)
	}
	_, err := c.feed(m+12*60000+100, kFrame(100, m+60000, 3999, ""))
	if !errors.Is(err, ErrCorrectedAfterDelivery) || !strings.Contains(err.Error(), "C 3000") || !strings.Contains(err.Error(), "C 3999") {
		t.Errorf("连交 10 根后改最早那根（id 100）：%v，应 Is ErrCorrectedAfterDelivery、报文带改前 C 3000 与改后 C 3999", err)
	}
}

// guard: id 跳号 ⇒ 报错，不永远沉默（评审方 09-21 实测：5c89c1d 在 101 缺时交 0 根、feed 与 tick 都不报）——
// 「不交」只能归到三种原因之一：等 k＋1 · 等 C＋G · 早于起点；k＋1 不在而更大的 id 已经来了，不是这三种里的任何一种。
func TestLiveIDGapIsAnError(t *testing.T) {
	c := newLiveCore(testCalendar(t), liveSym, 0, LiveOptions{})
	m := at2(2026, 9, 4, 9, 30, 0, 0)
	mustFeed(t, c, m+100, kFrame(100, m, 3000, ""))
	var err error
	for i := int64(2); i <= 6 && err == nil; i++ {
		if _, err = c.feed(m+i*60000+100, kFrame(100+i, m+i*60000, 3000, "")); err == nil {
			_, err = c.tick(m + i*60000 + 30000)
		}
	}
	if !errors.Is(err, ErrIDGap) || !strings.Contains(err.Error(), "101") {
		t.Errorf("100 之后缺 101、102 起照来：%v，应 Is ErrIDGap 且报出缺的 id 101", err)
	}
}

// guard: L6 —— 持续偏快 ⇒ ErrClockSkew；偏慢 ⇒ 只计数告警、不停；n＝30 里单帧尖峰 ⇒ 不停（取秩修正）。
func TestLiveClockGuard(t *testing.T) {
	base := at2(2026, 9, 4, 9, 40, 0, 0)
	run := func(d func(i int) int64, n int) (*liveCore, error) {
		c := newLiveCore(testCalendar(t), liveSym, 0, LiveOptions{})
		for i := 0; i < n; i++ {
			r := base + int64(i)*500
			if _, err := c.feed(r, qFrame(r-d(i))); err != nil {
				return c, err
			}
		}
		return c, nil
	}
	if _, err := run(func(int) int64 { return 1900 }, 60); !errors.Is(err, ErrClockSkew) {
		t.Errorf("D 恒 1900（高端 2100 > 2000）：%v，应 ErrClockSkew", err)
	}
	c, err := run(func(int) int64 { return -2500 }, 60)
	if err != nil {
		t.Fatalf("D 恒 −2500：%v，应不停", err)
	}
	if n, lo := c.warnings(); n == 0 || lo != -2500 {
		t.Errorf("D 恒 −2500：告警 %d 次、最新低端 %d，应 > 0 次、−2500", n, lo)
	}
	if _, err := run(func(i int) int64 {
		if i == 5 {
			return 5000
		}
		return 0
	}, 30); err != nil {
		t.Errorf("n＝30、单帧 ＋5000：%v，应不停", err)
	}
}

// guard: L7 中途 —— 时段内超过 N 没有真改动 ⇒ ErrSuspectedFreeze；计时只算时段内（跨午休 · 跨小节休息 · 跨日夜盘都不许误停）。
func TestLiveFreezeOnlyCountsSessionTime(t *testing.T) {
	c := newLiveCore(testCalendar(t), liveSym, 0, LiveOptions{})
	m := at2(2026, 9, 4, 11, 29, 0, 0)
	mustFeed(t, c, m+59000, kFrame(100, m, 3000, "")) // 最后一次改动 11:29:59
	for _, now := range []int64{
		at2(2026, 9, 4, 13, 31, 59, 0), // 午休之后：段首 13:30 起才 1 分 59 秒
		at2(2026, 9, 4, 10, 31, 0, 0),  // （换个起点）小节休息之后
		at2(2026, 9, 4, 21, 1, 0, 0),   // 15:00 → 21:00 夜盘（属 0907）
	} {
		d := newLiveCore(testCalendar(t), liveSym, 0, LiveOptions{})
		d.lastChg = m + 59000
		if now < m { // 小节休息那格：最后一次改动在 10:14:59
			d.lastChg = at2(2026, 9, 4, 10, 14, 59, 0)
		}
		if err := d.freezeCheck(now); err != nil {
			t.Errorf("本机 %s：%v，应不停（计时只算时段内）", fmtMs(now), err)
		}
	}
	if err := c.freezeCheck(at2(2026, 9, 4, 13, 32, 1, 0)); !errors.Is(err, ErrSuspectedFreeze) {
		t.Errorf("午休后开盘 2 分 1 秒没有改动：%v，应 ErrSuspectedFreeze", err)
	}
	if err := c.freezeCheck(at2(2026, 9, 4, 12, 30, 0, 0)); err != nil {
		t.Errorf("午休中：%v，应不判", err)
	}
}

// guard: 原值重发不算真改动 —— 最后一次真改动之后只剩原值重发（上游只重发、数据不动），超过 N 仍判冻结；
// 若把重发也算成改动，冻结计时会被重发一直刷新、永远查不出来。
func TestLiveResendDoesNotResetFreeze(t *testing.T) {
	c := newLiveCore(testCalendar(t), liveSym, 0, LiveOptions{})
	m := at2(2026, 9, 4, 9, 40, 0, 0)
	mustFeed(t, c, m+100, kFrame(100, m, 3000, ""))
	for s := int64(10); s <= 130; s += 10 {
		if _, err := c.feed(m+s*1000, kFrame(100, m, 3000, "")); err != nil {
			t.Fatalf("第 %d 秒的原值重发：%v", s, err)
		}
	}
	if err := c.freezeCheck(m + 130*1000); !errors.Is(err, ErrSuspectedFreeze) {
		t.Errorf("只有原值重发 2 分 10 秒：%v，应 ErrSuspectedFreeze", err)
	}
}

// guard: L7 启动自检 —— 在交易时段连上：当前那根开盘早于 now − N ⇒ ErrStaleSnapshot；开盘前诞生的那根（开盘 ＝ now ＋ 150 ms）不误报；
// 不在交易时段连上 ⇒ 不查。
func TestLiveStartupCheck(t *testing.T) {
	now := at2(2026, 9, 4, 13, 45, 0, 0)
	c := newLiveCore(testCalendar(t), liveSym, 0, LiveOptions{})
	if _, err := c.feed(now, kFrame(100, now-3*60000, 3000, "")); !errors.Is(err, ErrStaleSnapshot) {
		t.Errorf("当前那根开盘 now − 3 分钟：%v，应 ErrStaleSnapshot", err)
	}
	open := at2(2026, 9, 4, 13, 30, 0, 0)
	// 本机慢一点：本机 13:30:59.850（在时段里）时 13:31 那根已经诞生 ⇒ 当前那根开盘 ＝ now ＋ 150 ms，不许误报（上端放宽 G 就是为它）。
	// ⚠️ 第一版造的是「本机 13:30:00.150、当前那根开盘 13:30」—— 开盘不晚于 now，放不放宽都一样，突变「上端不放宽」在它身上全绿。
	d := newLiveCore(testCalendar(t), liveSym, 0, LiveOptions{})
	if _, err := d.feed(open+59850, kFrame(100, open+60000, 3000, "")); err != nil {
		t.Errorf("本机 13:30:59.850、当前那根开盘 13:31（＝ now ＋ 150 ms）：%v，应不报", err)
	}
	e := newLiveCore(testCalendar(t), liveSym, 0, LiveOptions{})
	if _, err := e.feed(open-150, kFrame(100, open, 3000, "")); err != nil {
		t.Errorf("本机 13:29:59.850（午休中）连上：%v，应不查", err)
	}
	// 上面那格守不住「时段外不查」：当前那根离 now 只差 150 ms，就算照查也在窗里（v0.11 Q-b 的突变「启动自检一律当在时段内」下它不红）⇒
	// 补一格当前那根远早于 now − N 的：午休中连上、当前那根是上午最后一根 11:29 ⇒ 只要查了就是 ErrStaleSnapshot，不查才不报
	e2 := newLiveCore(testCalendar(t), liveSym, 0, LiveOptions{})
	if _, err := e2.feed(open-150, kFrame(100, at2(2026, 9, 4, 11, 29, 0, 0), 3000, "")); err != nil {
		t.Errorf("本机 13:29:59.850（午休中）连上、当前那根 11:29：%v，应不查（时段外不做启动自检）", err)
	}
	f := newLiveCore(testCalendar(t), liveSym, 0, LiveOptions{})
	if _, err := f.feed(now, kFrame(100, now+4001, 3000, "")); !errors.Is(err, ErrStaleSnapshot) {
		t.Errorf("对照：当前那根开盘 now ＋ 4.001 秒（超过 ＋G）：%v，应 ErrStaleSnapshot", err)
	}
}
