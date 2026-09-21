package shinnysource

import (
	"errors"
	"strings"
	"testing"
)

// v0.10 P-b2：L12 起步补齐（历史通道用假的 fetch）与跳号漏角。设计 design.md §十五「v0.10 起手」五 L12。
//
// 场景：起始格 9/4 9:31（id 101）；推送窗口最早只到 9:35（id 105）⇒ [9:31, 9:35) 要向历史通道补。

var bfM = at2(2026, 9, 4, 9, 30, 0, 0) // id 100 的开盘；id i 的开盘 ＝ bfM ＋ (i − 100) 分钟

// hRow 造一行历史通道的 Row，字段取法与 kFrame 相同（同一根两条通道给的值相等）。
func hRow(id int64, c float64) Row {
	return Row{ID: id, Datetime: (bfM + (id-100)*60000) * 1e6, Open: c, High: c + 1, Low: c - 1, Close: c, Volume: 1, CloseOI: 100}
}

// fakeHist 是假的历史通道：按给的 id 从真相表（id i 收 3000 ＋ (i − 100)）里取行。
// 调用方可以多给 [from, to) 之外的（真的 fetch 按 view_width 拉，给回来的范围不会恰好是 [from, to)）。
func fakeHist(ids ...int64) []Row {
	var out []Row
	for _, id := range ids {
		out = append(out, hRow(id, 3000+float64(id-100)))
	}
	return out
}

// bfCore：起始格 9:31；推送来了 105、106（9:35、9:36）。没补齐之前一根都不交 —— 106 出现时 105 已有 k＋1，
// 不等补齐的话它在这里就交出去了（交出的第一根不是起始格，而之后补回来的 101–104 再也交不出去）。
func bfCore(t *testing.T) *liveCore {
	t.Helper()
	c := newLiveCore(testCalendar(t), liveSym, bfM+60000, LiveOptions{})
	for _, id := range []int64{105, 106} {
		if out := mustFeed(t, c, bfM+(id-100)*60000+100, kFrame(id, bfM+(id-100)*60000, 3000+float64(id-100), "")); len(out) != 0 {
			t.Fatalf("没补齐：推送来 id %d 时交出 %d 根（首根开盘 %s），应为 0（等补齐）", id, len(out), fmtTs(out[0].Ts))
		}
	}
	return c
}

// guard: L12 —— 推送最早那根晚于起始格 ⇒ 不交（等补齐），要的区间是 [起始格, 推送最早那根)；
// 补上之后从起始格起紧接交出（历史的 101–104 ＋ 推送的 105），推送接着往下交；多给的更早 / 更新的根不收。
func TestLiveBackfillFillsThenPushContinues(t *testing.T) {
	c := bfCore(t)
	if out := mustTick(t, c, bfM+7*60000); len(out) != 0 {
		t.Fatalf("没补齐：交出 %d 根，应为 0（等补齐）", len(out))
	}
	from, to, need := c.needBackfill()
	if !need || from != bfM+60000 || to != bfM+5*60000 {
		t.Fatalf("needBackfill ＝ %s, %s, %v；应为 9:31, 9:35, true", fmtTs(from), fmtTs(to), need)
	}
	if err := c.backfill(fakeHist(100, 101, 102, 103, 104, 105, 107)); err != nil {
		t.Fatal(err)
	}
	if _, _, need := c.needBackfill(); need {
		t.Fatalf("补上之后 needBackfill 仍为 true")
	}
	out := mustTick(t, c, bfM+7*60000)
	if len(out) != 5 {
		t.Fatalf("补上之后交出 %d 根，应为 5（9:31–9:35；9:36 等 k＋1）", len(out))
	}
	for i, b := range out {
		if want := bfM + int64(i+1)*60000; b.Ts != want || b.Close != 3001+float64(i) {
			t.Fatalf("第 %d 根：开盘 %s 收 %g，应为 %s 收 %g", i, fmtTs(b.Ts), b.Close, fmtTs(want), 3001+float64(i))
		}
	}
	if c.firstID != 101 {
		t.Errorf("firstID ＝ %d，应为 101（起始格那根）", c.firstID)
	}
	if out := mustFeed(t, c, bfM+7*60000+100, kFrame(107, bfM+7*60000, 3007, "")); len(out) != 1 || out[0].Ts != bfM+6*60000 {
		t.Errorf("推送接着来 107：交出 %+v，应只交 9:36（id 106）", out)
	}
}

// guard: L12 —— 推送窗口自带起始格 ⇒ 不要补、直接交（与 P-b1 的行为相同）。
func TestLiveBackfillNotNeededWhenPushCoversStart(t *testing.T) {
	c := newLiveCore(testCalendar(t), liveSym, bfM+60000, LiveOptions{})
	for _, id := range []int64{100, 101, 102} {
		mustFeed(t, c, bfM+(id-100)*60000+100, kFrame(id, bfM+(id-100)*60000, 3000, ""))
	}
	if _, _, need := c.needBackfill(); need {
		t.Fatalf("推送里有起始格 9:31，needBackfill 却为 true")
	}
	if out := mustTick(t, c, bfM+2*60000+200); c.firstID != 101 || c.lastID != 101 {
		t.Errorf("交出 %d 根、firstID %d、lastID %d；应只交 id 101", len(out), c.firstID, c.lastID)
	}
}

