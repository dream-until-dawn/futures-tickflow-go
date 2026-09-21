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

// guard: L12 三 —— 历史与推送同一个 id 值不同 ⇒ 按 L5 报（同一根两条通道给了两个值），报文带两边的值。
func TestLiveBackfillOverlapMismatch(t *testing.T) {
	c := bfCore(t)
	rows := fakeHist(101, 102, 103, 104)
	bad := hRow(105, 3999)
	err := c.backfill(append(rows, bad))
	if !errors.Is(err, ErrCorrectedAfterDelivery) || !strings.Contains(err.Error(), "C 3999") || !strings.Contains(err.Error(), "C 3005") {
		t.Errorf("历史的 105 收 3999、推送收 3005：%v，应 Is ErrCorrectedAfterDelivery、报文带两边的值", err)
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
