package officialdaily

import (
	"context"
	"fmt"
	"math"
	"sort"

	tf "github.com/dream-until-dawn/futures-tickflow-go"
)

// Resolver 由有据历史catalog提供郑商所原码消歧；禁止最近十年算法。
type Resolver interface {
	Resolve(exchange, native string, day tf.TradingDay) (tf.Symbol, error)
}

// Source 仅消费冻结本地原件；网络增量应先经archive/batch取得并审核原件。
type Source struct {
	cal  tf.Calendar
	now  int64
	rows map[tf.Symbol]map[tf.TradingDay]Record
}

// New 校验所有输入原件与唯一键；now显式给定，确保冻结结果可重现。
func New(cal tf.Calendar, documents []Document, resolver Resolver, now int64) (*Source, error) {
	if cal == nil || now <= 0 || len(documents) == 0 {
		return nil, fmt.Errorf("officialdaily: 缺日历/时钟/原件")
	}
	s := &Source{cal: cal, now: now, rows: make(map[tf.Symbol]map[tf.TradingDay]Record)}
	for _, d := range documents {
		records, e := Parse(d)
		if e != nil {
			return nil, e
		}
		for _, r := range records {
			var sym tf.Symbol
			if r.Exchange == tf.CZCE {
				if resolver == nil {
					return nil, fmt.Errorf("officialdaily: 郑商所缺有据原码目录")
				}
				sym, e = resolver.Resolve(r.Exchange, r.Native, r.Day)
				if e != nil {
					return nil, e
				}
				if sym.Exchange != r.Exchange || sym.Instrument() != r.Native {
					return nil, fmt.Errorf("officialdaily: 目录返回不相干合约")
				}
			} else {
				sym, e = tf.ParseSymbol(r.Exchange + "." + r.Native)
				if e != nil {
					return nil, e
				}
			}
			if s.rows[sym] == nil {
				s.rows[sym] = make(map[tf.TradingDay]Record)
			}
			if _, ok := s.rows[sym][r.Day]; ok {
				return nil, fmt.Errorf("officialdaily: 多原件重复合约日 %s %s", sym, r.Day)
			}
			s.rows[sym][r.Day] = r
		}
	}
	return s, nil
}

// Calendar 将同一日历实例交还Syncer，避免两张日历导致静默错位。
func (s *Source) Calendar() tf.Calendar { return s.cal }

// Caps 明示本地源不使用HTTP；结算字段存在不保证逐条有效。
func (s *Source) Caps(k tf.ProductKey) tf.Capabilities {
	var from tf.TradingDay
	found := false
	for sym, rows := range s.rows {
		if sym.ProductKey() != k {
			continue
		}
		for d := range rows {
			if !found || d < from {
				from = d
				found = true
			}
		}
	}
	if !found {
		return tf.Capabilities{}
	}
	return tf.Capabilities{Periods: []tf.Period{tf.Daily}, Since: map[tf.Period]tf.TradingDay{tf.Daily: from}, BatchDays: 1, ClientUse: tf.ClientUseNone, HasSettle: true, HasOI: true}
}

// Records 返回原始十进制附件的副本，连同文档SHA、业务日期、原行号。
func (s *Source) Records(sym tf.Symbol) []Record {
	out := make([]Record, 0, len(s.rows[sym]))
	for _, r := range s.rows[sym] {
		copyR := r
		if r.Artifact != nil {
			a := *r.Artifact
			copyR.Artifact = &a
		}
		copyR.Fields = map[string]string{}
		for k, v := range r.Fields {
			copyR.Fields[k] = v
		}
		out = append(out, copyR)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Day < out[j].Day })
	return out
}

// Bars 对请求区间逐个交易日查记录，缺失/异常整次失败，不让Syncer误记为空覆盖。
// 合约生命周期外的日期应由调用方按catalog先收窄；源不猜测合法缺失原因。
func (s *Source) Bars(ctx context.Context, req tf.BarRequest) ([]tf.Bar, error) {
	if e := req.Validate(); e != nil {
		return nil, e
	}
	if req.Period != tf.Daily {
		return nil, fmt.Errorf("officialdaily: 仅日线")
	}
	var out []tf.Bar
	var failure error
	err := s.cal.Walk(req.Symbol.ProductKey(), req.From, req.To, func(d tf.Day) bool {
		if failure = ctx.Err(); failure != nil {
			return false
		}
		if len(d.Sessions) == 0 {
			failure = fmt.Errorf("officialdaily: 日历交易日无时段")
			return false
		}
		r, ok := s.rows[req.Symbol][d.Num]
		if !ok {
			failure = fmt.Errorf("officialdaily: 未取得 %s %s，拒绝记为空覆盖", req.Symbol, d.Num)
			return false
		}
		end := d.Sessions[len(d.Sessions)-1].End
		if end > s.now {
			failure = fmt.Errorf("officialdaily: 未完结交易日 %s", d.Num)
			return false
		}
		v := map[string]float64{}
		for k, raw := range r.Fields {
			v[k], failure = Number(raw)
			if failure != nil {
				return false
			}
		}
		// 零OHLC和关系异常均隔离，不把结算价填成收盘，不关闭上层CheckBars。
		o, h, l, c := v["open"], v["high"], v["low"], v["close"]
		if math.IsNaN(o) || math.IsNaN(h) || math.IsNaN(l) || math.IsNaN(c) || o <= 0 || l <= 0 || h < math.Max(o, c) || l > math.Min(o, c) || l > h {
			failure = fmt.Errorf("officialdaily: %s %s OHLC隔离，原件%s行%d", req.Symbol, d.Num, r.Evidence.SHA256, r.Row)
			return false
		}
		turnover := math.NaN()
		if r.TurnoverUnit == "yuan" {
			turnover = v["turnover"]
		} else if r.TurnoverUnit == "wanyuan" {
			turnover = v["turnover"] * 10000
		}
		settle := v["settle"]
		if settle == 0 {
			settle = math.NaN()
		}
		out = append(out, tf.Bar{Ts: d.Sessions[0].Start, TsEnd: end, TradingDay: d.Num, Open: o, High: h, Low: l, Close: c, Volume: v["volume"], OpenInterest: v["open_interest"], Turnover: turnover, Settle: settle, Flags: tf.FlagSrcExchange})
		return true
	})
	if err != nil {
		return nil, err
	}
	if failure != nil {
		return nil, failure
	}
	if err = tf.CheckBars(req, out, s.now); err != nil {
		return nil, err
	}
	return out, nil
}

var _ tf.Source = (*Source)(nil)
var _ tf.CalendarHolder = (*Source)(nil)
