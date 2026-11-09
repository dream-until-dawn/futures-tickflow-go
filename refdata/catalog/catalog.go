// Package catalog 管理历史合约及其不确定性，候选目录不等于认证挂牌全集。
package catalog

import (
	"fmt"
	"sort"

	tf "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/verified"
	"github.com/dream-until-dawn/futures-tickflow-go/refdata/evidence"
	"github.com/dream-until-dawn/futures-tickflow-go/refdata/shinnyref"
)

// DateFact 的零Day表示未知，不以交割月或首条行情补造。
type DateFact struct {
	Day      tf.TradingDay  `json:"day"`
	Evidence []evidence.Ref `json:"evidence"`
}

// Observation 只证明该日出现了该全代码；不得据首末观察之间推完整挂牌区间。
type Observation struct {
	Day      tf.TradingDay `json:"day"`
	Evidence evidence.Ref  `json:"evidence"`
}

// Entry 将候选、实际观察和生命周期证据分开。ExpireTs不是LastTrading。
type Entry struct {
	Symbol         tf.Symbol     `json:"symbol"`
	Listed         DateFact      `json:"listed"`
	LastTrading    DateFact      `json:"last_trading"`
	ReportedExpiry DateFact      `json:"reported_expiry"` // 官方EXPIREDATE原字段；未核语义时不代填最后交易日
	Observed       []Observation `json:"observed"`
	Candidate      *evidence.Ref `json:"candidate,omitempty"`
	ExpireTs       int64         `json:"expire_ts,omitempty"`
}

// Catalog 的条目不可变，由调用方显式提供本地历史资料。
type Catalog struct{ entries map[tf.Symbol]Entry }

func fact(f DateFact) error {
	if f.Day == 0 {
		if len(f.Evidence) != 0 {
			return fmt.Errorf("catalog: 未知日期不能附成已核事实")
		}
		return nil
	}
	if _, e := verified.Date(f.Day); e != nil {
		return e
	}
	if len(f.Evidence) == 0 {
		return fmt.Errorf("catalog: 日期缺证据")
	}
	for _, r := range f.Evidence {
		if e := r.Validate(); e != nil {
			return e
		}
	}
	return nil
}
func copyEntry(e Entry) Entry {
	e.Listed.Evidence = append([]evidence.Ref(nil), e.Listed.Evidence...)
	e.LastTrading.Evidence = append([]evidence.Ref(nil), e.LastTrading.Evidence...)
	e.ReportedExpiry.Evidence = append([]evidence.Ref(nil), e.ReportedExpiry.Evidence...)
	e.Observed = append([]Observation(nil), e.Observed...)
	if e.Candidate != nil {
		r := *e.Candidate
		e.Candidate = &r
	}
	return e
}

// New 拒绝重复、矛盾生命周期和无证据观察。未知字段保持零值。
func New(entries []Entry) (*Catalog, error) {
	c := &Catalog{entries: make(map[tf.Symbol]Entry)}
	for _, e := range entries {
		s, err := tf.ParseSymbol(e.Symbol.String())
		if err != nil || s != e.Symbol {
			return nil, fmt.Errorf("catalog: 非规范合约")
		}
		if _, ok := c.entries[s]; ok {
			return nil, fmt.Errorf("catalog: 重复合约 %s", s)
		}
		if err = fact(e.Listed); err != nil {
			return nil, err
		}
		if err = fact(e.LastTrading); err != nil {
			return nil, err
		}
		if err = fact(e.ReportedExpiry); err != nil {
			return nil, err
		}
		if e.Listed.Day != 0 && e.LastTrading.Day != 0 && e.Listed.Day > e.LastTrading.Day {
			return nil, fmt.Errorf("catalog: 生命周期颠倒")
		}
		if e.Candidate != nil {
			if err = e.Candidate.Validate(); err != nil {
				return nil, err
			}
		}
		seen := map[tf.TradingDay]bool{}
		for _, o := range e.Observed {
			if _, err = verified.Date(o.Day); err != nil {
				return nil, err
			}
			if err = o.Evidence.Validate(); err != nil {
				return nil, err
			}
			if seen[o.Day] || (e.Listed.Day != 0 && o.Day < e.Listed.Day) || (e.LastTrading.Day != 0 && o.Day > e.LastTrading.Day) {
				return nil, fmt.Errorf("catalog: 重复/生命周期外观察")
			}
			seen[o.Day] = true
		}
		if len(e.Observed) == 0 && e.Candidate == nil && e.Listed.Day == 0 && e.LastTrading.Day == 0 {
			return nil, fmt.Errorf("catalog: 空条目无依据")
		}
		c.entries[s] = copyEntry(e)
	}
	return c, nil
}

