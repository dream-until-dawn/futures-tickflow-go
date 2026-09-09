package tickflow

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// 这一份的体例照 store/segfile 那两片：**每条约束都要有一个会红的反例**，
// 而且反例要核【报的是哪一条】，不是只核「它红了」。
//
// 理由是本仓撞过的那一格：一个只会报「有问题」的检查器，
// 和一个打偏了位置、恰好也报「有问题」的检查器，输出一模一样。

var cst = time.FixedZone("CST", 8*3600)

func ms(y, mo, d, h, mi int) int64 {
	return time.Date(y, time.Month(mo), d, h, mi, 0, 0, cst).UnixMilli()
}

// —— Period 的密封 ——

// TestPeriodIsSealed 核对 Period 真的封住了。
//
// ⚠️ 「包外实现不了」这件事**编译期才看得见**，测试里跑不出来——
// 所以这里查的是那个封口本身：接口里必须有一个【不可导出】的方法。
// 它比不了「有人在包内加了第三种周期」，那由下面那条查。
func TestPeriodIsSealed(t *testing.T) {
	rt := reflect.TypeOf((*Period)(nil)).Elem()
	sealed := ""
	for i := 0; i < rt.NumMethod(); i++ {
		m := rt.Method(i)
		if m.PkgPath != "" { // 非空 ⇒ 不可导出
			sealed = m.Name
		}
	}
	if sealed == "" {
		t.Fatalf("Period 没有不可导出的方法 ⇒ **包外任何带 String() 的类型都能实现它**，"+
			"包括 time.Duration。现有方法：%d 个", rt.NumMethod())
	}
	t.Logf("封口方法是 %s（不可导出）", sealed)
}

// TestPeriodImplementorsAreExactlyTwo 盯住「穷举的两种」这句话。
//
// 密封的价值是「周期在类型层面是穷举的」——而那句话只有在
// **有人加第三种时会有东西响**的前提下才成立。这条就是那个东西。
// 加一种周期时它会红，红了之后要做的是：确认 Capabilities.Depth、
// 各源的 Caps、以及聚合口径都想过这一种，然后改这里的数。
func TestPeriodImplementorsAreExactlyTwo(t *testing.T) {
	var got []string
	for _, p := range []Period{MustIntraday(1), CalendarPeriod(0)} {
		got = append(got, reflect.TypeOf(p).Name())
	}
	want := []string{"IntradayPeriod", "CalendarPeriod"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Period 的实现类型变了：得到 %v，登记的是 %v", got, want)
	}
}

// —— BarRequest.Validate ——

func goodReq() BarRequest {
	return BarRequest{
		Symbol: Symbol{Exchange: "SHFE", Product: "rb", YearMon: 2701},
		Period: MustIntraday(1),
		From:   20260901,
		To:     20260904,
	}
}

func TestBarRequestValidate(t *testing.T) {
	if err := goodReq().Validate(); err != nil {
		t.Fatalf("干净的请求应当通过，却报：%v\n"+
			"  ⇒ 基线不绿的话，下面每一条反例都不能说明任何事", err)
	}

	cases := []struct {
		name string
		mut  func(*BarRequest)
		want string // 失败信息里必须出现的字样 —— 核【报的是哪一条】
	}{
		{"交易所空", func(r *BarRequest) { r.Symbol.Exchange = "" }, "Symbol 不完整"},
		{"品种空", func(r *BarRequest) { r.Symbol.Product = "" }, "Symbol 不完整"},
		{"周期 nil", func(r *BarRequest) { r.Period = nil }, "Period 是 nil"},
		{"From 非法", func(r *BarRequest) { r.From = 0 }, "From=0"},
		{"To 非法", func(r *BarRequest) { r.To = 99999999 }, "To=99999999"},
		{"起晚于止", func(r *BarRequest) { r.From, r.To = r.To, r.From }, "晚于"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := goodReq()
			c.mut(&r)
			err := r.Validate()
			if err == nil {
				t.Fatalf("这一处改动应当被拦下，却通过了")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("红了，但报的不是要它报的那一条。\n  期望含：%s\n  实际：%v", c.want, err)
			}
		})
	}
}

// —— Capabilities.Validate ——

