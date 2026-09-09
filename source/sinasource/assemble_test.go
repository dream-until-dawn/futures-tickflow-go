package sinasource

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/embedded"
)

var cst = time.FixedZone("CST", 8*3600)

func at(y, mo, d, h, mi int) int64 {
	return time.Date(y, time.Month(mo), d, h, mi, 0, 0, cst).UnixMilli()
}

// testDays 是**手列的**交易日，不是从 fixture 里抽的。
//
// ⚠️ 这一条是刻意的：`calendar/embedded` 不自带交易日、由调用方注入，
// 而新浪日线返回的那串日期**本身就是该合约的交易日序列** ——
// 拿它注入再拿它来验，就是**循环论证**。
// ⇒ 手列一小段（2026-09-01…09-08，含一个周末），被验的那一侧才是独立的。
var testDays = []tickflow.TradingDay{
	20260901, 20260902, 20260903, 20260904, 20260907, 20260908,
}

func testCal(t *testing.T) tickflow.Calendar {
	t.Helper()
	cal, err := embedded.New(testDays)
	if err != nil {
		t.Fatalf("造测试日历：%v", err)
	}
	return cal
}

func rbReq() tickflow.BarRequest {
	return tickflow.BarRequest{
		Symbol: tickflow.Symbol{Exchange: tickflow.SHFE, Product: "rb", YearMon: 2610},
		Period: tickflow.Daily,
		From:   20260901,
		To:     20260908,
	}
}

// nowAfter 是「所有测试交易日都已收盘」的那个时刻。
var nowAfter = at(2026, 9, 9, 0, 0)

func rbRows(t *testing.T) []DailyRow {
	t.Helper()
	rows, err := ParseDaily(read(t, "daily_RB2610.jsonp"))
	if err != nil {
		t.Fatalf("解析 fixture：%v", err)
	}
	return rows
}

// —— 一、这一片存在的理由：Ts 推不出来，只能问日历 ——

// TestNightSessionPutsTsThreeCalendarDaysEarlier 是本片的头号事实。
//
// 2026-09-07 是周一。`SHFE.rb` 那个交易日的**第一段是周五 09-04 21:00 的夜盘** ——
// 也就是说这根日线的 Ts 落在它自己那个日期的**三个自然日之前**。
// ⇒ 光看 "2026-09-07" 这个字符串推不出 Ts，这正是组装必须要日历的原因。
func TestNightSessionPutsTsThreeCalendarDaysEarlier(t *testing.T) {
	bars, err := AssembleDaily(rbRows(t), testCal(t), rbReq(), nowAfter)
	if err != nil {
		t.Fatalf("组装失败：%v", err)
	}
	var mon *tickflow.Bar
	for i := range bars {
		if bars[i].TradingDay == 20260907 {
			mon = &bars[i]
		}
	}
	if mon == nil {
		t.Fatalf("没有 2026-09-07 那一根（共 %d 根）", len(bars))
	}
	wantTs := at(2026, 9, 4, 21, 0)  // 周五夜盘开盘
	wantEnd := at(2026, 9, 7, 15, 0) // 周一日盘收盘
	if mon.Ts != wantTs {
		t.Errorf("Ts = %s，期望 %s（周五夜盘）",
			time.UnixMilli(mon.Ts).In(cst).Format("01-02 15:04"),
			time.UnixMilli(wantTs).In(cst).Format("01-02 15:04"))
	}
	if mon.TsEnd != wantEnd {
		t.Errorf("TsEnd = %s，期望 %s",
			time.UnixMilli(mon.TsEnd).In(cst).Format("01-02 15:04"),
			time.UnixMilli(wantEnd).In(cst).Format("01-02 15:04"))
	}
	if d := time.UnixMilli(mon.TsEnd).Sub(time.UnixMilli(mon.Ts)); d < 60*time.Hour {
		t.Errorf("跨度只有 %v —— 周一那根应当从上周五晚上算起", d)
	}
}

