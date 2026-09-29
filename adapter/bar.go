// Package adapter 连接行情根与期货记账内核；金额计算仍由记账内核负责。
package adapter

import (
	"fmt"
	"math"
	"time"

	futsim "github.com/dream-until-dawn/futures-position-simulator-go"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/shopspring/decimal"
)

// Bar 将一根未复权行情转换为记账内核的盘中输入。
//
// 只检查转换前提：规范合约与有限的 High/Low/Close。时段、交易日、
// 价格关系由 Advance 校验；跨休市聚合根和日线可能被它拒绝。
// FlagPartial 不拦截，是否接受由调用方决定。Open、Settle 等未消费字段
// 可以缺失；不触发结算、不计算金额，也无法识别调用方误传的复权价格。
func Bar(b tickflow.Bar, sym tickflow.Symbol) (futsim.Bar, error) {
	// Symbol 可由结构体直接构造，不能假定调用方经过了解析器。
	canonical, err := tickflow.ParseSymbol(sym.String())
	if err != nil || canonical != sym {
		return futsim.Bar{}, fmt.Errorf("adapter: 非法或非规范合约 %q", sym.String())
	}
	for _, field := range []struct {
		name  string
		value float64
	}{
		{"High", b.High}, {"Low", b.Low}, {"Close", b.Close},
	} {
		if math.IsNaN(field.value) || math.IsInf(field.value, 0) {
			return futsim.Bar{}, fmt.Errorf("adapter: %s 必须是有限价格", field.name)
		}
	}
	year, month := sym.Expiry()
	return futsim.Bar{
		Instrument: types.InstrumentID{Exchange: types.Exchange(sym.Exchange), Product: sym.Product, Year: year, Month: month},
		TradingDay: types.TradingDay(b.TradingDay),
		Start:      time.UnixMilli(b.Ts).In(tickflow.CST),
		End:        time.UnixMilli(b.TsEnd).In(tickflow.CST),
		High:       decimal.NewFromFloat(b.High),
		Low:        decimal.NewFromFloat(b.Low),
		Close:      decimal.NewFromFloat(b.Close),
	}, nil
}
