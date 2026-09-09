package tickflow

import (
	"testing"
	"time"
)

// reportCal 是乙这一片的替身日历：每天的时段与模板都由测试摆布。
//
// ⚠️ 它存在的理由是 `calendar/embedded` **造不出**乙要测的那些情形 ——
// 它看不见停夜盘（本仓已登记的缺陷）⇒ 「标称>0 而实际=0 且连续两天」在真日历上不存在。
// ⛔ 而替身证明不了真日历也这样 ⇒ 那一半由 report_embedded_test.go 接着，
// **它用两份起点不同的真日历**（一份只证得了「在那一天」，证不了「跟着走」）。
type reportCal struct {
	from, to TradingDay
	nominal  []Session          // 模板里的夜盘（相对偏移，只用来算 NightMinutes）
	day      []Session          // 模板里的日盘
	night    map[TradingDay]int // 那一天【实际】开了多少分钟夜盘；缺省 = 与 nominal 同
	all      []TradingDay
}

func cstMs(y, m, d, hh, mm int) int64 {
	return time.Date(y, time.Month(m), d, hh, mm, 0, 0, CST).UnixMilli()
}

func (c *reportCal) Covers(ProductKey) (TradingDay, TradingDay, bool) {
	return c.from, c.to, c.from != 0
}

func (c *reportCal) Template(ProductKey, TradingDay) (SessionTemplate, error) {
	return SessionTemplate{Day: c.day, Night: c.nominal}, nil
}

func (c *reportCal) DayOf(_ ProductKey, n TradingDay) (Day, error) {
	found := false
	for _, d := range c.all {
		if d == n {
			found = true
			break
		}
	}
	if !found {
		return Day{}, ErrNotTradingDay
	}
	y, m, d := n.Split()
	var ss []Session
	// 夜盘：21:00 起（splitNightDay 按 CST 小时判，>=20 或 <4 算夜盘）。
	mins, ok := c.night[n]
	if !ok {
		mins = 0
		for _, s := range c.nominal {
			mins += s.Minutes()
		}
	}
	if mins > 0 {
		st := cstMs(y, m, d, 21, 0)
		ss = append(ss, Session{Start: st, End: st + int64(mins)*60000})
	}
	// 日盘：09:00 起。
	dm := 0
	for _, s := range c.day {
		dm += s.Minutes()
	}
	if dm > 0 {
		st := cstMs(y, m, d, 9, 0)
		ss = append(ss, Session{Start: st, End: st + int64(dm)*60000})
	}
	return Day{Num: n, Sessions: ss}, nil
}

func (c *reportCal) Walk(_ ProductKey, from, to TradingDay, fn func(Day) bool) error {
	if c.from == 0 || from < c.from || to > c.to {
		return ErrUncovered
	}
	for _, n := range c.all {
		if n < from || n > to {
			continue
		}
		d, err := c.DayOf(ProductKey{}, n)
		if err != nil {
			return err
		}
		if !fn(d) {
			return nil
		}
	}
	return nil
}

func (c *reportCal) DayAt(ProductKey, int64) (Day, error) { return Day{}, ErrUncovered }

var _ Calendar = (*reportCal)(nil)

// threeDays 是 08-06/07/10：日盘 225 分钟，夜盘标称 120 分钟。
func threeDays() *reportCal {
	return &reportCal{
		from: 20200806, to: 20200810,
		all:     []TradingDay{20200806, 20200807, 20200810},
		day:     []Session{{Start: 0, End: 225 * 60000}},
		nominal: []Session{{Start: 0, End: 120 * 60000}},
		night:   map[TradingDay]int{},
	}
}

// —— ClipToLastClosed（SYN-1）——

