package adapter_test

import (
	"reflect"
	"strings"
	"testing"

	futsim "github.com/dream-until-dawn/futures-position-simulator-go"
	"github.com/dream-until-dawn/futures-position-simulator-go/fee"
	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/dream-until-dawn/futures-tickflow-go/adapter"
	"github.com/shopspring/decimal"
)

// 合成时段独立声明，不从 tickflow 日历或 adapter 输出反推期望。
// 默认空规则用于接口、时段与交易日守卫；持仓用例可注入独立合成规则。
// 涨跌停查不到必须通过 Unchecked 明示。
func simulator(t *testing.T, supplied ...refdata.Provider) *futsim.Simulator {
	t.Helper()
	session := func(h1, m1, h2, m2 int) refdata.Session {
		return refdata.Session{Start: refdata.MustClockTime(h1, m1, 0), End: refdata.MustClockTime(h2, m2, 0)}
	}
	cal, err := refdata.NewCalendar([]types.TradingDay{20260928, 20260929, 20260930}, []refdata.SessionTable{{
		Exchange: types.SHFE, Product: "au",
		Day:   []refdata.Session{session(9, 0, 10, 15), session(10, 30, 11, 30), session(13, 30, 15, 0)},
		Night: []refdata.Session{session(21, 0, 2, 30)},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	rules, err := refdata.NewBuilder(1).Build()
	if err != nil {
		t.Fatal(err)
	}
	var chosen refdata.Provider = rules
	if len(supplied) > 0 {
		chosen = supplied[0]
	}
	choices := futsim.CTPChoices()
	choices.FeeRounding = fee.NoRounding // 合成输入的显式选择，不声称真实柜台口径。
	s, err := futsim.New(futsim.Config{Day: 20260929, PreBalance: decimal.NewFromInt(100000), Rules: chosen, Choices: choices, Calendar: cal})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAdvanceAcceptsSessionEnds(t *testing.T) {
	for _, c := range []struct{ start, end string }{
		{"2026-09-29T10:14:00+08:00", "2026-09-29T10:15:00+08:00"},
		{"2026-09-29T11:29:00+08:00", "2026-09-29T11:30:00+08:00"},
		{"2026-09-29T14:59:00+08:00", "2026-09-29T15:00:00+08:00"},
		{"2026-09-29T02:29:00+08:00", "2026-09-29T02:30:00+08:00"},
		{"2026-09-28T23:59:00+08:00", "2026-09-29T00:00:00+08:00"},
	} {
		b, err := adapter.Bar(minute(t, c.start, c.end), symbol(t, "SHFE.au2612"))
		if err != nil {
			t.Fatal(err)
		}
		got, err := simulator(t).Advance(b)
		if err != nil || len(got.Filled) != 0 || len(got.Unchecked) == 0 {
			t.Errorf("%s → %s：%+v / %v", c.start, c.end, got, err)
		}
	}
}

func TestAdvanceRejectsInvalidAndKeepsAccount(t *testing.T) {
	for _, name := range []string{"跨休市", "交易日", "坏OHLC", "零时长"} {
		t.Run(name, func(t *testing.T) {
			s := simulator(t)
			b := minute(t, "2026-09-29T10:14:00+08:00", "2026-09-29T10:15:00+08:00")
			want := ""
			switch name {
			case "跨休市":
				b.TsEnd = minute(t, "2026-09-29T10:30:00+08:00", "2026-09-29T10:31:00+08:00").TsEnd
				want = "跨了两个时段"
			case "交易日":
				b.TradingDay = 20260930
				want = "交易日"
			case "坏OHLC":
				b.Close = 200
				want = "低 ≤ 收 ≤ 高"
			case "零时长":
				b.TsEnd = b.Ts
				want = "不是一段正时长"
			}
			converted, err := adapter.Bar(b, symbol(t, "SHFE.au2612"))
			if err != nil {
				t.Fatalf("业务语义由 Advance 检查：%v", err)
			}
			before := s.Account()
			if _, err = s.Advance(converted); err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("应拒绝 %s，得到 %v", name, err)
			}
			if !reflect.DeepEqual(before, s.Account()) {
				t.Fatal("失败后账户改变")
			}
		})
	}
}
