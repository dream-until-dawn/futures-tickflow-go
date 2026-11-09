package catalog

import (
	"encoding/json"
	"fmt"
	"strconv"

	tf "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/verified"
	"github.com/dream-until-dawn/futures-tickflow-go/refdata/evidence"
)

// ParseSHFEBase 读取历史ContractBaseInfo；逐行核业务日期和官方全代码。
// OPENDATE进入上市事实，EXPIREDATE单列保留，不在缺少语义核验时冒充最后交易日。
// 该日实际出现的全代码构成观察清单，不能自动认证整个历史挂牌全集。
func ParseSHFEBase(raw []byte, ref evidence.Ref, day tf.TradingDay) ([]Entry, error) {
	if err := ref.Verify(raw); err != nil {
		return nil, err
	}
	if _, err := verified.Date(day); err != nil {
		return nil, err
	}
	var doc struct {
		Date string `json:"report_date"`
		Rows []struct {
			Code     string `json:"INSTRUMENTID"`
			Exchange string `json:"EXCHANGEID"`
			Product  string `json:"COMMODITYID"`
			Day      string `json:"TRADINGDAY"`
			Open     string `json:"OPENDATE"`
			Expiry   string `json:"EXPIREDATE"`
		} `json:"ContractBaseInfo"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	if doc.Date != fmt.Sprintf("%08d", day) || len(doc.Rows) == 0 {
		return nil, fmt.Errorf("catalog: 基本信息业务日期错配/无内容")
	}
	out := make([]Entry, 0, len(doc.Rows))
	for _, row := range doc.Rows {
		if row.Day != doc.Date || (row.Exchange != tf.SHFE && row.Exchange != tf.INE) {
			return nil, fmt.Errorf("catalog: 基本信息逐行日期/市场错配")
		}
		s, err := tf.ParseSymbol(row.Exchange + "." + row.Code)
		if err != nil || s.Product != row.Product {
			return nil, fmt.Errorf("catalog: 基本信息代码错配")
		}
		parse := func(text string) (DateFact, error) {
			n, e := strconv.Atoi(text)
			if e != nil || len(text) != 8 {
				return DateFact{}, fmt.Errorf("catalog: 基本信息日期无效")
			}
			d := tf.TradingDay(n)
			if _, e = verified.Date(d); e != nil {
				return DateFact{}, e
			}
			return DateFact{Day: d, Evidence: []evidence.Ref{ref}}, nil
		}
		open, err := parse(row.Open)
		if err != nil {
			return nil, err
		}
		expiry, err := parse(row.Expiry)
		if err != nil {
			return nil, err
		}
		if open.Day > day || expiry.Day < day {
			return nil, fmt.Errorf("catalog: 观察日期在公开生命周期之外")
		}
		out = append(out, Entry{Symbol: s, Listed: open, ReportedExpiry: expiry, Observed: []Observation{{Day: day, Evidence: ref}}})
	}
	c, err := New(out)
	if err != nil {
		return nil, err
	}
	return c.Entries(), nil
}
