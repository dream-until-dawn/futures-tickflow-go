package tickflow_test

// 这一份守的是 `Calendar` 接口的【契约】，不是某一个实现的私事。
//
// 为什么在根包而不是 calendar/embedded 包内（评审方 2026-09-08 定的）：
// 这条契约写在 calendar.go 的接口注释里 —— **守卫要放在它所守的那句话旁边**。
// 放进某个实现的包内，等于说这是那个实现的私事；
// **那么第二个实现来的时候，它没有任何机械途径知道契约里有这一条。**
//
// ⚠️ 它是【外部测试包】（package tickflow_test），这样才能 import calendar/embedded
// 而不形成循环。写成 package tickflow 去 import 它就是真的循环 ——
// 而那一格写错的后果是【编译不过】，所以它自己会告诉你，不需要守卫守它。
//
// ⚠️ 实现表写成【构造函数表】而不是写死 embedded：**今天只有一个实现，
// 而这条测试的形状要能容下第二个** —— 否则第二个实现到来时，
// 人会去复制这个文件，那正是本仓「第二份拷贝」那一族。

import (
	"errors"
	"testing"
	"time"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/embedded"
)

type calImpl struct {
	name string
	// newCal 用给定的交易日构造一个实现。第二个实现进来时在这里加一行。
	newCal func(t *testing.T, days []tickflow.TradingDay) tickflow.Calendar
}

var calImpls = []calImpl{
	{"embedded", func(t *testing.T, days []tickflow.TradingDay) tickflow.Calendar {
		t.Helper()
		c, err := embedded.New(days)
		if err != nil {
			t.Fatalf("构造 embedded 失败：%v", err)
		}
		return c
	}},
}

// au 有夜盘（21:00–02:30），正好用来撑起「夜盘挂在上一个交易日」那一格。
var contractKey = tickflow.ProductKey{Exchange: tickflow.SHFE, Product: "au"}

func midnightCST(d tickflow.TradingDay) int64 {
	y, m, day := d.Split()
	return time.Date(y, time.Month(m), day, 0, 0, 0, 0, tickflow.CST).UnixMilli()
}

// 一组「像生产」的连续交易日，全部 >= 内置模板生效起点。
var contractDays = []tickflow.TradingDay{20200506, 20200507, 20200508, 20200511, 20200512}

// ───────── 契约一：覆盖之外的一律 ErrUncovered，四个方法口径一致 ─────────
//
// ⚠️ 这条不是新规矩，是 calendar.go 早就写着的那条：
// 「本库不猜未来：覆盖不到的一律 ErrUncovered……实盘要判断明天是否开市，得靠交易所公告」。
// 而 2026-09-08 实测：四个方法里只有 Walk 真的这么做，另外三个各自把「答不了」
// 翻译成了一句不同的、听上去都很合理的话（详见 docs/contract.md §5）。

func TestCalendarContract_OutsideCoverage_Red(t *testing.T) {
	for _, impl := range calImpls {
		t.Run(impl.name, func(t *testing.T) {
			cal := impl.newCal(t, contractDays)
			cf, ct, ok := cal.Covers(contractKey)
			if !ok {
				t.Fatal("Covers 报 ok=false —— 这一格没法测")
			}
			// 覆盖之外的两个方向：ct 之后（「明天」），cf 之前。
			for _, num := range []tickflow.TradingDay{ct + 1, 20260101, cf - 1} {
				if _, err := cal.DayOf(contractKey, num); !errors.Is(err, tickflow.ErrUncovered) {
					t.Errorf("DayOf(%s) 应当 ErrUncovered，实得 %v"+
						"\n（把「答不了」说成「那天不交易」——而「明天开不开市」正是要去看公告的那一格）", num, err)
				}
				if _, err := cal.Template(contractKey, num); !errors.Is(err, tickflow.ErrUncovered) {
					t.Errorf("Template(%s) 应当 ErrUncovered，实得 %v"+
						"\n（成功返回模板最坏：它给的是一个能被拿去算相位的结构体）", num, err)
				}
				if err := cal.Walk(contractKey, num, num, func(tickflow.Day) bool { return true }); !errors.Is(err, tickflow.ErrUncovered) {
					t.Errorf("Walk(%s) 应当 ErrUncovered，实得 %v", num, err)
				}
			}
			// DayAt 要用一个【落在时段内】的时间戳才测得出来 ——
			// 用覆盖内某天的时段中点做标定，再整日平移到覆盖之外。
			day, err := cal.DayOf(contractKey, ct)
			if err != nil {
				t.Fatalf("取标定日失败：%v", err)
			}
			s := day.Sessions[len(day.Sessions)-1]
			mid := (s.Start + s.End) / 2
			if _, err := cal.DayAt(contractKey, mid); err != nil {
				t.Fatalf("标定不成立：覆盖内的时段中点本身就报错 %v —— 后面那两格没有意义", err)
			}
			for _, off := range []int64{1, 3, 365} {
				ts := mid + off*86400000
				if _, err := cal.DayAt(contractKey, ts); !errors.Is(err, tickflow.ErrUncovered) {
					t.Errorf("DayAt(标定时刻 +%d 天，已在覆盖之外) 应当 ErrUncovered，实得 %v"+
						"\n（说成 ErrClosed 就是把「答不了」降级成了一个实质答案）", off, err)
				}
			}
		})
	}
}

