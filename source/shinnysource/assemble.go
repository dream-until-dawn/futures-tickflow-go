package shinnysource

import (
	"fmt"
	"math"
	"time"

	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
)

// Assemble 把 DIFF 的 1m 行变成 tickflow.Bar。纯函数：不连网、不取时钟。
//
//	Ts          datetime（开盘时刻，纳秒）÷ 1e6 —— 必须整除，否则报错
//	TsEnd       Ts ＋ 60000，且必须不晚于 Ts 所在时段的收盘
//	TradingDay  cal.DayAt(k, Ts) —— 源的约定是「问日历」，不在这里写截止规则
//	完结        TsEnd > now 的根丢掉（上游不标完结，窗里总捎上最新那一根，probe.md 6.13 a）
//	OpenInterest close_oi · Settle NaN（分钟线没有）· Turnover NaN（DIFF 的 kline 没有这个字段）
//	Flags       FlagSrcShinny
//
// ⛔ **Volume == 0 的根照原样给**（片二评审裁 D-A 取甲）：天勤按分钟给根，不管那一分钟有没有成交
// （probe.md 6.13 c″、6.20 E2）⇒ **有根 ≠ 有交易**，判「开没开市」的人要看 Volume，不能看根在不在。
//
// ⛔ 日历放不下的根（休市时刻、跨时段收盘、落在请求区间之外的交易日、覆盖之外）⇒ **报错，不丢**：
// 丢掉的话，日历与上游的分歧就被一个更安静的丢数据藏起来了。
func Assemble(rows []Row, cal tickflow.Calendar, req tickflow.BarRequest, now int64) ([]tickflow.Bar, error) {
	k := req.Symbol.ProductKey()
	bars := make([]tickflow.Bar, 0, len(rows))
	prev := int64(-1)
	for _, row := range rows {
		if row.Datetime%1e6 != 0 {
			return nil, fmt.Errorf("shinnysource: id %d 的 datetime=%d 不是整毫秒", row.ID, row.Datetime)
		}
		ts := row.Datetime / 1e6
		if ts <= prev {
			return nil, fmt.Errorf("shinnysource: id %d 的开盘时刻 %s 不晚于上一根——没有按升序", row.ID, fmtTs(ts))
		}
		prev = ts
		tsEnd := ts + 60000
		d, err := cal.DayAt(k, ts)
		if err != nil {
			return nil, fmt.Errorf("%w：id %d 开盘于 %s，日历答：%w", ErrCalendarDisagrees, row.ID, fmtTs(ts), err)
		}
		inSession := false
		for _, s := range d.Sessions {
			if s.Contains(ts) {
				inSession = tsEnd <= s.End
				break
			}
		}
		if !inSession {
			return nil, fmt.Errorf("%w：id %d 开盘于 %s，收盘 %s 越过了它所在时段的收盘", ErrCalendarDisagrees, row.ID, fmtTs(ts), fmtTs(tsEnd))
		}
		if d.Num < req.From || d.Num > req.To {
			return nil, fmt.Errorf("%w：id %d 开盘于 %s，日历把它归到 %s，落在请求 [%s, %s] 之外",
				ErrCalendarDisagrees, row.ID, fmtTs(ts), d.Num, req.From, req.To)
		}
		if tsEnd > now {
			// 升序 ⇒ 后面的只会更晚。
			break
		}
		bars = append(bars, tickflow.Bar{
			Ts: ts, TsEnd: tsEnd, TradingDay: d.Num,
			Open: row.Open, High: row.High, Low: row.Low, Close: row.Close,
			Volume:       row.Volume,
			Turnover:     math.NaN(),
			OpenInterest: row.CloseOI,
			Settle:       math.NaN(),
			Flags:        tickflow.FlagSrcShinny,
		})
	}
	return bars, nil
}

func fmtTs(ms int64) string {
	return time.UnixMilli(ms).In(tickflow.CST).Format("2006-01-02 15:04")
}