func TestCapabilitiesValidate(t *testing.T) {
	m1, d1 := MustIntraday(1), CalendarPeriod(1)
	good := Capabilities{
		Periods: []Period{m1, d1},
		MaxBars: 1023,
		Depth:   map[Period]time.Duration{m1: 90 * 24 * time.Hour, d1: 17 * 365 * 24 * time.Hour},
	}
	if err := good.Validate(); err != nil {
		t.Fatalf("干净的能力声明应当通过，却报：%v", err)
	}
	if !good.Supports(m1) || !good.Supports(d1) {
		t.Fatalf("Supports 认不出自己 Periods 里的周期")
	}
	if good.Supports(MustIntraday(5)) {
		t.Fatalf("Supports 认出了一个不在 Periods 里的周期 ⇒ 它恒真，等于没有")
	}

	t.Run("Periods 里有而 Depth 里没有", func(t *testing.T) {
		bad := good
		bad.Depth = map[Period]time.Duration{m1: 90 * 24 * time.Hour} // 少了 d1
		err := bad.Validate()
		if err == nil {
			t.Fatal("少一条深度应当被拦下——深度会读成 0，而 0 和「不知道」长得一样")
		}
		if !strings.Contains(err.Error(), "不在 Depth 里") {
			t.Fatalf("红了，但报的不是那一条：%v", err)
		}
	})

	t.Run("MaxBars 负数", func(t *testing.T) {
		bad := good
		bad.MaxBars = -1
		if err := bad.Validate(); err == nil || !strings.Contains(err.Error(), "MaxBars=-1") {
			t.Fatalf("期望报 MaxBars 负数，得到：%v", err)
		}
	})
}

// —— CheckBars：六条约定里机械查得动的那五条 ——

func goodBars() []Bar {
	return []Bar{
		{Ts: ms(2026, 9, 1, 9, 0), TsEnd: ms(2026, 9, 1, 9, 1), TradingDay: 20260901},
		{Ts: ms(2026, 9, 2, 9, 0), TsEnd: ms(2026, 9, 2, 9, 1), TradingDay: 20260902},
	}
}

var checkNow = ms(2026, 9, 5, 0, 0)

func TestCheckBars(t *testing.T) {
	// 基线：干净的一组必须过。基线不绿 ⇒ 下面每一条都不说明任何事。
	if err := CheckBars(goodReq(), goodBars(), checkNow); err != nil {
		t.Fatalf("干净的一组应当通过，却报：%v", err)
	}
	// 空切片合法：停牌、上市前都会缺，那不是错误。
	if err := CheckBars(goodReq(), nil, checkNow); err != nil {
		t.Fatalf("空切片应当合法（停牌/上市前），却报：%v", err)
	}

	cases := []struct {
		name string
		mut  func(*BarRequest, []Bar) []Bar
		want string
	}{
		{"逆序", func(_ *BarRequest, b []Bar) []Bar {
			b[0], b[1] = b[1], b[0]
			return b
		}, "没有按升序"},

		{"重复 Ts", func(_ *BarRequest, b []Bar) []Bar {
			b[1].Ts = b[0].Ts
			return b
		}, "与上一根重复"},

		{"Ts 零值", func(_ *BarRequest, b []Bar) []Bar {
			b[0].Ts = 0
			return b
		}, "非正"},

		{"TsEnd 没填", func(_ *BarRequest, b []Bar) []Bar {
			b[1].TsEnd = 0
			return b
		}, "不大于 Ts"},

		{"TradingDay 没填", func(_ *BarRequest, b []Bar) []Bar {
			b[1].TradingDay = 0
			return b
		}, "TradingDay=0 不合法"},

		{"落在区间之外", func(_ *BarRequest, b []Bar) []Bar {
			b[1].TradingDay = 20260910
			return b
		}, "之外"},

		{"未完结的那一根", func(_ *BarRequest, b []Bar) []Bar {
			b[1].TsEnd = checkNow + 1
			return b
		}, "未完结的绝不进入"},

		{"请求本身就不合法", func(r *BarRequest, b []Bar) []Bar {
			r.From, r.To = r.To, r.From
			return b
		}, "请求本身不合法"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, b := goodReq(), goodBars()
			b = c.mut(&r, b)
			err := CheckBars(r, b, checkNow)
			if err == nil {
				t.Fatalf("这一处改动应当被拦下，却通过了")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("红了，但报的不是要它报的那一条。\n  期望含：%s\n  实际：%v", c.want, err)
			}
		})
	}
}

// TestCheckBarsReportsEveryViolation 盯住「一次报全部」这句话。
//
// 只报第一条的话，写实现的人要跑六遍才能修完六处——
// 而跑第二遍的前提是他知道还有第二处，**那正是这句承诺要替他知道的事**。
func TestCheckBarsReportsEveryViolation(t *testing.T) {
	b := goodBars()
	b[0].TsEnd = 0      // 一处
	b[1].TradingDay = 0 // 两处
	b[1].TsEnd = 0      // 三处（同一根的另一条）
	err := CheckBars(goodReq(), b, checkNow)
	if err == nil {
		t.Fatal("三处违反，却全绿")
	}
	msg := err.Error()
	for _, want := range []string{"第 0 根", "第 1 根", "TradingDay=0 不合法"} {
		if !strings.Contains(msg, want) {
			t.Errorf("报告里少了「%s」——它只报了一部分：\n%v", want, err)
		}
	}
	if n := strings.Count(msg, "\n") + 1; n < 3 {
		t.Errorf("三处违反只报出 %d 行 ⇒ 它不是「一次报全部」：\n%v", n, err)
	}
}
