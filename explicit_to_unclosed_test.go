package tickflow

import (
	"context"
	"strings"
	"testing"

	"github.com/dream-until-dawn/futures-tickflow-go/source/pacing"
)

// 本文件是「显式 To 含未收盘交易日 ⇒ Sync 报错」那一格的边界（v0.6.0 勘误二）。
// 端到端的两种形状（1m 半天 / 日线当天）在 source/shinnysource 与 source/sinasource 各有一格。

// closeCal 在 week() 上给每个交易日一个不同的收盘时刻：2020-01-06 收在 1000，之后每天 +1000。
type closeCal struct{ *fakeCal }

func closeOf(d TradingDay) int64 { return int64(d-20200105) * 1000 }

func (c closeCal) Walk(k ProductKey, from, to TradingDay, fn func(Day) bool) error {
	return c.fakeCal.Walk(k, from, to, func(d Day) bool {
		end := closeOf(d.Num)
		return fn(Day{Num: d.Num, Sessions: []Session{{Start: end - 500, End: end}}})
	})
}

func TestFirstUnclosedDayBoundaries(t *testing.T) {
	cal := closeCal{week()}
	k := ProductKey{Exchange: "SHFE", Product: "rb"}
	cells := []struct {
		name     string
		from, to TradingDay
		now      int64
		wantDay  TradingDay // 0 ⇒ 不许找到
	}{
		{"now 恰为末日收盘 ⇒ 已收盘", 20200106, 20200110, closeOf(20200110), 0},
		{"now 比末日收盘早 1ms ⇒ 末日没收盘", 20200106, 20200110, closeOf(20200110) - 1, 20200110},
		{"中间一天就没收盘 ⇒ 报第一个没收盘的，不是最后一个", 20200106, 20200110, closeOf(20200107) - 1, 20200107},
		{"To 是周日、周五已收盘 ⇒ 不算", 20200106, 20200112, closeOf(20200110), 0},
		{"To 越过覆盖、覆盖内都收盘 ⇒ 这一格不报（覆盖之外由 Covers 那一格报）", 20200106, 20200120, closeOf(20200110), 0},
		{"整段在覆盖之外 ⇒ 不报", 20200201, 20200205, 0, 0},
	}
	for _, ce := range cells {
		day, _, found, err := firstUnclosedDay(cal, k, ce.from, ce.to, ce.now)
		if err != nil {
			t.Errorf("%s：err=%v", ce.name, err)
			continue
		}
		if found != (ce.wantDay != 0) || day != ce.wantDay {
			t.Errorf("%s：found=%v day=%s，应为 found=%v day=%s", ce.name, found, day, ce.wantDay != 0, ce.wantDay)
		}
	}
	notCovered := week()
	notCovered.ok = false
	if _, _, found, err := firstUnclosedDay(notCovered, k, 20200106, 20200110, 0); found || err != nil {
		t.Errorf("日历覆盖不到这个品种 ⇒ 不报（由 Covers 那一格报）：found=%v err=%v", found, err)
	}
}

// Sync 这一层：显式 To 含未收盘交易日 ⇒ 报错、Halt 未记录、一次都不向源要、库不动。
func TestSyncRejectsExplicitToWithUnclosedDay(t *testing.T) {
	// week() 每个交易日的时段都是 [1, 2) ⇒ now=1 时一天都没收盘，now=2 时全都收盘。
	cells := []struct {
		name    string
		now     int64
		wantErr bool
	}{
		{"now=1：区间里的交易日都还没收盘 ⇒ 拒", 1, true},
		{"now=2：恰好全都收盘 ⇒ 照常同步（标定格）", 2, false},
	}
	for _, ce := range cells {
		h := newHarness(t, pacing.NoPacing(), 1, 0)
		rep, err := h.syn.Sync(context.Background(), req(0), ce.now)
		if !ce.wantErr {
			if err != nil || rep.Halt != HaltDone || h.src.calls == 0 {
				t.Errorf("%s：err=%v Halt=%v 源被调 %d 次，应为照常跑完", ce.name, err, rep.Halt, h.src.calls)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), "还没收盘") || !strings.Contains(err.Error(), "To=0") {
			t.Fatalf("%s：应报「还没收盘」并指路 To=0，得到 %v", ce.name, err)
		}
		if rep.Halt != HaltUnknown || rep.Complete() {
			t.Errorf("%s：Halt=%v Complete=%v，早退应为 HaltUnknown 且不 Complete", ce.name, rep.Halt, rep.Complete())
		}
		if h.src.calls != 0 || h.store.appended != 0 || len(h.store.spans) != 0 {
			t.Errorf("%s：源被调 %d 次、AppendBars %d 次、CommitSpan %v —— 参数被拒时什么都不该做", ce.name, h.src.calls, h.store.appended, h.store.spans)
		}
	}
}
