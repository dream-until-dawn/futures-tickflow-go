package tickflow

import (
	"strings"
	"testing"
	"time"
)

// 基线全部来自 docs/probe.md 的实测标签（新浪，收盘时刻标注）。
// 本库内部按开盘时刻记，所以比对的是 BarBound.Close。

func ts(s string) int64 {
	t, err := time.ParseInLocation("2006-01-02 15:04", s, CST)
	if err != nil {
		panic(err)
	}
	return t.UnixMilli()
}

func sess(a, b string) Session { return Session{Start: ts(a), End: ts(b)} }

// 三个品种的标称模板。日盘完全相同，只有夜盘长度不同——
// 这正是「同日盘、不同网格」那条结论的实验设计。
func tmplWithNight(nightMin int) SessionTemplate {
	t := SessionTemplate{
		Day: []Session{
			{Start: 9 * 3600000, End: 10*3600000 + 15*60000},
			{Start: 10*3600000 + 30*60000, End: 11*3600000 + 30*60000},
			{Start: 13*3600000 + 30*60000, End: 15 * 3600000},
		},
	}
	if nightMin > 0 {
		t.Night = []Session{{Start: 21 * 3600000, End: 21*3600000 + int64(nightMin)*60000}}
	}
	return t
}

var (
	tmplRB = tmplWithNight(120) // 21:00–23:00
	tmplCU = tmplWithNight(240) // 21:00–01:00
	tmplAG = tmplWithNight(330) // 21:00–02:30
	tmplSI = tmplWithNight(0)   // 广期所：无夜盘（实测，见 probe.md 6.x）
)

// day 造一个交易日：夜盘（前一自然日 21:00 起，nightMin 分钟）+ 标准日盘。
// nightMin 为 0 表示【那天没有夜盘】（长假前停夜盘，或本就无夜盘的品种）。
func day(num TradingDay, prevDate, date string, nightMin int) Day {
	var ss []Session
	if nightMin > 0 {
		start := ts(prevDate + " 21:00")
		ss = append(ss, Session{Start: start, End: start + int64(nightMin)*60000})
	}
	ss = append(ss,
		sess(date+" 09:00", date+" 10:15"),
		sess(date+" 10:30", date+" 11:30"),
		sess(date+" 13:30", date+" 15:00"),
	)
	return Day{Num: num, Sessions: ss}
}