// TestNoNightProductStartsSameDay 是上面那条的**对照组**。
//
// 没有它的话，「Ts 落在三天前」也可能只是我把日期算错了。
// 中金所国债无夜盘 ⇒ 同一个交易日的 Ts 就落在当天 09:30。
func TestNoNightProductStartsSameDay(t *testing.T) {
	rows, err := ParseDaily(read(t, "daily_T2612.jsonp"))
	if err != nil {
		t.Fatalf("%v", err)
	}
	req := rbReq()
	req.Symbol = tickflow.Symbol{Exchange: tickflow.CFFEX, Product: "T", YearMon: 2612}
	bars, err := AssembleDaily(rows, testCal(t), req, nowAfter)
	if err != nil {
		t.Fatalf("组装失败：%v", err)
	}
	for _, b := range bars {
		if b.TradingDay != 20260907 {
			continue
		}
		want := at(2026, 9, 7, 9, 30) // v0.2.0 修过的那一处：国债日盘 09:30 不是 09:15
		if b.Ts != want {
			t.Fatalf("CFFEX.T 无夜盘，Ts 应当是当天 09:30，得到 %s",
				time.UnixMilli(b.Ts).In(cst).Format("01-02 15:04"))
		}
		return
	}
	t.Fatalf("没有 2026-09-07 那一根")
}

// —— 二、组装出来的东西要满足 Source 契约 ——

// TestAssembledBarsPassCheckBars 拿契约层那个检查器核组装的产物。
//
// ⚠️ **CheckBars 是在这里调的，不在 AssembleDaily 里调。**
// 实现自己核自己的话，「组装出来的东西满足契约」就由被测者证明，
// 测试跟着变成同义反复。
func TestAssembledBarsPassCheckBars(t *testing.T) {
	req := rbReq()
	bars, err := AssembleDaily(rbRows(t), testCal(t), req, nowAfter)
	if err != nil {
		t.Fatalf("组装失败：%v", err)
	}
	if len(bars) == 0 {
		t.Fatal("一根都没组装出来 —— 下面那句 CheckBars 会在空切片上恒绿")
	}
	if err := tickflow.CheckBars(req, bars, nowAfter); err != nil {
		t.Fatalf("组装的产物不满足 Source 契约：%v", err)
	}
	for _, b := range bars {
		if !b.Flags.Has(tickflow.FlagSrcSina) {
			t.Fatalf("%s 那根没打来源标记 —— 多源共存时「这根是哪来的」事后推不出来", b.TradingDay)
		}
	}
	t.Logf("组装出 %d 根，全部通过 CheckBars 且带 FlagSrcSina", len(bars))
}

func TestSettleNaNSurvivesAssembly(t *testing.T) {
	rows, err := ParseDaily(read(t, "daily_T2612.jsonp"))
	if err != nil {
		t.Fatalf("%v", err)
	}
	req := rbReq()
	req.Symbol = tickflow.Symbol{Exchange: tickflow.CFFEX, Product: "T", YearMon: 2612}
	bars, err := AssembleDaily(rows, testCal(t), req, nowAfter)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if len(bars) == 0 {
		t.Fatal("一根都没有 —— 下面的循环恒绿")
	}
	for _, b := range bars {
		if b.HasSettle() {
			t.Fatalf("%s 那根报了有结算价，而中金所是系统性缺失", b.TradingDay)
		}
		if !math.IsNaN(b.Settle) {
			t.Fatalf("%s 那根的 Settle=%v，应当是 NaN 而不是 0", b.TradingDay, b.Settle)
		}
	}
}

// —— 三、四条「不静默」 ——

func TestUncoveredRangeIsAnErrorNotAPartialResult(t *testing.T) {
	req := rbReq()
	req.From = 20200601 // 日历只注入了 2026-09 那几天
	bars, err := AssembleDaily(rbRows(t), testCal(t), req, nowAfter)
	if !errors.Is(err, ErrCalendarGap) {
		t.Fatalf("请求超出日历覆盖，应当报 ErrCalendarGap，得到 %v", err)
	}
	if len(bars) != 0 {
		t.Fatalf("报错的同时还给了 %d 根 —— 部分结果会被 Syncer 记成「拉过，确认没有」", len(bars))
	}
}