// guard: L12 补不齐 —— 历史第一根不是起始格（短了一根 · 起始格给在午休里）⇒ ErrStartGap，不交任何根。
func TestLiveBackfillShortHistory(t *testing.T) {
	c := bfCore(t)
	err := c.backfill(fakeHist(102, 103, 104))
	if !errors.Is(err, ErrStartGap) || !strings.Contains(err.Error(), "09:32") || !strings.Contains(err.Error(), "09:31") {
		t.Errorf("历史从 9:32 起：%v，应 Is ErrStartGap、报文带 9:32 与起始格 9:31", err)
	}
	if out, _ := c.tick(bfM + 7*60000); len(out) != 0 {
		t.Errorf("补不齐之后仍交出 %d 根", len(out))
	}
	// 起始格给在午休（11:45）：推送从 13:30 起 ⇒ 历史通道 [11:45, 13:30) 里一根都没有 ⇒ 报，不永远等
	l := at2(2026, 9, 4, 13, 30, 0, 0)
	d := newLiveCore(testCalendar(t), liveSym, at2(2026, 9, 4, 11, 45, 0, 0), LiveOptions{})
	mustFeed(t, d, l+100, kFrame(300, l, 3000, ""))
	if err := d.backfill(nil); !errors.Is(err, ErrStartGap) || !strings.Contains(err.Error(), "一根都没有") {
		t.Errorf("起始格在午休：%v，应 Is ErrStartGap、报文说一根都没有", err)
	}
}

// guard: L12 接不上 —— 历史中间跳号 · 历史的最后一根与推送最早那根之间缺一根 ⇒ ErrIDGap，报文带两端的 id。
func TestLiveBackfillIDGap(t *testing.T) {
	for _, tc := range []struct {
		name string
		ids  []int64
		want []string
	}{
		{"历史中间缺 103", []int64{101, 102, 104}, []string{"102", "104"}},
		{"历史到 103、推送从 105 起", []int64{101, 102, 103}, []string{"103", "105"}},
	} {
		c := bfCore(t)
		err := c.backfill(fakeHist(tc.ids...))
		ok := errors.Is(err, ErrIDGap)
		for _, w := range tc.want {
			ok = ok && strings.Contains(err.Error(), w)
		}
		if !ok {
			t.Errorf("%s：%v，应 Is ErrIDGap、报文带 %v", tc.name, err, tc.want)
		}
		if out, _ := c.tick(bfM + 7*60000); len(out) != 0 {
			t.Errorf("%s：接不上之后仍交出 %d 根", tc.name, len(out))
		}
	}
}

// guard: L12 三 —— 两边都已完结的重叠根（105：推送里 106 已在）值不同 ⇒ ErrChannelsDisagree，报文带两边的值；
// 不借 ErrCorrectedAfterDelivery（此时一根都没交出，处置是重新起步，不是 L5 的 Close → Sync → 重建）。
func TestLiveBackfillOverlapMismatch(t *testing.T) {
	c := bfCore(t)
	rows := fakeHist(101, 102, 103, 104)
	bad := hRow(105, 3999)
	err := c.backfill(append(rows, bad))
	if !errors.Is(err, ErrChannelsDisagree) || errors.Is(err, ErrCorrectedAfterDelivery) || !strings.Contains(err.Error(), "C 3999") || !strings.Contains(err.Error(), "C 3005") {
		t.Errorf("历史的 105 收 3999、推送收 3005：%v，应 Is ErrChannelsDisagree、不 Is ErrCorrectedAfterDelivery、报文带两边的值", err)
	}
}

// guard: L12 三的射程 —— 推送里还在变的当前根（106，107 还没来）不比：真的 fetch 拉到「此刻」，给回来的最后一根就是它，
// 值取自较早时刻（收 3005.5，推送已到 3006）⇒ 不报、以推送为准，补齐照常（评审方 09-21 实测：比它就误停）。
func TestLiveBackfillSkipsLiveCurrentBar(t *testing.T) {
	c := bfCore(t)
	err := c.backfill(append(fakeHist(101, 102, 103, 104, 105), hRow(106, 3005.5)))
	if err != nil {
		t.Fatalf("历史给了当前根 106 的较早值：%v，应不报", err)
	}
	out := mustTick(t, c, bfM+7*60000)
	if len(out) != 5 || c.rows[106].Close != 3006 {
		t.Errorf("交出 %d 根、106 收 %g；应交 9:31–9:35 五根、106 仍以推送为准（3006）", len(out), c.rows[106].Close)
	}
}

