// Package verified 提供证据驱动的逐日日历，不将当前时段回填历史。
// 记录必须逐自然日连续；实际时段使用绝对时间，跨周末和节假日夜盘由资料明确给出。
package verified

import (
	"fmt"
	"sort"
	"time"

	tf "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/refdata/evidence"
)

// Record 表达一个已核自然日期；空Sessions是已核休市，缺少Record是未知。
// Evidence应指向日历/时段制度原件，不应只指向同源行情。
type Record struct {
	Day      tf.TradingDay      `json:"day"`
	Sessions []tf.Session       `json:"sessions"`
	Template tf.SessionTemplate `json:"template"`
	Evidence []evidence.Ref     `json:"evidence"`
}

// Series 是一个品种的连续、闭区间资料。上市前区间应排除，不暗示已交易。
type Series struct {
	Product tf.ProductKey `json:"product"`
	Records []Record      `json:"records"`
}

// Calendar 保持私有副本；调用方修改入参或返回切片不会改变已冻结日历。
type Calendar struct{ data map[tf.ProductKey][]Record }

// Date 严格校验公历日期；TradingDay.Valid本身只做粗筛。
func Date(d tf.TradingDay) (time.Time, error) {
	t, e := time.Parse("20060102", fmt.Sprintf("%08d", d))
	if e != nil || !d.Valid() {
		return time.Time{}, fmt.Errorf("verified: 非真实日期 %d", d)
	}
	return t, nil
}

func clone(r Record) Record {
	r.Sessions = append([]tf.Session(nil), r.Sessions...)
	r.Template.Day = append([]tf.Session(nil), r.Template.Day...)
	r.Template.Night = append([]tf.Session(nil), r.Template.Night...)
	r.Evidence = append([]evidence.Ref(nil), r.Evidence...)
	return r
}

func sessions(ss []tf.Session, relative bool) error {
	for i, s := range ss {
		if s.End <= s.Start || s.Start%60000 != 0 || s.End%60000 != 0 || (i > 0 && ss[i-1].End > s.Start) || (relative && (s.Start < 0 || s.End > 48*3600000)) {
			return fmt.Errorf("verified: 无效或重叠时段")
		}
	}
	return nil
}

// New 拒绝空证据、缺日、重叠时段及模板未生效；不从排序或默认值修复输入。
func New(all []Series) (*Calendar, error) {
	c := &Calendar{data: make(map[tf.ProductKey][]Record)}
	if len(all) == 0 {
		return nil, fmt.Errorf("verified: 空日历")
	}
	for _, s := range all {
		if s.Product.Exchange == "" || s.Product.Product == "" || len(s.Records) == 0 {
			return nil, fmt.Errorf("verified: 空品种/记录")
		}
		if _, ok := c.data[s.Product]; ok {
			return nil, fmt.Errorf("verified: 重复品种")
		}
		rr := make([]Record, len(s.Records))
		var previous time.Time
		var lastEnd int64
		for i, r := range s.Records {
			date, err := Date(r.Day)
			if err != nil {
				return nil, err
			}
			if i > 0 && !previous.AddDate(0, 0, 1).Equal(date) {
				return nil, fmt.Errorf("verified: %s 有缺日/乱序", s.Product)
			}
			previous = date
			if len(r.Evidence) == 0 {
				return nil, fmt.Errorf("verified: 缺少制度证据")
			}
			for _, e := range r.Evidence {
				if err := e.Validate(); err != nil {
					return nil, err
				}
			}
			if !r.Template.Covers(r.Day) || len(r.Template.Day) == 0 {
				return nil, fmt.Errorf("verified: 模板覆盖不到日期")
			}
			if _, err = Date(r.Template.From); err != nil {
				return nil, err
			}
			if r.Template.To != 0 {
				if _, err = Date(r.Template.To); err != nil || r.Template.To < r.Template.From {
					return nil, fmt.Errorf("verified: 模板终点非法")
				}
			}
			if err = sessions(r.Sessions, false); err != nil {
				return nil, err
			}
			if err = sessions(r.Template.Day, true); err != nil {
				return nil, err
			}
			if err = sessions(r.Template.Night, true); err != nil {
				return nil, err
			}
			if len(r.Sessions) > 0 {
				end := r.Sessions[len(r.Sessions)-1].End
				local := time.UnixMilli(end - 1).In(time.FixedZone("CST", 8*3600))
				if local.Format("20060102") != date.Format("20060102") || (lastEnd != 0 && r.Sessions[0].Start < lastEnd) {
					return nil, fmt.Errorf("verified: 实际时段日期不符/跨交易日重叠")
				}
				lastEnd = end
			}
			rr[i] = clone(r)
		}
		c.data[s.Product] = rr
	}
	return c, nil
}