// TestClipToLastClosedDistinguishesNothingClosed 第二个返回值的不可分辨格。
//
// ⛔ 「一天都还没收盘」与「截到某一天」在**只看第一个返回值**时长得一样：
// 前者给 TradingDay(0)，而 0 读起来仍然像一个日期。同⑫、同 SYN-4。
func TestClipToLastClosedDistinguishesNothingClosed(t *testing.T) {
	cal := threeDays()
	// 08-06 的日盘 09:00 起 225 分钟 ⇒ 12:45 收；夜盘 21:00 起 ⇒ 23:00 收。
	before := cstMs(2020, 8, 6, 0, 0)
	got, ok, err := ClipToLastClosed(cal, testKey, 20200810, before)
	if err != nil {
		t.Fatalf("不该出错：%v", err)
	}
	if ok {
		t.Fatalf("一天都还没收盘，却报 ok=true（截到 %s）", got)
	}
	if got != 0 {
		t.Errorf("ok=false 时第一个返回值应当是零值，实得 %s", got)
	}

	// 对照：08-07 夜盘收盘之后 ⇒ 应当截到 08-07（08-10 还没到）。
	after := cstMs(2020, 8, 7, 23, 30)
	got, ok, err = ClipToLastClosed(cal, testKey, 20200810, after)
	if err != nil {
		t.Fatalf("不该出错：%v", err)
	}
	if !ok || got != 20200807 {
		t.Fatalf("期望截到 2020-08-07（ok=true），实得 %s ok=%v", got, ok)
	}
}

// TestClipToLastClosedClampsToCoverage 请求越出覆盖 ⇒ 截到覆盖内，而不是报错。
func TestClipToLastClosedClampsToCoverage(t *testing.T) {
	cal := threeDays()
	got, ok, err := ClipToLastClosed(cal, testKey, 20201231, cstMs(2021, 1, 1, 0, 0))
	if err != nil {
		t.Fatalf("不该出错：%v", err)
	}
	if !ok || got != 20200810 {
		t.Fatalf("期望截到覆盖末端 2020-08-10，实得 %s ok=%v", got, ok)
	}

	// 请求整段在覆盖【之前】⇒ 没有可用的一天，而这不是错误。
	got, ok, err = ClipToLastClosed(cal, testKey, 20200101, cstMs(2021, 1, 1, 0, 0))
	if err != nil {
		t.Fatalf("不该出错：%v", err)
	}
	if ok {
		t.Fatalf("请求整段在覆盖之前，却报 ok=true（%s）", got)
	}
}

func TestClipToLastClosedRejectsBadInput(t *testing.T) {
	if _, _, err := ClipToLastClosed(nil, testKey, 20200810, 0); err == nil {
		t.Error("没给日历，期望报错")
	}
	if _, _, err := ClipToLastClosed(threeDays(), testKey, 0, 0); err == nil {
		t.Error("to 不合法，期望报错")
	}
	if _, _, err := ClipToLastClosed(&reportCal{}, testKey, 20200810, 0); err == nil {
		t.Error("日历覆盖不到这个品种，期望报错（而不是静默返回没有）")
	}
}

// —— ScanBars（SYN-2 / SYN-5）——

func TestScanBarsCountsBarsButRecordsDays(t *testing.T) {
	cal := threeDays()
	p := MustIntraday(15)
	d, _ := cal.DayOf(testKey, 20200807)
	tmpl, _ := cal.Template(testKey, 20200807)
	grid := p.Bars(tmpl, d)
	if len(grid) < 3 {
		t.Fatalf("这个 fixture 要至少 3 格，实得 %d —— 前提没成立", len(grid))
	}

	// 三根：两根对齐、一根**终点**不对（起点对）。
	bars := []Bar{
		{Ts: grid[0].Open, TsEnd: grid[0].Close, TradingDay: 20200807},
		{Ts: grid[1].Open, TsEnd: grid[1].Close, TradingDay: 20200807},
		{Ts: grid[2].Open, TsEnd: grid[2].Close + 60000, TradingDay: 20200807},
	}
	mis, anom, err := ScanBars(cal, testKey, p, bars)
	if err != nil {
		t.Fatalf("不该出错：%v", err)
	}
	if mis != 1 {
		t.Errorf("期望 1 根对不上网格，实得 %d\n"+
			"  ⇒ 若得 0，多半是只比了 Ts 没比 TsEnd —— "+
			"而「起点对、终点不对」正是聚合口径出错最常见的样子", mis)
	}
	if len(anom) != 0 {
		t.Errorf("这一天不该可疑，实得 %v", anom)
	}
}