// guard: 跳号漏角（评审方 09-21）—— 时段末根按 C＋G 交出之后，下一段来的 id 跳过了一根：
// 「k＋1 没来而更大的 id 来了」那条只查【还没交出的当前根】，照不到这里（10:14 那根已交出，202 自己的 k＋1 ＝ 203 在）⇒
// 要靠「交出过之后，下一根必须是 lastID＋1」。
func TestLiveIDGapAfterSegmentEnd(t *testing.T) {
	c := newLiveCore(testCalendar(t), liveSym, 0, LiveOptions{})
	m := at2(2026, 9, 4, 10, 14, 0, 0)
	mustFeed(t, c, m+100, kFrame(200, m, 3000, ""))
	if out := mustTick(t, c, at2(2026, 9, 4, 10, 15, 4, 0)); len(out) != 1 {
		t.Fatalf("前提没成立：10:14 那根没按 C＋G 交出")
	}
	s := at2(2026, 9, 4, 10, 30, 0, 0)
	_, err := c.feed(s+100, kFrame(202, s, 3001, "")) // 缺 201；202 一出现就报，不等它自己的 k＋1
	if err == nil {
		_, err = c.feed(s+60100, kFrame(203, s+60000, 3002, ""))
	}
	if !errors.Is(err, ErrIDGap) || !strings.Contains(err.Error(), "缺 id 201") {
		t.Errorf("200 交出后下一段从 202 起：%v，应 Is ErrIDGap、报文带缺 id 201", err)
	}
}

// —— L8 重连（liveCore 那一半；联网那一半在 live_net_test.go）——

// rcCore：起始格 9:30；推送来了 100–102，交出 100、101；然后重连（本机时刻 at）。
func rcCore(t *testing.T, at int64) *liveCore {
	t.Helper()
	c := newLiveCore(testCalendar(t), liveSym, bfM, LiveOptions{})
	for _, id := range []int64{100, 101, 102} {
		mustFeed(t, c, bfM+(id-100)*60000+100, kFrame(id, bfM+(id-100)*60000, 3000+float64(id-100), ""))
	}
	if c.lastID != 101 {
		t.Fatalf("前提没成立：交出到 id %d，应到 101", c.lastID)
	}
	c.reconnected(at)
	return c
}

// guard: L8 —— 重连后新推送从 106 起 ⇒ 等补齐（不交、不报跳号），要 [101 的开盘 ＋ 1 分钟, 106 的开盘)；
// 历史补回来的第一根不是 102 ⇒ ErrIDGap（报文带应接上的 102）；补对了 ⇒ 102 … 106 紧接交出。
func TestLiveReconnectResyncs(t *testing.T) {
	at := bfM + 7*60000
	c := rcCore(t, at)
	// 状态断言：「重连后没进 resync」与「进了 resync 却不等补齐」从外面看是同一张脸（都在下一帧报跳号，突变实测）⇒ 这里先分开
	if !c.resync {
		t.Fatalf("交出过之后重连：resync 应为真（接缝换成 lastID＋1）")
	}
	if out := mustFeed(t, c, at+100, kFrame(106, bfM+6*60000, 3006, "")); len(out) != 0 {
		t.Fatalf("重连后没补齐：交出 %d 根，应为 0", len(out))
	}
	mustFeed(t, c, at+200, kFrame(107, bfM+7*60000, 3007, ""))
	from, to, need := c.needBackfill()
	if !need || from != bfM+2*60000 || to != bfM+6*60000 {
		t.Fatalf("needBackfill ＝ %s, %s, %v；应为 9:32, 9:36, true", fmtTs(from), fmtTs(to), need)
	}
	if err := c.backfill(fakeHist(103, 104, 105)); !errors.Is(err, ErrIDGap) || !strings.Contains(err.Error(), "id 102") {
		t.Errorf("历史从 103 起：%v，应 Is ErrIDGap、报文带应接上的 id 102", err)
	}
	if err := c.backfill(fakeHist(102, 103, 104, 105)); err != nil {
		t.Fatal(err)
	}
	out := mustTick(t, c, at+300)
	if len(out) != 5 || out[0].Ts != bfM+2*60000 || out[4].Ts != bfM+6*60000 {
		t.Errorf("补上之后交出 %d 根（%+v），应为 9:32 … 9:36 五根", len(out), out)
	}
}

// guard: L8 —— 新连接上再做一次启动自检，而且按「这条连接上第一帧带根的」触发：
// 只带报价的帧不触发（重连后 rows 里还有旧根，按「rows 非空」触发会拿旧根判出快照过旧）；
// 新连接给的当前那根过旧 ⇒ ErrStaleSnapshot。冻结计时从重连这一刻起算（断开期间不算没有改动）。
func TestLiveReconnectRechecks(t *testing.T) {
	at := bfM + 10*60000 // 9:40 重连：离 101 交出已 8 分钟 > N
	c := rcCore(t, at)
	if _, err := c.feed(at+100, qFrame(at)); err != nil {
		t.Fatalf("重连后第一帧只带报价：%v，应不报（不拿旧根做启动自检）", err)
	}
	if _, err := c.tick(at + 60000); err != nil {
		t.Fatalf("重连后 1 分钟：%v，应不报冻结（计时从重连起算）", err)
	}
	if _, err := c.feed(at+60100, kFrame(102, bfM+2*60000, 3002, "")); !errors.Is(err, ErrStaleSnapshot) {
		t.Errorf("新连接的当前那根是 9:32（本机 9:41）：%v，应 ErrStaleSnapshot", err)
	}
}
