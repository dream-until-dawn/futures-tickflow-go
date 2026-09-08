package embedded

import (
	"testing"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
)

// 这两条基准存在的理由是一次【我自己造出来的性能回归】，把读数记在这儿好让人重量。
//
// 2026-09-09：给 Template / DayOf / DayAt 补上「按 Covers 分流」之后，
// 我把 Covers 放到了每一条调用路径上 —— **而 Covers 当时是线性扫全部交易日的**。
//
//	1500 个交易日的日历，-benchtime=200x
//	                     DayOf      DayAt
//	基线 c51e4d1（修覆盖之前）  198 ns     310 ns
//	补完覆盖判定（未优化）      975 ns    3322 ns    ← DayOf 约 5 倍，DayAt 约 11 倍
//	Covers 改成 O(1) 之后      199 ns     847 ns
//
// ⚠️ DayAt 仍是基线的约 2.7 倍，而**那一部分是正确性的价钱，不是浪费**：
// 覆盖窗口要知道两端那一天的时段，就得取两次 Day。
// 这里**不再往下优化**，理由是本仓那条「先问值不值」：再快也要么缓存一份
// 会过期的抄件，要么把拼时段的逻辑抄第二份 —— 两条都是本仓栽过的那一类。
//
// ⚠️ 而「Covers 能预算」靠的前提是 **Calendar 在 New 之后不可变**，
// 那个前提现在没有守卫（见 Calendar 结构体上的注释）。
//
// 跑法：go test ./calendar/embedded/ -run XXX -bench . -benchtime=200x

func benchCal(tb testing.TB) (*Calendar, tickflow.ProductKey) {
	tb.Helper()
	// 攒约 1500 个交易日 —— 接近本仓实测的 1497（2020-05-06 起至今）。
	var days []tickflow.TradingDay
	y, m, d := 2020, 5, 6
	for len(days) < 1500 {
		days = append(days, tickflow.TradingDay(y*10000+m*100+d))
		d++
		if d > 28 { // 只求条数够、升序不重复，不求它是真的交易日
			d = 1
			m++
			if m > 12 {
				m = 1
				y++
			}
		}
	}
	c, err := New(days)
	if err != nil {
		tb.Fatal(err)
	}
	return c, tickflow.ProductKey{Exchange: tickflow.SHFE, Product: "au"}
}

func BenchmarkDayOf(b *testing.B) {
	c, k := benchCal(b)
	days := c.Days()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := c.DayOf(k, days[i%len(days)]); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDayAt(b *testing.B) {
	c, k := benchCal(b)
	days := c.Days()
	day, err := c.DayOf(k, days[len(days)/2])
	if err != nil {
		b.Fatal(err)
	}
	ts := day.Sessions[len(day.Sessions)-1].Start + 1000
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := c.DayAt(k, ts); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCovers(b *testing.B) {
	c, k := benchCal(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, ok := c.Covers(k); !ok {
			b.Fatal("Covers 报 ok=false")
		}
	}
}
