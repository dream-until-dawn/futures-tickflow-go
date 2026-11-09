package catalog

import (
	"fmt"

	tf "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/verified"
)

// Query 是按交易所、历史闭区间的目录查询。
type Query struct {
	Exchange string
	From, To tf.TradingDay
}

// QueryReport 区分已核生命周期相交、区间内观察、生命周期未知候选。
// Observed可能只覆盖一天，绝不是区间内完整挂牌证明。
type QueryReport struct {
	Requested                 Query   `json:"requested"`
	LifecycleOverlap          []Entry `json:"lifecycle_overlap"`
	Observed                  []Entry `json:"observed"`
	Unresolved                []Entry `json:"unresolved"`
	CompleteUniverseCertified bool    `json:"complete_universe_certified"`
}

// QueryHistory 不从当前合约倒推历史；未知条目不会混进已核生命周期集合。
func (c *Catalog) QueryHistory(q Query) (QueryReport, error) {
	r := QueryReport{Requested: q}
	if _, err := verified.Date(q.From); err != nil {
		return r, err
	}
	if _, err := verified.Date(q.To); err != nil {
		return r, err
	}
	if q.From > q.To {
		return r, fmt.Errorf("catalog: 查询范围颠倒")
	}
	switch q.Exchange {
	case tf.SHFE, tf.INE, tf.CZCE, tf.DCE, tf.GFEX, tf.CFFEX:
	default:
		return r, fmt.Errorf("catalog: 未知交易所")
	}
	for _, e := range c.Entries() {
		if e.Symbol.Exchange != q.Exchange {
			continue
		}
		if e.Listed.Day != 0 && e.LastTrading.Day != 0 {
			if e.Listed.Day <= q.To && e.LastTrading.Day >= q.From {
				r.LifecycleOverlap = append(r.LifecycleOverlap, e)
			}
			continue
		}
		observed := false
		for _, o := range e.Observed {
			if q.From <= o.Day && o.Day <= q.To {
				observed = true
			}
		}
		if observed {
			r.Observed = append(r.Observed, e)
		} else if !(e.Listed.Day != 0 && e.Listed.Day > q.To) && !(e.LastTrading.Day != 0 && e.LastTrading.Day < q.From) {
			r.Unresolved = append(r.Unresolved, e)
		}
	}
	return r, nil
}