// TestScanBarsRecordsDayOnceNotPerBar SYN-5：可疑记【交易日】，不记根数。
//
// ⛔ 这一条是自查补上的：对照组「把每根都重算一次网格」（可疑按根累加）
// 在补它之前**不红** —— 因为当时**没有一条测试走到「可疑」那条路**
// （替身造的日子 nominal == actual ⇒ Anomalous 恒为 false）。
// ⇒ 又是那一格：**断言在，而没有输入走得到它。**
//
// 记根数错在哪：同一个事实在 60m 给约 6/交易日、在 1m 给约 345/交易日 ——
// **标志修到了交易日一级，计数又把它挂回周期上**（v0.1 那个坑的形状）。
func TestScanBarsRecordsDayOnceNotPerBar(t *testing.T) {
	cal := threeDays()
	cal.night = map[TradingDay]int{20200807: 90} // 标称 120、实际 90 ⇒ 矛盾
	p := MustIntraday(15)

	d, _ := cal.DayOf(testKey, 20200807)
	tmpl, _ := cal.Template(testKey, 20200807)
	grid := p.Bars(tmpl, d)
	anyFlag := false
	for _, bb := range grid {
		if bb.Anomalous {
			anyFlag = true
			break
		}
	}
	if !anyFlag {
		t.Fatalf("这个 fixture 要造出 Anomalous，而一格都没有（%d 格）——\n"+
			"  前提没成立，后面那条断言什么也没验", len(grid))
	}

	var bars []Bar
	for i := 0; i < 4 && i < len(grid); i++ {
		bars = append(bars, Bar{Ts: grid[i].Open, TsEnd: grid[i].Close, TradingDay: 20200807})
	}
	_, anom, err := ScanBars(cal, testKey, p, bars)
	if err != nil {
		t.Fatalf("不该出错：%v", err)
	}
	if len(anom) != 1 || anom[0] != 20200807 {
		t.Fatalf("期望【一天】(2020-08-07)，实得 %v（%d 条）\n"+
			"  ⇒ 条数等于根数 = 把交易日一级的标志挂回了周期上", anom, len(anom))
	}
}

func TestScanBarsRejectsZeroTradingDay(t *testing.T) {
	if _, _, err := ScanBars(threeDays(), testKey, MustIntraday(15),
		[]Bar{{Ts: 1, TsEnd: 2}}); err == nil {
		t.Fatal("TradingDay 是零值，期望报错（本层不猜它属于哪一天）")
	}
}

// —— ScanNightAbsent（SYN-7 / 8 / 9）——

// TestScanNightAbsentNotApplicableWhenNominalZero 标称夜盘为 0 的品种【不适用】，
// 而「不适用」与「适用但没找到」在 NightAbsent{Days:0} 上不可分辨 ⇒ 靠第二个返回值分开。
//
// 实测依据：CFFEX.IF 的标称夜盘就是 0，它**从来没有过夜盘** ——
// 照直报会把它的整段历史都报出来。
func TestScanNightAbsentNotApplicableWhenNominalZero(t *testing.T) {
	cal := threeDays()
	cal.nominal = nil // 标称夜盘 0
	cal.night = map[TradingDay]int{20200806: 0, 20200807: 0, 20200810: 0}

	got, ok, err := ScanNightAbsent(cal, testKey, cal.all)
	if err != nil {
		t.Fatalf("不该出错：%v", err)
	}
	if ok {
		t.Fatalf("标称夜盘为 0 的品种应当【不适用】，而它报 ok=true：%s\n"+
			"  ⇒ 照直报的话它的每一天都是「无夜盘」，一报报整段历史", got)
	}
	if got.Days != 0 {
		t.Errorf("不适用时不该给出段，实得 %s", got)
	}
}

// TestScanNightAbsentApplicableButNoRun 适用、而没有 ≥2 的段 ⇒ (零值, true, nil)。
func TestScanNightAbsentApplicableButNoRun(t *testing.T) {
	cal := threeDays()
	cal.night = map[TradingDay]int{20200807: 0} // 只有一天无夜盘
	got, ok, err := ScanNightAbsent(cal, testKey, cal.all)
	if err != nil {
		t.Fatalf("不该出错：%v", err)
	}
	if !ok {
		t.Fatal("这个品种标称夜盘 120，应当适用")
	}
	if got.Days != 0 {
		t.Errorf("只有 1 天无夜盘（低于阈值 2），不该报，实得 %s\n"+
			"  ⇒ 阈值 2 不是拍的：十年实测 1 天×52 段 / 64 天×1 段，2..63 一次都没出现过", got)
	}
}

