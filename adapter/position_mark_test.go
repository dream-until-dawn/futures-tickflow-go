package adapter_test

import (
	"reflect"
	"testing"

	futsim "github.com/dream-until-dawn/futures-position-simulator-go"
	"github.com/dream-until-dawn/futures-position-simulator-go/match"
	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/dream-until-dawn/futures-tickflow-go/adapter"
	"github.com/shopspring/decimal"
)

// TestAdvanceMarksSyntheticPositions 钉住真实持仓经转换后以 Close 计价。
// 价格和规则均为合成值；直接录入成交不代表报单、撮合路径通过。
func TestAdvanceMarksSyntheticPositions(t *testing.T) {
	inst, err := types.ParseSymbol("SHFE.au2612", 20260929)
	if err != nil {
		t.Fatal(err)
	}
	d := decimal.RequireFromString
	rules, err := refdata.NewBuilder(1).
		AddInstrument(refdata.Instrument{ID: inst, VolumeMultiple: d("1000"), PriceTick: d("0.001"), PositionDateType: refdata.UseHistory, IsTrading: true}).
		AddCommissionRates(inst, types.Speculation, refdata.CommissionRates{OpenByVolume: d("1"), CloseByVolume: d("1"), CloseTodayByVolume: d("1")}).
		AddMarginRates(inst, types.Speculation, refdata.MarginRates{LongByMoney: d("0.1"), ShortByMoney: d("0.1")}).Build()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		direction types.Direction
		profit    string
	}{{"long", types.Buy, "2250"}, {"short", types.Sell, "-2250"}} {
		t.Run(tc.name, func(t *testing.T) {
			s := simulator(t, rules)
			if err := s.Mark(20260929, futsim.Quote{Instrument: inst, Last: d("100"), HasLast: true, PreSettlement: d("100"), HasPreSettlement: true}); err != nil {
				t.Fatal(err)
			}
			if err := s.ApplyTrade(20260929, match.Trade{Instrument: inst, Direction: tc.direction, Offset: types.Open, Hedge: types.Speculation, Price: d("100"), Volume: 2}); err != nil {
				t.Fatal(err)
			}
			b := minute(t, "2026-09-29T10:14:00+08:00", "2026-09-29T10:15:00+08:00")
			converted, err := adapter.Bar(b, symbol(t, "SHFE.au2612"))
			if err != nil {
				t.Fatal(err)
			}
			got, err := s.Advance(converted)
			if err != nil {
				t.Fatal(err)
			}
			// 多：(101.125-100)*1000*2=2250；空相反。期望不从转换结果回算。
			if !s.Account().PositionProfit.Equal(d(tc.profit)) || len(got.Filled) != 0 {
				t.Fatalf("盈亏 %s / Filled %d，应 %s / 0", s.Account().PositionProfit, len(got.Filled), tc.profit)
			}
			before := s.Account()
			b.Close = 200 // 超出 High，应被下游业务守卫拒绝，已有盈亏不得被改写。
			bad, err := adapter.Bar(b, symbol(t, "SHFE.au2612"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.Advance(bad); err == nil {
				t.Fatal("非法根被接受")
			}
			if !reflect.DeepEqual(before, s.Account()) {
				t.Fatal("失败根改变了已有持仓的账户")
			}
		})
	}
}
