package sinavendor

import (
	"context"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"

	tf "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/source/officialdaily"
)

// Source 仅读冻结原件；日历资格由调用方独立证明，不能用报价日期自证。
type Source struct {
	cal  tf.Calendar
	now  int64
	rows map[tf.Symbol]map[tf.TradingDay]Record
}

// New 整体核验原件和唯一键；不同版本必须由调用方显式选择，不能静默覆盖。
func New(cal tf.Calendar, docs []Document, now int64) (*Source, error) {
	if cal == nil || now <= 0 || len(docs) == 0 {
		return nil, fmt.Errorf("sinavendor: 缺日历/时钟/原件")
	}
	s := &Source{cal: cal, now: now, rows: map[tf.Symbol]map[tf.TradingDay]Record{}}
	for _, d := range docs {
		rr, e := Parse(d)
		if e != nil {
			return nil, e
		}
		if s.rows[d.Symbol] == nil {
			s.rows[d.Symbol] = map[tf.TradingDay]Record{}
		}
		for _, r := range rr {
			if _, ok := s.rows[d.Symbol][r.Day]; ok {
				return nil, fmt.Errorf("sinavendor: 重复合约日，须显式选版本")
			}
			s.rows[d.Symbol][r.Day] = r
		}
	}
	return s, nil
}

// Calendar 返回同一个实例，避免Syncer静默错位。
func (s *Source) Calendar() tf.Calendar { return s.cal }

// Caps 表示字段能力，绝不代表逐日完整、官方日历或财务准入。
func (s *Source) Caps(k tf.ProductKey) tf.Capabilities {
	var first tf.TradingDay
	for sym, rows := range s.rows {
		if sym.ProductKey() != k {
			continue
		}
		for d := range rows {
			if d < 20210101 {
				continue
			}
			if first == 0 || d < first {
				first = d
			}
		}
	}
	if first == 0 {
		return tf.Capabilities{}
	}
	return tf.Capabilities{Periods: []tf.Period{tf.Daily}, Since: map[tf.Period]tf.TradingDay{tf.Daily: first}, BatchDays: 1, ClientUse: tf.ClientUseNone, HasSettle: true, HasOI: true}
}

// Records 返回防御副本；更早返回行保留原件边界，但不新增2021年前补采。
func (s *Source) Records(sym tf.Symbol) []Record {
	out := make([]Record, 0, len(s.rows[sym]))
	for _, r := range s.rows[sym] {
		q := r
		q.Fields = map[string]string{}
		for k, v := range r.Fields {
			q.Fields[k] = v
		}
		out = append(out, q)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Day < out[j].Day })
	return out
}

// numeric 复用严格数值语法，而不继承官方来源身份；量仓用精确有理数防小数被float舍入。
func numeric(k, raw string) (float64, error) {
	if len(raw) > 128 {
		return 0, fmt.Errorf("sinavendor: 数字文本过长")
	}
	if i := strings.IndexAny(raw, "eE"); i >= 0 {
		exp, e := strconv.Atoi(raw[i+1:])
		if e != nil || exp < -128 || exp > 128 {
			return 0, fmt.Errorf("sinavendor: 指数超出有界解析范围")
		}
	}
	v, e := officialdaily.Number(raw)
	if e != nil {
		return 0, e
	}
	if k == "v" || k == "p" {
		r, ok := new(big.Rat).SetString(raw)
		if math.IsNaN(v) || !ok || !r.IsInt() || v > 9007199254740991 {
			return 0, fmt.Errorf("sinavendor: 量仓非精确非负整数")
		}
	}
	return v, nil
}

// Bars 在外部日历上逐日核对，缺日/坏值/未收盘整次失败，绝不把供应商缺失改成休市。
func (s *Source) Bars(ctx context.Context, req tf.BarRequest) ([]tf.Bar, error) {
	if e := req.Validate(); e != nil {
		return nil, e
	}
	if req.Period != tf.Daily || req.From < 20210101 {
		return nil, fmt.Errorf("sinavendor: 本路径仅2021起日线")
	}
	var out []tf.Bar
	var failure error
	e := s.cal.Walk(req.Symbol.ProductKey(), req.From, req.To, func(d tf.Day) bool {
		if failure = ctx.Err(); failure != nil {
			return false
		}
		if len(d.Sessions) == 0 {
			failure = fmt.Errorf("sinavendor: 日历无时段")
			return false
		}
		r, ok := s.rows[req.Symbol][d.Num]
		if !ok {
			failure = fmt.Errorf("sinavendor: 未取得 %s %s，拒绝空覆盖", req.Symbol, d.Num)
			return false
		}
		end := d.Sessions[len(d.Sessions)-1].End
		if end > s.now {
			failure = fmt.Errorf("sinavendor: 未完结日")
			return false
		}
		v := map[string]float64{}
		for _, k := range []string{"o", "h", "l", "c", "v", "p", "s"} {
			v[k], failure = numeric(k, r.Fields[k])
			if failure != nil {
				return false
			}
		}
		o, h, l, c := v["o"], v["h"], v["l"], v["c"]
		if math.IsNaN(o) || math.IsNaN(h) || math.IsNaN(l) || math.IsNaN(c) || o <= 0 || l <= 0 || h < math.Max(o, c) || l > math.Min(o, c) || l > h {
			failure = fmt.Errorf("sinavendor: OHLC隔离，原件%s行%d", r.Evidence.SHA256, r.Row)
			return false
		}
		settle := v["s"]
		if settle == 0 {
			settle = math.NaN()
		}
		out = append(out, tf.Bar{Ts: d.Sessions[0].Start, TsEnd: end, TradingDay: d.Num, Open: o, High: h, Low: l, Close: c, Volume: v["v"], OpenInterest: v["p"], Settle: settle, Turnover: math.NaN(), Flags: tf.FlagSrcSina})
		return true
	})
	if e != nil {
		return nil, e
	}
	if failure != nil {
		return nil, failure
	}
	if e = tf.CheckBars(req, out, s.now); e != nil {
		return nil, e
	}
	return out, nil
}

var _ tf.Source = (*Source)(nil)
var _ tf.CalendarHolder = (*Source)(nil)
