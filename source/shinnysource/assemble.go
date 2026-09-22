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
//	完结        收「TsEnd ≤ now 且（id＋1 已在这一窗里，或 now ≥ TsEnd ＋ CloseGrace）」的根；第一根不收的就停
//	            （上游不标完结，窗里总捎上最新那一根，probe.md 6.13 a；宽限见 CloseGrace —— v0.10 P-e，L10）
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
	have := make(map[int64]bool, len(rows))
	for _, r := range rows {
		have[r.ID] = true
	}
	grace := CloseGrace.Milliseconds()
	prev := int64(-1)
	for _, row := range rows {
		if row.Datetime%1e6 == 0 && row.Datetime/1e6 <= prev {
			return nil, fmt.Errorf("shinnysource: id %d 的开盘时刻 %s 不晚于上一根——没有按升序", row.ID, fmtTs(row.Datetime/1e6))
		}
		b, err := rowBar(row, cal, k)
		if err != nil {
			return nil, err
		}
		prev = b.Ts
		if b.TradingDay < req.From || b.TradingDay > req.To {
			return nil, fmt.Errorf("%w：id %d 开盘于 %s，日历把它归到 %s，落在请求 [%s, %s] 之外",
				ErrCalendarDisagrees, row.ID, fmtTs(b.Ts), b.TradingDay, req.From, req.To)
		}
		if b.TsEnd > now {
			// 升序 ⇒ 后面的只会更晚。
			break
		}
		if !have[row.ID+1] && now < b.TsEnd+grace {
			// L10：下一根还没出现、收盘也还没过宽限 ⇒ 这一根可能还在变（6.36 / 6.37 的 A1：收盘后几十毫秒内还有改动）。
			// 不收，等下一次 Sync；与旧规则一样在第一根不收的地方停。
			break
		}
		bars = append(bars, b)
	}
	return bars, nil
}

// rowBar 把一行 1m 变成 Bar（整毫秒 · 问日历归日 · 整根落在一个时段里 · 字段取法），不看请求区间、不看完结。
// Assemble（历史拉取）与 Live（实时推送）共用 —— 两条路的换算是同一份代码（v0.10 P-b1 从 Assemble 里抽出，行为不变）。
func rowBar(row Row, cal tickflow.Calendar, k tickflow.ProductKey) (tickflow.Bar, error) {
	if row.Datetime%1e6 != 0 {
		return tickflow.Bar{}, fmt.Errorf("shinnysource: id %d 的 datetime=%d 不是整毫秒", row.ID, row.Datetime)
	}
	ts := row.Datetime / 1e6
	tsEnd := ts + 60000
	d, err := cal.DayAt(k, ts)
	if err != nil {
		return tickflow.Bar{}, fmt.Errorf("%w：id %d 开盘于 %s，日历答：%w", ErrCalendarDisagrees, row.ID, fmtTs(ts), err)
	}
	inSession := false
	for _, s := range d.Sessions {
		if s.Contains(ts) {
			inSession = tsEnd <= s.End
			break
		}
	}
	if !inSession {
		return tickflow.Bar{}, fmt.Errorf("%w：id %d 开盘于 %s，收盘 %s 越过了它所在时段的收盘", ErrCalendarDisagrees, row.ID, fmtTs(ts), fmtTs(tsEnd))
	}
	return tickflow.Bar{
		Ts: ts, TsEnd: tsEnd, TradingDay: d.Num,
		Open: row.Open, High: row.High, Low: row.Low, Close: row.Close,
		Volume:       row.Volume,
		Turnover:     math.NaN(),
		OpenInterest: row.CloseOI,
		Settle:       math.NaN(),
		Flags:        tickflow.FlagSrcShinny,
	}, nil
}

// CloseGrace 是历史拉取与实时推送共用的「收盘宽限」G：一根的下一根还没出现时，本机时刻过了收盘 ＋ G 才算完结。
// 4 秒 —— probe.md 6.37（10:15 · 11:30 · 15:00 三处末根同值）。LiveOptions.G 的默认值也是它。
//
// ⚠️ 在 Assemble 里它只收紧、不放松：「TsEnd ≤ now」照旧是必要条件（CheckBars 的契约：未完结的不进库），
// 宽限是在它之上再加的一道（probe.md 6.40 一）。
const CloseGrace = 4 * time.Second

func fmtTs(ms int64) string {
	return time.UnixMilli(ms).In(tickflow.CST).Format("2006-01-02 15:04")
}