func closes(bs []BarBound) []string {
	out := make([]string, len(bs))
	for i, b := range bs {
		out[i] = time.UnixMilli(b.Close).In(CST).Format("15:04")
	}
	return out
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// dayPart 只取日盘那一段的标签（09:00–15:00）。
func dayPart(bs []BarBound) []string {
	var out []string
	for _, b := range bs {
		h := time.UnixMilli(b.Close).In(CST).Hour()
		m := time.UnixMilli(b.Close).In(CST).Minute()
		if h > 9 || (h == 9 && m > 0) {
			if h < 15 || (h == 15 && m == 0) {
				out = append(out, time.UnixMilli(b.Close).In(CST).Format("15:04"))
			}
		}
	}
	return out
}

// TestBarsMatchMeasured 与 docs/probe.md 记录的新浪实测标签逐根比对。
func TestBarsMatchMeasured(t *testing.T) {
	rb := day(20260904, "2026-09-03", "2026-09-04", 120)
	ag := day(20260904, "2026-09-03", "2026-09-04", 330)
	cu := day(20260904, "2026-09-03", "2026-09-04", 240)

	cases := []struct {
		name string
		p    IntradayPeriod
		tmpl SessionTemplate
		d    Day
		want []string
	}{
		// 螺纹 30m —— 实测：09:30 10:00 10:45 11:15 13:45 14:15 14:45 15:00
		// 其中 10:45 那根跨了上午休市段，13:45 那根横跨午休与上下午。
		{"rb 30m", MustIntraday(30), tmplRB, rb, []string{
			"21:30", "22:00", "22:30", "23:00",
			"09:30", "10:00", "10:45", "11:15", "13:45", "14:15", "14:45", "15:00"}},

		// 螺纹 60m —— 实测日盘：10:00 11:15 14:15 15:00
		{"rb 60m", MustIntraday(60), tmplRB, rb, []string{
			"22:00", "23:00",
			"10:00", "11:15", "14:15", "15:00"}},

		// 螺纹 15m —— 三个日盘时段都是 15 的整数倍，两种规则在这里【结果相同】，
		// 所以这条测不出对齐规则，只作数值回归。
		{"rb 15m", MustIntraday(15), tmplRB, rb, []string{
			"21:15", "21:30", "21:45", "22:00", "22:15", "22:30", "22:45", "23:00",
			"09:15", "09:30", "09:45", "10:00", "10:15",
			"10:45", "11:00", "11:15", "11:30",
			"13:45", "14:00", "14:15", "14:30", "14:45", "15:00"}},

		// 沪银 60m —— 实测：夜盘 5 根 + 30 分钟余数跨隔夜缺口，
		// 与次日 09:00–09:30 拼成一根完整的 60m，标注 09:30。
		{"ag 60m", MustIntraday(60), tmplAG, ag, []string{
			"22:00", "23:00", "00:00", "01:00", "02:00",
			"09:30", "10:45", "13:45", "14:45", "15:00"}},

		// 沪铜 60m —— 夜盘 240 分整除，日盘从 09:00 干净重开。
		{"cu 60m", MustIntraday(60), tmplCU, cu, []string{
			"22:00", "23:00", "00:00", "01:00",
			"10:00", "11:15", "14:15", "15:00"}},

		// 沪银 30m —— 330/30 = 11 整除，网格与螺纹一致。
		{"ag 30m", MustIntraday(30), tmplAG, ag, []string{
			"21:30", "22:00", "22:30", "23:00", "23:30",
			"00:00", "00:30", "01:00", "01:30", "02:00", "02:30",
			"09:30", "10:00", "10:45", "11:15", "13:45", "14:15", "14:45", "15:00"}},
	}
	for _, c := range cases {
		got := closes(c.p.Bars(c.tmpl, c.d))
		if !eq(got, c.want) {
			t.Errorf("%s\n  got  %v\n  want %v", c.name, got, c.want)
		}
	}
}

// TestGridDiffersByNightLength 是【反向测试】：
// 沪银与沪铜的日盘时段完全相同，60m 网格【必须不同】。
//
// 两者相同就说明实现退化成了「日盘重新对齐到整点」——
// 那是个在沪银上每一根都错 30 分钟、而序列看起来完全正常的实现。
func TestGridDiffersByNightLength(t *testing.T) {
	d := day(20260904, "2026-09-03", "2026-09-04", 0) // 时段本身不重要，只看日盘
	agDay := day(20260904, "2026-09-03", "2026-09-04", 330)
	cuDay := day(20260904, "2026-09-03", "2026-09-04", 240)
	_ = d

	p := MustIntraday(60)
	ag := dayPart(p.Bars(tmplAG, agDay))
	cu := dayPart(p.Bars(tmplCU, cuDay))

	if eq(ag, cu) {
		t.Fatalf("沪银与沪铜的 60m 日盘网格【必须不同】，实际都是 %v\n"+
			"  ——相同说明相位没生效，实现退化成了时钟网格", ag)
	}
	if want := []string{"09:30", "10:45", "13:45", "14:45", "15:00"}; !eq(ag, want) {
		t.Errorf("ag 日盘 got %v want %v", ag, want)
	}
	if want := []string{"10:00", "11:15", "14:15", "15:00"}; !eq(cu, want) {
		t.Errorf("cu 日盘 got %v want %v", cu, want)
	}
}

// TestPhaseIsProductConstant 是本包最要紧的一条反向测试：
// 停夜盘那天，日盘网格【必须与普通日相同】。
//
// 「沿当天实际时段累计」的实现会在这里给出沪铜那一族的网格——
// 它在普通日上全绿，只在长假前后错，且只错夜盘不整除的品种。
// 一年发作十几次，不报错、不崩溃。
func TestPhaseIsProductConstant(t *testing.T) {
	p := MustIntraday(60)
	normal := dayPart(p.Bars(tmplAG, day(20260904, "2026-09-03", "2026-09-04", 330)))
	// 2026-05-06：五一后第一个交易日，前夜停夜盘（实测，见 probe.md 坑三之三）
	susp := dayPart(p.Bars(tmplAG, day(20260506, "2026-05-05", "2026-05-06", 0)))

	if !eq(normal, susp) {
		t.Fatalf("停夜盘日的日盘网格必须与普通日相同\n  普通日   %v\n  停夜盘日 %v\n"+
			"  ——不同说明相位是按【当天实际时段】算的，而实测证明它是【品种常量】",
			normal, susp)
	}
	if want := []string{"09:30", "10:45", "13:45", "14:45", "15:00"}; !eq(susp, want) {
		t.Errorf("停夜盘日 got %v want %v", susp, want)
	}
}

// TestGfexNoNight 广期所无夜盘（实测），相位为 0，落在「日盘 + 无夜盘」那一族。
func TestGfexNoNight(t *testing.T) {
	p := MustIntraday(60)
	if got := p.Phase(tmplSI); got != 0 {
		t.Errorf("无夜盘品种的相位应为 0，得到 %v", got)
	}
	got := dayPart(p.Bars(tmplSI, day(20260907, "2026-09-04", "2026-09-07", 0)))
	want := []string{"10:00", "11:15", "14:15", "15:00"}
	if !eq(got, want) {
		t.Errorf("si 60m 日盘 got %v want %v", got, want)
	}
}

// TestLastBarOfDayIsShort 交易日末尾那根可以是短的，且必须标 Full=false。
func TestLastBarOfDayIsShort(t *testing.T) {
	p := MustIntraday(60)
	bs := p.Bars(tmplCU, day(20260904, "2026-09-03", "2026-09-04", 240))
	last := bs[len(bs)-1]
	if last.Full {
		t.Error("交易日最后一根应当 Full=false")
	}
	d := day(20260904, "2026-09-03", "2026-09-04", 240)
	if m := last.Minutes(d); m != 45 {
		t.Errorf("cu 60m 末根应装 45 分钟（14:15–15:00），实际 %d", m)
	}
	for _, b := range bs[:len(bs)-1] {
		if !b.Full {
			t.Errorf("非末根应当 Full=true: %v", time.UnixMilli(b.Close).In(CST))
		}
	}
}

// TestBarSpansSessionBreak 一根 K 线可以跨休市段，此时墙钟跨度大于周期长度。
func TestBarSpansSessionBreak(t *testing.T) {
	d := day(20260904, "2026-09-03", "2026-09-04", 120)
	bs := MustIntraday(30).Bars(tmplRB, d)
	var found *BarBound
	for i := range bs {
		if time.UnixMilli(bs[i].Close).In(CST).Format("15:04") == "13:45" {
			found = &bs[i]
		}
	}
	if found == nil {
		t.Fatal("没找到 13:45 那根")
	}
	// 它装的是 11:15–11:30 + 13:30–13:45，横跨午休且跨上下午
	if m := found.Minutes(d); m != 30 {
		t.Errorf("13:45 那根应装 30 分钟交易时间，实际 %d", m)
	}
	if wall := found.Close - found.Open; wall != int64(2*60+30)*60000 {
		t.Errorf("它的墙钟跨度应是 2 小时 30 分（11:15→13:45），实际 %v",
			time.Duration(wall)*time.Millisecond)
	}
}

// TestGroupCalendarPeriods 日 / 周 / 月成组。
func TestGroupCalendarPeriods(t *testing.T) {
	days := []Day{
		day(20260903, "2026-09-02", "2026-09-03", 120), // 周四
		day(20260904, "2026-09-03", "2026-09-04", 120), // 周五
		day(20260907, "2026-09-04", "2026-09-07", 120), // 周一（下一周）
	}
	if got := len(Daily.Group(days)); got != 3 {
		t.Errorf("日线应当一天一组，得到 %d 组", got)
	}
	w := Weekly.Group(days)
	if len(w) != 2 {
		t.Fatalf("周线应当分成 2 组（周四五一组、下周一一组），得到 %d 组", len(w))
	}
	// 第一组：从周四夜盘（09-02 21:00）到周五日盘收盘（09-04 15:00）
	if s := time.UnixMilli(w[0].Open).In(CST).Format("01-02 15:04"); s != "09-02 21:00" {
		t.Errorf("周线首组开盘 %s，期望 09-02 21:00（周四交易日的夜盘起点）", s)
	}
	if s := time.UnixMilli(w[0].Close).In(CST).Format("01-02 15:04"); s != "09-04 15:00" {
		t.Errorf("周线首组收盘 %s，期望 09-04 15:00", s)
	}
	if got := len(Monthly.Group(days)); got != 1 {
		t.Errorf("三天同月，月线应当 1 组，得到 %d", got)
	}
}

// TestGroupSpansThreeNaturalDays 一个交易日横跨三个自然日——
// 周五夜盘 → 周六凌晨 → 周一日盘，中间没有结算。
func TestGroupSpansThreeNaturalDays(t *testing.T) {
	// 交易日 20260907 = 周五 09-04 21:00 起的夜盘（沪银 330 分，跨到周六 02:30）
	// + 周一 09-07 的日盘
	d := day(20260907, "2026-09-04", "2026-09-07", 330)
	g := Daily.Group([]Day{d})
	if len(g) != 1 {
		t.Fatalf("期望 1 组，得到 %d", len(g))
	}
	open := time.UnixMilli(g[0].Open).In(CST)
	close := time.UnixMilli(g[0].Close).In(CST)
	if open.Format("01-02 15:04") != "09-04 21:00" {
		t.Errorf("开盘落在 %s，期望 09-04 21:00（周五夜盘）", open.Format("01-02 15:04"))
	}
	if close.Format("01-02 15:04") != "09-07 15:00" {
		t.Errorf("收盘落在 %s，期望 09-07 15:00（周一日盘）", close.Format("01-02 15:04"))
	}
	// 三个自然日：09-04（周五夜）、09-05（周六凌晨）、09-07（周一日盘）
	nights, _ := splitNightDay(d.Sessions)
	if len(nights) != 1 {
		t.Fatalf("期望一段夜盘，得到 %d", len(nights))
	}
	if e := time.UnixMilli(nights[0].End).In(CST); e.Format("01-02 15:04") != "09-05 02:30" {
		t.Errorf("夜盘结束落在 %s，期望 09-05 02:30（周六凌晨）", e.Format("01-02 15:04"))
	}
}

func TestIntradayRejectsNonPositive(t *testing.T) {
	for _, n := range []int{0, -1, -60} {
		if _, err := Intraday(n); err == nil {
			t.Errorf("Intraday(%d) 应当报错", n)
		} else if !strings.Contains(err.Error(), "必须为正") {
			t.Errorf("错误信息应说明原因，得到 %v", err)
		}
	}
}

// TestBarsCoverEveryTradingMinute 是一条【不变量】，不是回归值。
//
// 一个交易日里的每一分钟交易时间，必须【恰好】落在一根 K 线的 [Open, Close) 里：
// 不多不少，一分钟都不能漏、也不能被两根同时认领。
//
// 这条是补上来的：原实现让沪银 60m 的 02:00–02:30 掉在了所有 K 线之外。
// 收盘标签序列（TestBarsMatchMeasured 比的那个）一根不差、总根数一根不差，
// 唯独那半小时不属于任何一根——聚合时会静默少 30 分钟成交。
// **只比标签的测试看不见这个**，所以要单独按分钟数点一遍。
func TestBarsCoverEveryTradingMinute(t *testing.T) {
	cases := []struct {
		name string
		tmpl SessionTemplate
		d    Day
	}{
		{"ag", tmplAG, day(20260904, "2026-09-03", "2026-09-04", 330)},
		{"cu", tmplCU, day(20260904, "2026-09-03", "2026-09-04", 240)},
		{"rb", tmplRB, day(20260904, "2026-09-03", "2026-09-04", 120)},
		// 停夜盘日：模板有夜盘，当天实际没有
		{"ag 停夜盘", tmplAG, day(20260904, "2026-09-03", "2026-09-04", 0)},
		{"rb 停夜盘", tmplRB, day(20260904, "2026-09-03", "2026-09-04", 0)},
	}
	for _, mins := range []int{1, 5, 15, 30, 60} {
		p := MustIntraday(mins)
		for _, c := range cases {
			owners := map[int64]int{}
			total := 0
			for _, s := range c.d.Sessions {
				for ts := s.Start; ts < s.End; ts += 60000 {
					owners[ts] = 0
					total++
				}
			}
			for _, b := range p.Bars(c.tmpl, c.d) {
				for ts := range owners {
					if ts >= b.Open && ts < b.Close {
						owners[ts]++
					}
				}
			}
			var orphan, dup int64 = -1, -1
			no, nd := 0, 0
			for ts, n := range owners {
				switch {
				case n == 0:
					no++
					if orphan < 0 || ts < orphan {
						orphan = ts
					}
				case n > 1:
					nd++
					if dup < 0 || ts < dup {
						dup = ts
					}
				}
			}
			if no > 0 {
				t.Errorf("%s %s：%d/%d 分钟不属于任何一根 K 线，最早一分钟是 %s",
					c.name, p, no, total,
					time.UnixMilli(orphan).In(CST).Format("01-02 15:04"))
			}
			if nd > 0 {
				t.Errorf("%s %s：%d/%d 分钟被多根 K 线同时认领，最早一分钟是 %s",
					c.name, p, nd, total,
					time.UnixMilli(dup).In(CST).Format("01-02 15:04"))
			}
		}
	}
}