func (c *Calendar) record(k tf.ProductKey, d tf.TradingDay) (Record, error) {
	rr := c.data[k]
	i := sort.Search(len(rr), func(i int) bool { return rr[i].Day >= d })
	if i == len(rr) || rr[i].Day != d {
		return Record{}, tf.ErrUncovered
	}
	return clone(rr[i]), nil
}

// DayOf 区分已核休市和资料未知。
func (c *Calendar) DayOf(k tf.ProductKey, d tf.TradingDay) (tf.Day, error) {
	r, e := c.record(k, d)
	if e != nil {
		return tf.Day{}, e
	}
	if len(r.Sessions) == 0 {
		return tf.Day{}, tf.ErrNotTradingDay
	}
	return tf.Day{Num: d, Sessions: r.Sessions}, nil
}

// Template 返回该日明确记录的历史模板。
func (c *Calendar) Template(k tf.ProductKey, d tf.TradingDay) (tf.SessionTemplate, error) {
	r, e := c.record(k, d)
	return r.Template, e
}

// Covers 返回逐日连续资料的边界，不把资料孔洞包进区间。
func (c *Calendar) Covers(k tf.ProductKey) (tf.TradingDay, tf.TradingDay, bool) {
	r := c.data[k]
	if len(r) == 0 {
		return 0, 0, false
	}
	return r[0].Day, r[len(r)-1].Day, true
}

// Walk 先核区间，再遍历；覆盖错误不会产生部分回调结果。
func (c *Calendar) Walk(k tf.ProductKey, from, to tf.TradingDay, fn func(tf.Day) bool) error {
	a, b, ok := c.Covers(k)
	if !ok || from > to || from < a || to > b {
		return tf.ErrUncovered
	}
	if _, e := Date(from); e != nil {
		return e
	}
	if _, e := Date(to); e != nil {
		return e
	}
	for _, r := range c.data[k] {
		if r.Day < from || r.Day > to || len(r.Sessions) == 0 {
			continue
		}
		if !fn(tf.Day{Num: r.Day, Sessions: append([]tf.Session(nil), r.Sessions...)}) {
			break
		}
	}
	return nil
}

// DayAt 按记录中的绝对时间查归属，不根据自然日猜夜盘交易日。
func (c *Calendar) DayAt(k tf.ProductKey, ts int64) (tf.Day, error) {
	rr := c.data[k]
	if len(rr) == 0 {
		return tf.Day{}, tf.ErrUncovered
	}
	for _, r := range rr {
		for _, s := range r.Sessions {
			if s.Contains(ts) {
				return tf.Day{Num: r.Day, Sessions: append([]tf.Session(nil), r.Sessions...)}, nil
			}
		}
	}
	// 最后收盘当晚可能属于尚未覆盖的下一交易日，不能按自然日报成休市。
	var first, last int64
	for _, r := range rr {
		if len(r.Sessions) == 0 {
			continue
		}
		if first == 0 {
			first = r.Sessions[0].Start
		}
		last = r.Sessions[len(r.Sessions)-1].End
	}
	if first == 0 || ts < first || ts >= last {
		return tf.Day{}, tf.ErrUncovered
	}
	return tf.Day{}, tf.ErrClosed
}

// Evidence 返回某日期的证据副本。
func (c *Calendar) Evidence(k tf.ProductKey, d tf.TradingDay) ([]evidence.Ref, error) {
	r, e := c.record(k, d)
	return r.Evidence, e
}

var _ tf.Calendar = (*Calendar)(nil)