// Candidates 复用已取得的shinnyref快照；本函数不联网，也不把ExpireTs折成最后交易日。
func Candidates(contracts []shinnyref.Contract, ref evidence.Ref) ([]Entry, error) {
	if err := ref.Validate(); err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(contracts))
	for _, s := range contracts {
		r := ref
		out = append(out, Entry{Symbol: s.Symbol, Candidate: &r, ExpireTs: s.ExpireTs})
	}
	return out, nil
}

// Entries 返回稳定排序的副本，不宣称结果为挂牌全集。
func (c *Catalog) Entries() []Entry {
	out := make([]Entry, 0, len(c.entries))
	for _, e := range c.entries {
		out = append(out, copyEntry(e))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Symbol.String() < out[j].Symbol.String() })
	return out
}

// Get 查某全代码的证据条目。
func (c *Catalog) Get(s tf.Symbol) (Entry, bool) { e, ok := c.entries[s]; return copyEntry(e), ok }

// Resolve 将交易所原码绑定到有证据的全代码。歧义和未知均报错，不猜十年年份。
func (c *Catalog) Resolve(exchange, native string, day tf.TradingDay) (tf.Symbol, error) {
	if _, err := verified.Date(day); err != nil {
		return tf.Symbol{}, err
	}
	var result tf.Symbol
	count := 0
	for s, e := range c.entries {
		if s.Exchange != exchange || s.Instrument() != native {
			continue
		}
		known := e.Listed.Day != 0 && e.LastTrading.Day != 0 && e.Listed.Day <= day && day <= e.LastTrading.Day
		for _, o := range e.Observed {
			if o.Day == day {
				known = true
			}
		}
		if known {
			result = s
			count++
		}
	}
	if count != 1 {
		return tf.Symbol{}, fmt.Errorf("catalog: %s.%s 在 %s 的有据映射数为%d", exchange, native, day, count)
	}
	return result, nil
}

// Expected 是某日独立挂牌清单；必须来自行情之外的参考资料，调用方负责独立性审查。
type Expected struct {
	Day      tf.TradingDay
	Symbols  []tf.Symbol
	Evidence []evidence.Ref
}

// CheckSet 对照独立清单查删除和误加，包括零成交合约。它不从当前Catalog自造预期。
func CheckSet(want Expected, actual []tf.Symbol) error {
	if _, err := verified.Date(want.Day); err != nil {
		return err
	}
	if len(want.Evidence) == 0 {
		return fmt.Errorf("catalog: 缺独立清单证据")
	}
	for _, r := range want.Evidence {
		if err := r.Validate(); err != nil {
			return err
		}
	}
	set := map[tf.Symbol]bool{}
	for _, s := range want.Symbols {
		if _, e := tf.ParseSymbol(s.String()); e != nil || set[s] {
			return fmt.Errorf("catalog: 预期清单非法/重复")
		}
		set[s] = true
	}
	seen := map[tf.Symbol]bool{}
	for _, s := range actual {
		if !set[s] || seen[s] {
			return fmt.Errorf("catalog: 多余/重复合约 %s", s)
		}
		seen[s] = true
	}
	if len(seen) != len(set) {
		return fmt.Errorf("catalog: 有缺失合约，实际%d预期%d", len(seen), len(set))
	}
	return nil
}