func TestCalendarContract_OutsideCoverage_Green(t *testing.T) {
	for _, impl := range calImpls {
		t.Run(impl.name, func(t *testing.T) {
			cal := impl.newCal(t, contractDays)
			// 与 _Red 只差一件事：问的日期在覆盖【之内】。四个方法都不许报 ErrUncovered。
			for _, num := range contractDays {
				if _, err := cal.DayOf(contractKey, num); err != nil {
					t.Errorf("DayOf(%s) 误伤：覆盖内的交易日被判答不了：%v", num, err)
				}
				if _, err := cal.Template(contractKey, num); err != nil {
					t.Errorf("Template(%s) 误伤：%v", num, err)
				}
				if err := cal.Walk(contractKey, num, num, func(tickflow.Day) bool { return true }); err != nil {
					t.Errorf("Walk(%s) 误伤：%v", num, err)
				}
			}
			// 覆盖之内、但那一刻没在交易 ⇒ 应当是 ErrClosed（一个实质答案），不是 ErrUncovered。
			day, err := cal.DayOf(contractKey, contractDays[2])
			if err != nil {
				t.Fatalf("取样日失败：%v", err)
			}
			gap := day.Sessions[0].End + 1 // 第一段刚结束的那一毫秒
			inGap := true
			for _, s := range day.Sessions {
				if s.Contains(gap) {
					inGap = false
				}
			}
			if inGap {
				_, err := cal.DayAt(contractKey, gap)
				if errors.Is(err, tickflow.ErrUncovered) {
					t.Errorf("DayAt 误伤：覆盖【之内】的休市时刻被说成答不了 —— "+
						"那一格答得了，答案是 ErrClosed：%v", err)
				}
				if !errors.Is(err, tickflow.ErrClosed) {
					t.Errorf("DayAt(覆盖内的休市时刻) 应当 ErrClosed，实得 %v", err)
				}
			}
		})
	}
}

// ───────── 契约二：cf 的夜盘时刻属于覆盖之内 ─────────
//
// ⛔ 这一格防的是一个很自然的写法：把覆盖窗口的下界写成 midnight(cf)。
// 有夜盘的品种，cf 那天的第一段挂在【上一个交易日的自然日】上，
// 于是 Sessions[0].Start 早于 midnight(cf) ——
// 按午夜算，一个真属于 cf 的夜盘时刻会被判成 ErrUncovered。
// **这是「交易日 ≠ 自然日」在时间戳侧的同一个形状。**

// contractDaysNight 的第一天【早于】内置模板生效起点，
// 所以 Covers 的 cf 不是 days[0] ⇒ cf 那天才会带上夜盘段。
var contractDaysNight = []tickflow.TradingDay{20200505, 20200506, 20200507, 20200508}

func TestCalendarContract_FirstDayNightSession_Red(t *testing.T) {
	for _, impl := range calImpls {
		t.Run(impl.name, func(t *testing.T) {
			cal := impl.newCal(t, contractDaysNight)
			cf, _, ok := cal.Covers(contractKey)
			if !ok {
				t.Fatal("Covers ok=false")
			}
			day, err := cal.DayOf(contractKey, cf)
			if err != nil {
				t.Fatalf("DayOf(cf=%s) 失败：%v", cf, err)
			}
			first := day.Sessions[0]
			// 先证明这个 fixture 真的撑起了那一格：cf 的第一段早于 cf 的午夜。
			if first.Start >= midnightCST(cf) {
				t.Fatalf("fixture 不成立：cf=%s 的第一段没有早于它的午夜"+
					"（%d >= %d）—— 那这一格什么也没测到", cf, first.Start, midnightCST(cf))
			}
			// 而那个时刻【属于 cf】，必须答得出来。
			got, err := cal.DayAt(contractKey, first.Start+60000)
			if errors.Is(err, tickflow.ErrUncovered) {
				t.Fatalf("把覆盖下界算成了 midnight(cf)：cf 的夜盘时刻被判成答不了：%v"+
					"\n（夜盘挂在上一个交易日的自然日上，下界必须取 Sessions[0].Start）", err)
			}
			if err != nil {
				t.Fatalf("cf 的夜盘时刻应当答得出来，实得 %v", err)
			}
			if got.Num != cf {
				t.Fatalf("cf 的夜盘时刻被归到了 %s，而它属于 %s", got.Num, cf)
			}
		})
	}
}

func TestCalendarContract_FirstDayNightSession_Green(t *testing.T) {
	for _, impl := range calImpls {
		t.Run(impl.name, func(t *testing.T) {
			cal := impl.newCal(t, contractDaysNight)
			cf, _, ok := cal.Covers(contractKey)
			if !ok {
				t.Fatal("Covers ok=false")
			}
			day, err := cal.DayOf(contractKey, cf)
			if err != nil {
				t.Fatalf("DayOf(cf) 失败：%v", err)
			}
			// 与 _Red 只差一件事：往【下界之前】再挪一小时 ⇒ 那里确实答不了。
			before := day.Sessions[0].Start - 3600000
			if _, err := cal.DayAt(contractKey, before); !errors.Is(err, tickflow.ErrUncovered) {
				t.Fatalf("下界之前的时刻应当 ErrUncovered，实得 %v"+
					"\n（否则下界形同虚设，_Red 那一格也就证明不了什么）", err)
			}
		})
	}
}
