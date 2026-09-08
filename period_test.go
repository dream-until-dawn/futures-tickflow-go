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
		// 停夜盘日：模板有夜盘，当天实际没有（实际 < 标称，实测过的那一侧）
		{"ag 停夜盘", tmplAG, day(20260904, "2026-09-03", "2026-09-04", 0)},
		{"rb 停夜盘", tmplRB, day(20260904, "2026-09-03", "2026-09-04", 0)},
		// 模板过期：实际 > 标称。**这一侧原先只有上界那条测试覆盖**——
		// 成对的不变量必须跑在【同一片输入】上，否则「成对」是假的：
		// 一条测 A 集合、另一条测 B 集合，两个方向就各留了一半没人看。
		{"cu模板/实际330", tmplCU, day(20260904, "2026-09-03", "2026-09-04", 330)},
		{"ag模板/实际390", tmplAG, day(20260904, "2026-09-03", "2026-09-04", 390)},
		{"si模板/实际120", tmplSI, day(20260904, "2026-09-03", "2026-09-04", 120)},
	}
	for _, mins := range []int{1, 5, 15, 30, 60, 90} {
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

// TestBarsNeverExceedOnePeriod 是 TestBarsCoverEveryTradingMinute 的【另一半】。
//
// 守恒类不变量是有方向的，必须成对：
//
//	下界「不漏」——每一分钟至少落在一根里；
//	上界「不超」——每一根至多装一个周期。
//
// 只写一条，另一个方向的错误照样全绿。这条就是被那样漏掉的：
// 标称夜盘 240 而当日实际 330 时，第一根日盘装了 90 分钟、Full=true，
// 而覆盖检查 555=555 通过、收盘标签序列一根不差。
//
// Full=true 可以装得【少】（停夜盘日沪银第一根只装 30 分钟，因为那 30 分钟
// 来自一个没开的夜盘），但绝不该装得【多】——多出来的时间是从别处偷的。
func TestBarsNeverExceedOnePeriod(t *testing.T) {
	tmpls := map[string]SessionTemplate{
		"标称120": tmplRB, "标称240": tmplCU, "标称330": tmplAG, "标称0": tmplSI,
	}
	// 实际夜盘长度遍历：既覆盖「实际 < 标称」（停夜盘），
	// 也覆盖「实际 > 标称」（交易所延长夜盘、模板过期）——**后者正是漏掉的方向**
	for _, actual := range []int{0, 90, 120, 150, 240, 300, 330, 390} {
		d := day(20260907, "2026-09-04", "2026-09-07", actual)
		for _, mins := range []int{1, 5, 15, 30, 60, 90} {
			p := MustIntraday(mins)
			for name, tmpl := range tmpls {
				for i, b := range p.Bars(tmpl, d) {
					if got := b.Minutes(d); got > mins {
						t.Errorf("%s 实际夜盘 %d 分 %s：第 %d 根装了 %d 分钟 > 周期 %d"+
							"（Full=%v Anomalous=%v）——多出来的时间是从别处偷的",
							name, actual, p, i, got, mins, b.Full, b.Anomalous)
					}
				}
			}
		}
	}
}

// TestStaleTemplateIsFlaggedNotSmoothed 钉住 I1 的【行为决定】。
//
// 当日实际夜盘比标称长 ⇒ 标称模板过期。这一侧【未验】（实测只覆盖实际 ≤ 标称），
// 未验就不替它选一套口径：把夜盘残段单独冲刷成一根短的并标 Anomalous，
// 让矛盾显式冒到上层，而不是安静地摊进网格。
func TestStaleTemplateIsFlaggedNotSmoothed(t *testing.T) {
	// 标称 240（相位 0），实际 330（余数 30）
	d := day(20260907, "2026-09-04", "2026-09-07", 330)
	bars := MustIntraday(60).Bars(tmplCU, d)

	// 超装那段被单独冲刷成一根短的，而不是硬塞进第一根日盘
	short := 0
	for _, b := range bars {
		if !b.Full && time.UnixMilli(b.Open).In(CST).Format("01-02 15:04") == "09-05 02:00" {
			short++
		}
	}
	if short != 1 {
		t.Errorf("夜盘残段应单独成一根短的（从 09-05 02:00 起），得到 %d 根", short)
	}

	// 【K1】矛盾是交易日一级的事实，与周期无关：每个周期都要报，且整天都带标。
	//
	// 原先判据是 nightCarried > phase——两个【取模后的余数】相比，
	// 于是「标称 330 / 实际 310」这同一个事实在 15m/30m 报、5m/60m/90m 不报。
	// 上层按周期扫这一位就按周期漏，而漏掉的那些看起来完全正常。
	for _, tc := range []struct {
		tmpl    SessionTemplate
		actual  int
		nominal int
	}{
		{tmplCU, 330, 240}, // 实际比标称长
		{tmplAG, 310, 330}, // 实际比标称短——K1 报的就是这一组
		{tmplSI, 120, 0},   // 本无夜盘的品种开了夜盘
	} {
		dd := day(20260907, "2026-09-04", "2026-09-07", tc.actual)
		if n, a, bad := dd.TemplateMismatch(tc.tmpl); !bad || n != tc.nominal || a != tc.actual {
			t.Errorf("TemplateMismatch(标称%d/实际%d) = (%d,%d,%v)，期望矛盾",
				tc.nominal, tc.actual, n, a, bad)
		}
		for _, mins := range []int{1, 5, 15, 30, 60, 90} {
			got := MustIntraday(mins).Bars(tc.tmpl, dd)
			flagged := 0
			for _, b := range got {
				if b.Anomalous {
					flagged++
				}
			}
			if flagged != len(got) {
				t.Errorf("标称%d/实际%d %dm：%d/%d 根带标——"+
					"矛盾是交易日一级的事实，不该随周期变",
					tc.nominal, tc.actual, mins, flagged, len(got))
			}
		}
	}

	// 反向：模板与实际【一致】时，一根都不该被标——
	// 否则这一位会因为总是亮着而被上层忽略，等于没有。
	for _, tc := range []struct {
		tmpl   SessionTemplate
		actual int
	}{
		// 实际 == 标称：本来就一致
		{tmplRB, 120}, {tmplCU, 240}, {tmplAG, 330}, {tmplSI, 0},
		// 停夜盘日（实际为 0）：这是实测过的、预期内的逐日事实，**不算模板过期**。
		// 把它算成矛盾，这一位会在每个长假前后亮起来——
		// 而一个经常亮的标志和一个不亮的标志一样没用。
		{tmplAG, 0}, {tmplCU, 0}, {tmplRB, 0},
	} {
		for _, mins := range []int{5, 15, 30, 60} {
			d := day(20260907, "2026-09-04", "2026-09-07", tc.actual)
			for _, b := range MustIntraday(mins).Bars(tc.tmpl, d) {
				if b.Anomalous {
					t.Errorf("标称夜盘 %d / 实际 %d / %dm：不该有 Anomalous",
						tc.tmpl.NightMinutes(), tc.actual, mins)
				}
			}
		}
	}
}

// TestGroupKeepsGroupWithEmptyEdgeDay 边界日没有时段时，【整组】不该被静默丢掉。
//
// 丢的是一整周或一整月，而且不出声；组里其余的日全都有时段也照丢。
func TestGroupKeepsGroupWithEmptyEdgeDay(t *testing.T) {
	full := func(num TradingDay, date string) Day {
		return day(num, "2026-09-04", date, 0)
	}
	// 同一周：周一空、周二三有、周五空
	week := []Day{
		{Num: 20260907},
		full(20260908, "2026-09-08"),
		full(20260909, "2026-09-09"),
		{Num: 20260911},
	}
	got := Weekly.Group(week)
	if len(got) != 1 {
		t.Fatalf("整周应当仍出一根，得到 %d 根", len(got))
	}
	if o := time.UnixMilli(got[0].Open).In(CST).Format("01-02 15:04"); o != "09-08 09:00" {
		t.Errorf("应从首个【有时段】的日开盘 09-08 09:00 起，得到 %s", o)
	}
	if c := time.UnixMilli(got[0].Close).In(CST).Format("01-02 15:04"); c != "09-09 15:00" {
		t.Errorf("应收在末个【有时段】的日 09-09 15:00，得到 %s", c)
	}
	// 全组都没有时段：那一组确实没有交易时间，不出根是对的答案，不是丢失
	if n := len(Weekly.Group([]Day{{Num: 20260907}, {Num: 20260908}})); n != 0 {
		t.Errorf("全空的一组不该凭空造出 %d 根", n)
	}
}

// TestNightRemainderStillMergesWhenTemplateMatches 守住「残段单独冲刷」这个改法
// 最大的回归风险：模板与实际【一致】时，跨隔夜那一根不能被拆掉。
//
// 沪银 330/330 的 60m，02:00–02:30 的余数要与次日 09:00–09:30 拼成【一根完整的】
// 60m，标注 09:30——这是实测基线（probe.md），也是 BarBound 注释里的那句承诺。
// 拆掉它同样满足覆盖与上界（两根短的，总量不变），所以那两条不变量看不见这次回归。
func TestNightRemainderStillMergesWhenTemplateMatches(t *testing.T) {
	d := day(20260904, "2026-09-03", "2026-09-04", 330)
	bars := MustIntraday(60).Bars(tmplAG, d)
	var got *BarBound
	for i := range bars {
		if time.UnixMilli(bars[i].Close).In(CST).Format("15:04") == "09:30" {
			got = &bars[i]
		}
	}
	if got == nil {
		t.Fatal("没找到标注 09:30 的那一根")
	}
	if o := time.UnixMilli(got.Open).In(CST).Format("01-02 15:04"); o != "09-04 02:00" {
		t.Errorf("它应当从前一日 02:00 起（跨隔夜缺口），得到 %s", o)
	}
	if m := got.Minutes(d); m != 60 {
		t.Errorf("它应当装满 60 分钟（02:00–02:30 + 09:00–09:30），实际 %d", m)
	}
	if !got.Full {
		t.Error("它是个完整格子")
	}
	if got.Anomalous {
		t.Error("模板与实际一致，不该带矛盾标记")
	}
}

// TestKnownDefect_PermanentNightCancellationLooksLikeHoliday 钉住一条
// 【按设计静默】的洞，不是期望行为。
//
// TemplateMismatch 豁免 actual == 0，理由是「停一天不构成标称错了的证据」。
// 但**单日停（长假）与永久取消，在 Day 这一级不可分辨**——
// 而后者是真的模板过期：
//
//	mismatch 永远 false（actual > 0 这个前置条件永远不成立）；
//	Phase 继续按陈旧的标称 330 算；
//	沪银日盘一直给 09:30/10:45/13:45/14:45/15:00，而实际已是 10:00/11:15/14:15/15:00。
//	每一根都错 30 分钟，永远，静默。
//
// 区分需要【序列】（连续 N 个交易日 actual == 0），那是 v0.3 Syncer 的层级，
// 不是 Day 的——所以边界划在这里是对的，洞是它的代价，代价要记账。
//
// 这条断言的是**错误行为**：v0.3 补上序列级检测后它会变红，
// 那时请一并更新 Day.TemplateMismatch 的文档与 contract.md 那一行风险。
func TestKnownDefect_PermanentNightCancellationLooksLikeHoliday(t *testing.T) {
	// 交易所永久取消了沪银夜盘，而内置模板仍写着 330
	d := day(20260907, "2026-09-04", "2026-09-07", 0)

	if _, actual, bad := d.TemplateMismatch(tmplAG); bad || actual != 0 {
		t.Fatalf("按当前设计，实际=0 不算矛盾（得到 actual=%d mismatch=%v）；"+
			"若这是 v0.3 序列级检测修好的结果，"+
			"请一并更新 Day.TemplateMismatch 的文档与 contract.md 的风险行",
			actual, bad)
	}

	// 而它切出来的网格【确实是错的】——这才是这个洞的代价
	p := MustIntraday(60)
	stale := dayPart(p.Bars(tmplAG, d)) // 按过期模板（标称 330）
	truth := dayPart(p.Bars(tmplSI, d)) // 按真实情况（无夜盘）
	if eq(stale, truth) {
		t.Fatalf("两者本应不同，得到同一组 %v——"+
			"若相位规则改了，这条已知缺陷的代价描述要重写", stale)
	}
	if want := []string{"09:30", "10:45", "13:45", "14:45", "15:00"}; !eq(stale, want) {
		t.Errorf("过期模板切出 %v，期望 %v", stale, want)
	}
	if want := []string{"10:00", "11:15", "14:15", "15:00"}; !eq(truth, want) {
		t.Errorf("真实情况切出 %v，期望 %v", truth, want)
	}
}