func TestUnfinishedBarIsDropped(t *testing.T) {
	req := rbReq()
	full, err := AssembleDaily(rbRows(t), testCal(t), req, nowAfter)
	if err != nil {
		t.Fatalf("%v", err)
	}
	// 把「现在」拨回 09-07 收盘前一毫秒 ⇒ 09-07 与 09-08 两根都还没走完
	earlier := at(2026, 9, 7, 15, 0) - 1
	part, err := AssembleDaily(rbRows(t), testCal(t), req, earlier)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if len(part) >= len(full) {
		t.Fatalf("把 now 拨到 09-07 收盘前，根数应当变少：满 %d 根 vs 拨回后 %d 根",
			len(full), len(part))
	}
	for _, b := range part {
		if b.TsEnd > earlier {
			t.Fatalf("%s 那根 TsEnd 在 now 之后，没被丢掉", b.TradingDay)
		}
	}
	t.Logf("满 %d 根 → 拨回后 %d 根（丢掉 %d 根未完结的）", len(full), len(part), len(full)-len(part))
}

func TestNotAscendingIsAnError(t *testing.T) {
	rows := []DailyRow{
		{Date: "2026-09-03", Open: 1, High: 1, Low: 1, Close: 1},
		{Date: "2026-09-02", Open: 1, High: 1, Low: 1, Close: 1},
	}
	_, err := AssembleDaily(rows, testCal(t), rbReq(), nowAfter)
	if !errors.Is(err, ErrNotAscending) {
		t.Fatalf("乱序应当报 ErrNotAscending（不排序），得到 %v", err)
	}
	// 重复日期也算不升序 —— 两根同一天在 coverage 的 Days 计数上会错。
	dup := []DailyRow{{Date: "2026-09-02"}, {Date: "2026-09-02"}}
	if _, err := AssembleDaily(dup, testCal(t), rbReq(), nowAfter); !errors.Is(err, ErrNotAscending) {
		t.Fatalf("重复日期应当报 ErrNotAscending，得到 %v", err)
	}
}

func TestSinaDisagreesWithCalendar(t *testing.T) {
	// 2026-09-05 是周六：日历里没注入它，而这里假装新浪给了一根。
	rows := []DailyRow{{Date: "2026-09-05", Open: 1, High: 1, Low: 1, Close: 1}}
	_, err := AssembleDaily(rows, testCal(t), rbReq(), nowAfter)
	if !errors.Is(err, ErrSinaDisagreesWithCalendar) {
		t.Fatalf("日历说不是交易日而新浪给了一根，应当报 ErrSinaDisagreesWithCalendar，得到 %v", err)
	}
}

// —— 四、区间外是常态，不是异常 ——

func TestOutOfRangeRowsAreSkippedQuietly(t *testing.T) {
	req := rbReq()
	req.From, req.To = 20260907, 20260908 // fixture 有 221 行，这里只要两天
	bars, err := AssembleDaily(rbRows(t), testCal(t), req, nowAfter)
	if err != nil {
		t.Fatalf("新浪一次给全部历史，区间外应当安静跳过，却报：%v", err)
	}
	if len(bars) != 2 {
		t.Fatalf("期望 2 根（09-07 / 09-08），得到 %d", len(bars))
	}
	for _, b := range bars {
		if b.TradingDay < req.From || b.TradingDay > req.To {
			t.Fatalf("%s 落在请求区间之外", b.TradingDay)
		}
	}
}

// —— 五、拒绝的输入 ——

func TestAssembleRejectsBadInput(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*tickflow.BarRequest)
		cal  func(t *testing.T) tickflow.Calendar
		want string
	}{
		{"周期不是日线", func(r *tickflow.BarRequest) { r.Period = tickflow.MustIntraday(1) }, testCal, "只组装日线"},
		{"没给日历", nil, func(*testing.T) tickflow.Calendar { return nil }, "没给日历"},
		{"请求不合法", func(r *tickflow.BarRequest) { r.From, r.To = r.To, r.From }, testCal, "请求不合法"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := rbReq()
			if c.mut != nil {
				c.mut(&req)
			}
			_, err := AssembleDaily(rbRows(t), c.cal(t), req, nowAfter)
			if err == nil {
				t.Fatal("应当被拒，却通过了")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("红了，但报的不是那一条。期望含 %q，实际：%v", c.want, err)
			}
		})
	}
}
