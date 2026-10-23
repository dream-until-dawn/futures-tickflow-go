package adapter_test

import (
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	futsim "github.com/dream-until-dawn/futures-position-simulator-go"
	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/adapter"
)

func minute(t *testing.T, start, end string) tickflow.Bar {
	t.Helper()
	parse := func(s string) int64 {
		v, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			t.Fatal(err)
		}
		return v.UnixMilli()
	}
	return tickflow.Bar{Ts: parse(start), TsEnd: parse(end), TradingDay: 20260929, High: 102.5, Low: 99.25, Close: 101.125}
}

func symbol(t *testing.T, s string) tickflow.Symbol {
	t.Helper()
	v, err := tickflow.ParseSymbol(s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// 独立文本期望同时核规范键与线格式，不能拿被测映射生成期望。
func TestBarSymbols(t *testing.T) {
	b := minute(t, "2026-09-29T10:14:00+08:00", "2026-09-29T10:15:00+08:00")
	for _, c := range []struct{ canonical, native string }{
		{"SHFE.rb2701", "SHFE.rb2701"}, {"DCE.m2701", "DCE.m2701"},
		{"CZCE.TA2701", "CZCE.TA701"}, {"CFFEX.IF2610", "CFFEX.IF2610"},
		{"INE.sc2611", "INE.sc2611"}, {"GFEX.si2701", "GFEX.si2701"},
	} {
		got, err := adapter.Bar(b, symbol(t, c.canonical))
		if err != nil {
			t.Fatal(err)
		}
		if got.Instrument.Canonical() != c.canonical || got.Instrument.Native() != c.native {
			t.Fatalf("合约映射错误：%+v", got.Instrument)
		}
	}
}

func TestBarTimeAndPrices(t *testing.T) {
	b := minute(t, "2026-09-29T00:59:00.123+08:00", "2026-09-29T01:00:00.456+08:00")
	b.TradingDay = 20261008 // 单测刻意与自然日不同：adapter 不能自己重算交易日。
	b.Flags = tickflow.FlagPartial
	b.Open, b.Settle, b.Turnover = math.NaN(), math.NaN(), math.NaN()
	got, err := adapter.Bar(b, symbol(t, "SHFE.au2612"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Start.Format(time.RFC3339Nano) != "2026-09-29T00:59:00.123+08:00" || got.End.Format(time.RFC3339Nano) != "2026-09-29T01:00:00.456+08:00" || got.TradingDay != 20261008 {
		t.Fatalf("时间或交易日改变：%+v", got)
	}
	if got.High.String() != "102.5" || got.Low.String() != "99.25" || got.Close.String() != "101.125" {
		t.Fatalf("高低收字段错配：%+v", got)
	}
}

// probe.md 十仅保留了这 16 个明确数值，原 29 项一次性脚本未入仓，不能补造其余样本。
// 108.750 的期望规范化为 108.75：decimal.String 不保留无意义的末尾零。
func TestBarRecordedPrices(t *testing.T) {
	for _, want := range strings.Fields("100.005 101.235 99.995 108.75 512.34 512.36 1024.98 899.02 3856.2 3856.4 4012.8 2999.6 0.1 0.2 0.3 2.675") {
		f, err := strconv.ParseFloat(want, 64)
		if err != nil {
			t.Fatal(err)
		}
		got, err := adapter.Bar(tickflow.Bar{High: f, Low: f, Close: f}, symbol(t, "SHFE.rb2701"))
		if err != nil {
			t.Fatal(err)
		}
		if got.High.String() != want || got.Low.String() != want || got.Close.String() != want {
			t.Errorf("%s 转换错误：%+v", want, got)
		}
	}
	a, b := 0.1, 0.2 // 运行时相加，不能写编译期精确常量 0.1+0.2。
	got, err := adapter.Bar(tickflow.Bar{High: a + b, Low: a + b, Close: a + b}, symbol(t, "SHFE.rb2701"))
	if err != nil || got.Close.String() != "0.30000000000000004" {
		t.Fatalf("应保留计算长尾：%+v / %v", got, err)
	}
}

func TestBarRejectsNonFinite(t *testing.T) {
	for _, field := range []string{"High", "Low", "Close"} {
		for _, bad := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
			b := tickflow.Bar{High: 102, Low: 99, Close: 100}
			switch field {
			case "High":
				b.High = bad
			case "Low":
				b.Low = bad
			case "Close":
				b.Close = bad
			}
			got, err := adapter.Bar(b, symbol(t, "SHFE.rb2701"))
			if err == nil || !strings.Contains(err.Error(), field) || !reflect.DeepEqual(got, futsim.Bar{}) {
				t.Fatalf("%s=%v 应返回点名错误与零结果，得到 %+v / %v", field, bad, got, err)
			}
		}
	}
}

func TestBarRejectsInvalidSymbol(t *testing.T) {
	for _, s := range []tickflow.Symbol{{}, {Exchange: "UNKNOWN", Product: "rb", YearMon: 2701}, {Exchange: "SHFE", Product: "rb", YearMon: 2713}, {Exchange: "SHFE", Product: "rb", YearMon: 10001}, {Exchange: "SHFE", Product: "RB", YearMon: 2701}} {
		if _, err := adapter.Bar(tickflow.Bar{}, s); err == nil {
			t.Errorf("非法或非规范合约被接受：%+v", s)
		}
	}
}
