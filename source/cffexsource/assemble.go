package cffexsource

import (
	"errors"
	"fmt"
	"math"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
)

// 这一片是【组装】：一天的 XML 行 ＋ 那一天的日历 → 一根 tickflow.Bar。
//
// ⛔ **本源与 sinasource 的形状是反的，这决定了接口长什么样：**
//
//	sinasource   一次请求 = 【一个合约】的【全部历史】 ⇒ 一次就覆盖整个区间
//	cffexsource  一次请求 = 【一天】的【全部合约】     ⇒ 要 N 天就得请求 N 次
//
// ⇒ 所以这里的单位是**一天一根**；把区间铺开成一串交易日是拉取层的活，
// 而铺开它的正确工具是 `Calendar.Walk`（它对区间落在覆盖之外会当场炸，不静默少遍历）。

// ErrNotCFFEX 这个源只覆盖中金所。
//
// ⛔ 单列出来不是洁癖，它**支撑着下面那个 Instrument() 的用法**：
//
//	CFFEX  Instrument() 给 "IC2609" —— **正是 XML 里那个形态** ✅
//	CZCE   Instrument() 给 "TA701"  —— 三位年月，别处会出事（sinasource 就栽过）
//
// ⇒ 因为本源只接受 CFFEX，`Instrument()` 在这里才是安全的。
// **把这条守住，那个用法就不会哪天被一个非中金所的 Symbol 悄悄带偏。**
var ErrNotCFFEX = errors.New("cffexsource: 这个源只覆盖中金所（CFFEX）")

// ErrTradingDayMismatch XML 自报的交易日与日历给的那一天不一致。
//
// ⚠️ 这份 XML **自带 `<tradingday>`** —— 那是 sinasource 没有的一手证据。
// 两边对不上时，可能是取错了日期的文件，也可能是日历注入错了；
// **两种都要人看，所以不吞。**
var ErrTradingDayMismatch = errors.New("cffexsource: XML 自报的交易日与日历给的那一天不一致")

// AssembleDay 把一天的行配上那一天的日历，组装出【一根】日线。
//
// found 为 false 表示**那一天的文件里没有这个合约**。它可能是：
// 合约还没上市、已经到期、或者那天它确实没有数据。
//
//	⛔ **本层分不出这三者** —— 与 sinasource 那边的「丙格」同族（登记⑨）：
//	  「日历说是交易日，而数据里没有这一行」在输出上是一个【缺席】，
//	  而缺席也可能是别的原因。**处置在 Syncer 那一层，本层只如实报 found=false。**
//
// now 用来判完结，**由调用方给，不在内部取 time.Now()**（同 CheckBars 那条理由）。
func AssembleDay(rows []SettleRow, day tickflow.Day, sym tickflow.Symbol, now int64) (tickflow.Bar, bool, error) {
	var zero tickflow.Bar
	if sym.Exchange != tickflow.CFFEX {
		return zero, false, fmt.Errorf("%w：收到 %s", ErrNotCFFEX, sym)
	}
	if sym.Product == "" || sym.YearMon == 0 {
		return zero, false, fmt.Errorf("cffexsource: Symbol 不完整（%+v）——"+
			"主力连续在这里同样不成立：这份 XML 里只有具体合约", sym)
	}
	if !day.Num.Valid() {
		return zero, false, fmt.Errorf("cffexsource: 交易日 %d 不合法", int32(day.Num))
	}
	if len(day.Sessions) == 0 {
		return zero, false, fmt.Errorf("cffexsource: %s 那天一个时段都没有——"+
			"Ts/TsEnd 无从取值，而留零值上层只能猜", day.Num)
	}

	// CFFEX 上 Instrument() 就是 XML 里那个形态（由 ErrNotCFFEX 守着这个前提）。
	want := sym.Instrument()
	var row *SettleRow
	for i := range rows {
		if rows[i].InstrumentID == want {
			row = &rows[i]
			break
		}
	}
	if row == nil {
		return zero, false, nil // 缺席：见函数注释，本层不判它是哪一种
	}

	// 一手交叉核对：XML 自报的交易日 vs 日历给的那一天。
	if row.TradingDay != "" &&
		normalizeDay(row.TradingDay) != normalizeDay(day.Num.String()) {
		return zero, false, fmt.Errorf("%w：%s 自报 %q，而日历给的是 %s",
			ErrTradingDayMismatch, want, row.TradingDay, day.Num)
	}

	ts := day.Sessions[0].Start
	tsEnd := day.Sessions[len(day.Sessions)-1].End
	if tsEnd > now {
		return zero, false, nil // 未完结：整根丢掉，未完结的绝不进入本库任何一层
	}

	return tickflow.Bar{
		Ts:           ts,
		TsEnd:        tsEnd,
		TradingDay:   day.Num,
		Open:         row.Open,
		High:         row.High,
		Low:          row.Low,
		Close:        row.Close,
		Volume:       row.Volume,
		Turnover:     row.Turnover,
		OpenInterest: row.OpenInterest,
		Settle:       settleOrNaN(row.Settle),
		Flags:        tickflow.FlagSrcExchange,
	}, true, nil
}

// settleOrNaN 把 0 映射成 NaN。
//
// ⛔ **这个判断在这一层做，而不是在 parse.go** —— 那里明确写着不做，理由是分层：
// 解析层只报告「XML 里写的是什么」，而**「0 该不该当成缺失」需要语境**。
// 这里就是那个语境：`bar.go` 说得很清楚 ——
// **没有任何品种的结算价会是零，0 只可能是缺失的伪装。**
//
// ⚠️ 而本层**仍然分不出「XML 里写了 0」与「XML 里那一格是空白」**（登记⑳）：
// 解析层已经把两者抹成同一个 0 了。**这一层只是给这个 0 一个正确的下游语义，
// 而不是把已经丢掉的信息找回来。**
func settleOrNaN(v float64) float64 {
	if v == 0 {
		return math.NaN()
	}
	return v
}

// normalizeDay 把 "20260908" 与 "2026-09-08" 归到同一形态。
//
// XML 实测给的是 `20260908`，而 `TradingDay.String()` 给的是 `2026-09-08` ——
// **两个都在本仓里，而它们不能直接比。**
func normalizeDay(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			out = append(out, s[i])
		}
	}
	return string(out)
}