// TestScanNightAbsentReportsRun 连续两天无夜盘 ⇒ 报出来（不判过期）。
func TestScanNightAbsentReportsRun(t *testing.T) {
	cal := threeDays()
	cal.night = map[TradingDay]int{20200807: 0, 20200810: 0}
	got, ok, err := ScanNightAbsent(cal, testKey, cal.all)
	if err != nil {
		t.Fatalf("不该出错：%v", err)
	}
	if !ok {
		t.Fatal("应当适用")
	}
	want := NightAbsent{Days: 2, From: 20200807, To: 20200810}
	if got != want {
		t.Fatalf("期望 %s，实得 %s", want, got)
	}
}

// TestScanNightAbsentKeepsFirstDayWhenRequestStartsLater ⛔ 这一条是【输入集】那一格：
//
// 上面 ExcludesCoversFrom 里 days[0] 恰好【就是】Covers().from ——
// 于是「排除 Covers().from」与「排除 days[0]」两种实现**在那条输入上给出同一个答案**，
// 那条测试对这个突变是瞎的（我自查时发现的，不是它报出来的）。
//
// ⇒ 这一条把两者分开：请求起点**晚于** Covers().from，而序列头一天是【真的】无夜盘日。
// 照「排除 days[0]」实现 ⇒ 剩 1 天 ⇒ 低于阈值 2 ⇒ **整段静默不报**
// —— 正是 SYN-7 存在的理由（评审方用 CFFEX.IF 造出的那个情形）。
func TestScanNightAbsentKeepsFirstDayWhenRequestStartsLater(t *testing.T) {
	cal := threeDays() // Covers().from = 2020-08-06
	cal.night = map[TradingDay]int{20200807: 0, 20200810: 0}

	days := []TradingDay{20200807, 20200810} // **起点晚于 Covers().from**
	got, ok, err := ScanNightAbsent(cal, testKey, days)
	if err != nil {
		t.Fatalf("不该出错：%v", err)
	}
	if !ok {
		t.Fatal("应当适用")
	}
	want := NightAbsent{Days: 2, From: 20200807, To: 20200810}
	if got != want {
		t.Fatalf("期望 %s，实得 %s\n"+
			"  ⇒ 得到零值 = 排除了序列头一天（而它是【真的】无夜盘日）\n"+
			"     ⇒ 剩 1 天低于阈值 ⇒ 【一整段真事实静默消失】", want, got)
	}
}

// TestScanNightAbsentRequiresAscendingDays 「连续」这个词要求 days 严格升序。
//
// ⛔ 乱序时算出来的段**读起来仍然像一个段** —— 坏输入产出一个像样的答案。
// 这个前提是自查对照组逼出来的（见 ScanNightAbsent 里那一段注释）。
func TestScanNightAbsentRequiresAscendingDays(t *testing.T) {
	cal := threeDays()
	cal.night = map[TradingDay]int{20200807: 0, 20200810: 0}
	for _, days := range [][]TradingDay{
		{20200810, 20200807},           // 降序
		{20200807, 20200807},           // 重复
		{20200807, 20200810, 20200806}, // 中间回头
	} {
		if _, _, err := ScanNightAbsent(cal, testKey, days); err == nil {
			t.Errorf("days=%v 不是严格升序，期望报错", days)
		}
	}
}

// TestScanNightAbsentExcludesCoversFrom SYN-9：排除的是 Covers().from 那一天。
//
// ⛔ 覆盖首日的 actual 恒为 0（夜盘挂在上一个交易日上，而它没有上一个交易日）——
// 那是**日历的假象**。若不排除，这里会得到 3 天而不是 2 天。
func TestScanNightAbsentExcludesCoversFrom(t *testing.T) {
	cal := threeDays()
	// 覆盖首日 08-06 造成假象（实际 0），随后两天是真的无夜盘。
	cal.night = map[TradingDay]int{20200806: 0, 20200807: 0, 20200810: 0}
	got, ok, err := ScanNightAbsent(cal, testKey, cal.all)
	if err != nil {
		t.Fatalf("不该出错：%v", err)
	}
	if !ok {
		t.Fatal("应当适用")
	}
	want := NightAbsent{Days: 2, From: 20200807, To: 20200810}
	if got != want {
		t.Fatalf("期望 %s，实得 %s\n"+
			"  ⇒ 得到 3 天 = 没排除 Covers().from，把日历的假象算成了市场事实", want, got)
	}
}
