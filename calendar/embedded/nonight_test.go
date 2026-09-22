package embedded

import (
	"errors"
	"strings"
	"testing"
	"time"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/derived"
)

// v0.11 Q-a：停夜盘名单 NoNightAfter（design.md「v0.11 起手」甲）。键是交易所《休市安排》里「X 日晚上不进行夜盘交易」的 X。
// 日子取 2026 年那份（probe.md 6.23 之二）：9/30（周三）晚上不开夜盘，10/1–10/7 休市，10/8（周四）开市。

var (
	holDays = []tickflow.TradingDay{20260928, 20260929, 20260930, 20261008, 20261009}
	au      = tickflow.ProductKey{Exchange: tickflow.SHFE, Product: "au"}
)

func mustNew(t *testing.T, opts ...Option) *Calendar {
	t.Helper()
	c, err := New(holDays, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func firstSession(t *testing.T, c *Calendar, k tickflow.ProductKey, d tickflow.TradingDay) string {
	t.Helper()
	day, err := c.DayOf(k, d)
	if err != nil {
		t.Fatalf("DayOf(%s)：%v", d, err)
	}
	return hhmm(day.Sessions[0].Start)
}

// guard: 判据一 —— NoNightAfter(20260930) 去掉的是 10/8 的夜盘（9/30 晚上那段），**而 9/30 自己的夜盘（9/29 晚上）照旧**。
// 后半句就是「键被实现成夜盘所属的交易日」时会红的地方（评审方 M2：照公告抄 20260930，删的会是 9/29 晚上那段真实存在的夜盘）。
// 对照：不注入 ⇒ 10/8 仍挂着 9/30 21:00 那段（v0.10 的形状，已知缺陷测试同一个）。
func TestNoNightAfterKeyIsTheAnnouncedEvening(t *testing.T) {
	c := mustNew(t, NoNightAfter(20260930))
	if got := firstSession(t, c, rb, 20261008); got != "10-08 09:00" {
		t.Errorf("注入之后 10/8 的第一段是 %s，应为 10-08 09:00（9/30 晚上那段没有了）", got)
	}
	if got := firstSession(t, c, rb, 20260930); got != "09-29 21:00" {
		t.Errorf("注入之后 9/30 的第一段是 %s，应仍为 09-29 21:00（它自己的夜盘不受影响）", got)
	}
	if got := firstSession(t, c, rb, 20261009); got != "10-08 21:00" {
		t.Errorf("注入之后 10/9 的第一段是 %s，应仍为 10-08 21:00", got)
	}
	plain := mustNew(t)
	if got := firstSession(t, plain, rb, 20261008); got != "09-30 21:00" {
		t.Errorf("对照：不注入 ⇒ 10/8 的第一段是 %s，应为 09-30 21:00（与 v0.10 相同）", got)
	}
}

// guard: 判据二（上）—— Day 的派生都按「那一夜不存在」：DayAt 在 9/30 21:30 报 ErrClosed（不注入时归 10/8）·
// Walk 交出的 10/8 没有夜盘段 · 分钟数少了夜盘那 120 分钟（rb）。
func TestNoNightAfterDerivedViews(t *testing.T) {
	c := mustNew(t, NoNightAfter(20260930))
	plain := mustNew(t)
	ts := time.Date(2026, 9, 30, 21, 30, 0, 0, tickflow.CST).UnixMilli()
	if _, err := c.DayAt(rb, ts); !errors.Is(err, tickflow.ErrClosed) {
		t.Errorf("注入之后 DayAt(9/30 21:30)：%v，应 ErrClosed", err)
	}
	if d, err := plain.DayAt(rb, ts); err != nil || d.Num != 20261008 {
		t.Errorf("对照：不注入 ⇒ DayAt(9/30 21:30) ＝ %s · %v，应归 10/8", d.Num, err)
	}
	var walked tickflow.Day
	if err := c.Walk(rb, 20261008, 20261008, func(d tickflow.Day) bool { walked = d; return true }); err != nil {
		t.Fatal(err)
	}
	if got := hhmm(walked.Sessions[0].Start); got != "10-08 09:00" {
		t.Errorf("Walk 交出的 10/8 第一段 %s，应为 10-08 09:00", got)
	}
	d8, _ := c.DayOf(rb, 20261008)
	p8, _ := plain.DayOf(rb, 20261008)
	if d8.Minutes() != 225 || p8.Minutes() != 345 {
		t.Errorf("10/8 分钟数：注入 %d · 不注入 %d，应为 225 · 345（rb 日盘 225，夜盘 120）", d8.Minutes(), p8.Minutes())
	}
}

// guard: 判据二（下）—— 相位按标称模板算、不受停夜盘影响。用 au（夜盘 330 分钟，60m 余 30 分钟；rb 夜盘 120 分钟、余数 0，分不出对错）：
// 被删的那段夜盘里没有格子 · 日盘格子与普通日（9/30，夜盘在）逐个相同（按离当天零点的偏移比 Open / Close / Full）。
func TestNoNightAfterPhaseUnchanged(t *testing.T) {
	c := mustNew(t, NoNightAfter(20260930))
	p60 := tickflow.MustIntraday(60)
	cells := func(d tickflow.TradingDay) []tickflow.BarBound {
		t.Helper()
		tmpl, err := c.Template(au, d)
		if err != nil {
			t.Fatal(err)
		}
		day, err := c.DayOf(au, d)
		if err != nil {
			t.Fatal(err)
		}
		return p60.Bars(tmpl, day)
	}
	gone := [2]int64{time.Date(2026, 9, 30, 21, 0, 0, 0, tickflow.CST).UnixMilli(), time.Date(2026, 10, 1, 2, 30, 0, 0, tickflow.CST).UnixMilli()}
	hol, normal := cells(20261008), cells(20260930)
	for _, b := range hol {
		if b.Open < gone[1] && b.Close > gone[0] {
			t.Errorf("10/8 有一个格子 [%s, %s) 落在被删的那段夜盘里", hhmm(b.Open), hhmm(b.Close))
		}
	}
	type rel struct {
		open, close int64
		full        bool
	}
	// 日盘格子 ＝ 收盘落在当天 04:00 之后的格子（夜盘最晚收 02:30）。
	// ⚠️ 第一版按「开盘在当天零点之后」划，把 au 夜盘过零点的两格算进来；第二版按「开盘在 04:00 之后」划，又漏掉了普通日那个
	// 跨休市的格子（02:00–02:30 ＋ 09:00–09:30，开盘在 02:00）—— 两次都是测试构造错，读格子清单才看出来（下面的 Logf）。
	dayCells := func(bs []tickflow.BarBound, d tickflow.TradingDay) []rel {
		m := midnight(d)
		var out []rel
		for _, b := range bs {
			if b.Close > m+4*3600*1000 {
				out = append(out, rel{b.Open - m, b.Close - m, b.Full})
			}
		}
		return out
	}
	h, n := dayCells(hol, 20261008), dayCells(normal, 20260930)
	for _, x := range [][]tickflow.BarBound{normal, hol} {
		var show []string
		for _, b := range x {
			show = append(show, hhmm(b.Open)+"–"+hhmm(b.Close))
		}
		t.Logf("au 60m：%v", show)
	}
	if len(h) == 0 || len(h) != len(n) {
		t.Fatalf("日盘格子数：停夜盘日 %d · 普通日 %d，应相同且非零", len(h), len(n))
	}
	// 收盘时刻与 Full 逐个相同（相位按标称）；开盘时刻从第二格起相同 —— 第一格在普通日跨过休市、从前一晚 02:00 开始，
	// 在停夜盘日从 09:00 开始（那 30 分钟来自一个没有开的夜盘，BarBound.Full 的注释写的就是这一格）
	for i := range h {
		if h[i].close != n[i].close || h[i].full != n[i].full || (i > 0 && h[i].open != n[i].open) {
			t.Errorf("第 %d 个日盘格子：停夜盘日 %+v · 普通日 %+v，收盘 / Full（及第二格起的开盘）应相同", i, h[i], n[i])
		}
	}
	at := func(off int64) string {
		return time.UnixMilli(midnight(20261008) + off).In(tickflow.CST).Format("15:04")
	}
	if at(h[0].open) != "09:00" || at(h[0].close) != "09:30" {
		t.Errorf("停夜盘日第一个日盘格子 [%s, %s)，应为 [09:00, 09:30)（probe.md 坑三之三：余数 30 分钟照旧、收盘标签 09:30）", at(h[0].open), at(h[0].close))
	}
}

// guard: 名单里的日子必须是注入的交易日（否则 New 报错，不静默忽略）；是注入的最后一天 ⇒ 不报错，
// 而它影响的那一天不在覆盖里 ⇒ 任何查询都答 ErrUncovered（空操作不会给出错的答案 —— 评审方要的「处置与理由」，钉成一格）。
func TestNoNightAfterValidation(t *testing.T) {
	if _, err := New(holDays, NoNightAfter(20261001)); err == nil || !strings.Contains(err.Error(), "不在注入的交易日里") {
		t.Errorf("NoNightAfter(10/1，休市日)：%v，应报「不在注入的交易日里」", err)
	}
	c, err := New([]tickflow.TradingDay{20260929, 20260930}, NoNightAfter(20260930))
	if err != nil {
		t.Fatalf("NoNightAfter(注入的最后一天)：%v，应不报错", err)
	}
	if got := firstSession(t, c, rb, 20260930); got != "09-29 21:00" {
		t.Errorf("最后一天自己的夜盘：%s，应为 09-29 21:00（不受影响）", got)
	}
	if _, err := c.DayOf(rb, 20261008); !errors.Is(err, tickflow.ErrUncovered) {
		t.Errorf("被影响的那一天（10/8）不在覆盖里：DayOf %v，应 ErrUncovered", err)
	}
	if _, err := c.DayAt(rb, time.Date(2026, 9, 30, 21, 30, 0, 0, tickflow.CST).UnixMilli()); !errors.Is(err, tickflow.ErrUncovered) {
		t.Errorf("被影响的那一晚（9/30 21:30）：DayAt %v，应 ErrUncovered", err)
	}
}

// guard: 判据五 —— derived 的差异清单：base 用不注入的日历 ⇒ 节后首日报一条「base 有夜盘 · 观测没有夜盘根」；
// 用注入了 NoNightAfter 的日历摊平 ⇒ 这条差异消失（它正是 derived 当初报出来的那一类）。
// base 的摊平与 tools/derivedreport 的 flattenBase 同一把尺子：首段起点落在 20:00 之后或 04:00 之前 ⇒ Night。
func TestNoNightAfterClearsDerivedDiff(t *testing.T) {
	flatten := func(c *Calendar) []derived.BaseDay {
		var out []derived.BaseDay
		for _, d := range holDays[1:] {
			day, err := c.DayOf(rb, d)
			if err != nil {
				t.Fatal(err)
			}
			h := time.UnixMilli(day.Sessions[0].Start).In(tickflow.CST).Hour()
			out = append(out, derived.BaseDay{Day: d, Night: h >= 20 || h < 4})
		}
		return out
	}
	rep := derived.Report{From: holDays[1], To: holDays[len(holDays)-1]}
	for _, d := range holDays[1:] {
		v := derived.NightTraded
		if d == 20261008 {
			v = derived.NightAbsent // 观测：节后首日没有夜盘根（6.21 / 6.25 那种形状）
		}
		rep.Days = append(rep.Days, derived.DayVerdict{Day: d, Verdict: v})
	}
	count := func(c *Calendar) int {
		diffs, err := derived.Compare(rep, flatten(c))
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, df := range diffs {
			if df.Day == 20261008 {
				n++
			}
		}
		return n
	}
	if n := count(mustNew(t)); n != 1 {
		t.Fatalf("前提：不注入 ⇒ 10/8 的差异 %d 条，应为 1", n)
	}
	if n := count(mustNew(t, NoNightAfter(20260930))); n != 0 {
		t.Errorf("注入 NoNightAfter(9/30) ⇒ 10/8 的差异 %d 条，应为 0", n)
	}
}
