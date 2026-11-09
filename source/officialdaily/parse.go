// Package officialdaily 将可追溯官方日行情接入原有Source，不填造缺值或修复异常OHLC。
package officialdaily

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	tf "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/verified"
	"github.com/dream-until-dawn/futures-tickflow-go/refdata/evidence"
)

// Record 保留原始报价字面量。空串、--和零值不合并，浮点仅在组装Bar时使用。
type Record struct {
	Exchange     string            `json:"exchange"`
	Native       string            `json:"native"`
	Day          tf.TradingDay     `json:"day"`
	Fields       map[string]string `json:"fields"`
	TurnoverUnit string            `json:"turnover_unit"` // yuan / wanyuan / unknown
	Evidence     evidence.Ref      `json:"evidence"`
	Member       string            `json:"member,omitempty"`
	Row          int               `json:"row"`
	Artifact     *evidence.Ref     `json:"artifact,omitempty"`
}

// Document 是冻结原件；Day必须是业务日期，不是取得时刻或update_date。
type Document struct {
	Format   string        `json:"format"` // shfe-json / czce-txt / annual-decoded-v1
	Day      tf.TradingDay `json:"day"`
	Evidence evidence.Ref  `json:"evidence"`
	Raw      []byte        `json:"-"`
	Origin   evidence.Ref  `json:"origin,omitempty"`
	Original []byte        `json:"-"`
}

var numeric = regexp.MustCompile(`^(?:[0-9]+|[0-9]{1,3}(?:,[0-9]{3})+)(?:\.[0-9]+)?$`)
var shfeProduct = regexp.MustCompile(`^[a-z]{1,3}_f$`)
var month = regexp.MustCompile(`^[0-9]{4}$`)
var czceCode = regexp.MustCompile(`^[A-Z]{1,3}[0-9]{3,4}$`)
var ine = map[string]bool{"sc": true, "nr": true, "lu": true, "bc": true, "ec": true}

// Number 将原始非负十进制转为计算值；缺值返回NaN，零仍是零。
// 原报价始终保留在Record，不用此转换结果作财务原始字段。
func Number(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "-" || s == "--" {
		return math.NaN(), nil
	}
	if !numeric.MatchString(s) {
		return 0, fmt.Errorf("officialdaily: 非法非负十进制 %q", s)
	}
	v, e := strconv.ParseFloat(strings.ReplaceAll(s, ",", ""), 64)
	if e != nil || math.IsInf(v, 0) {
		return 0, fmt.Errorf("officialdaily: 数值溢出 %q", s)
	}
	return v, nil
}
func text(v json.RawMessage) (string, error) {
	if len(v) == 0 || bytes.Equal(v, []byte("null")) {
		return "", nil
	}
	if v[0] == '"' {
		var s string
		e := json.Unmarshal(v, &s)
		return strings.TrimSpace(s), e
	}
	var n json.Number
	if e := json.Unmarshal(v, &n); e != nil {
		return "", e
	}
	return string(n), nil
}
func finish(rr []Record) ([]Record, error) {
	if len(rr) == 0 {
		return nil, fmt.Errorf("officialdaily: 无真实期货行")
	}
	seen := map[string]bool{}
	for _, r := range rr {
		k := fmt.Sprintf("%s.%s/%d", r.Exchange, r.Native, r.Day)
		if seen[k] {
			return nil, fmt.Errorf("officialdaily: 重复合约日 %s", k)
		}
		seen[k] = true
		for _, key := range []string{"open", "high", "low", "close", "settle", "pre_settle", "volume", "open_interest", "turnover"} {
			if _, e := Number(r.Fields[key]); e != nil {
				return nil, e
			}
		}
		for _, key := range []string{"volume", "open_interest"} {
			v, _ := Number(r.Fields[key])
			if math.IsNaN(v) || v != math.Trunc(v) || v > 9007199254740991 {
				return nil, fmt.Errorf("officialdaily: 量仓缺失/非安全整数")
			}
		}
	}
	return rr, nil
}

// Parse 在读取任何报价前核验原件哈希和业务日期；不接受HTTP200挑战页或无期货内容。
func Parse(d Document) ([]Record, error) {
	if len(d.Raw) > 64<<20 || len(d.Original) > 256<<20 {
		return nil, fmt.Errorf("officialdaily: 文档超过64MiB或原始ZIP超过256MiB预算")
	}
	if e := d.Evidence.Verify(d.Raw); e != nil {
		return nil, e
	}
	if _, e := verified.Date(d.Day); e != nil {
		return nil, e
	}
	switch d.Format {
	case "shfe-json":
		return parseSHFE(d)
	case "czce-txt":
		return parseCZCE(d)
	case "annual-decoded-v1":
		return parseAnnualDecoded(d)
	default:
		return nil, fmt.Errorf("officialdaily: 未支持格式 %q", d.Format)
	}
}
func parseSHFE(d Document) ([]Record, error) {
	var doc struct {
		Code json.RawMessage              `json:"o_code"`
		Date string                       `json:"report_date"`
		Rows []map[string]json.RawMessage `json:"o_curinstrument"`
	}
	if e := json.Unmarshal(d.Raw, &doc); e != nil {
		return nil, e
	}
	code, e := text(doc.Code)
	if e != nil || (code != "0" && code != "0000") || doc.Date != fmt.Sprintf("%08d", d.Day) {
		return nil, fmt.Errorf("officialdaily: JSON业务状态/日期不符")
	}
	var out []Record
	fields := map[string]string{"open": "OPENPRICE", "high": "HIGHESTPRICE", "low": "LOWESTPRICE", "close": "CLOSEPRICE", "settle": "SETTLEMENTPRICE", "pre_settle": "PRESETTLEMENTPRICE", "volume": "VOLUME", "open_interest": "OPENINTEREST", "turnover": "TURNOVER"}
	for i, row := range doc.Rows {
		p, e := text(row["PRODUCTID"])
		if e != nil {
			return nil, e
		}
		m, e := text(row["DELIVERYMONTH"])
		if e != nil {
			return nil, e
		}
		// 只接受普通期货。TAS、期权、统计行不进入合约空间。
		if !shfeProduct.MatchString(p) {
			continue
		}
		if m == "" || m == "小计" || m == "合计" || m == "总计" {
			continue
		}
		if !month.MatchString(m) {
			return nil, fmt.Errorf("officialdaily: 非法交割月份 %q", m)
		}
		product := strings.TrimSuffix(p, "_f")
		exchange := tf.SHFE
		if ine[product] {
			exchange = tf.INE
		}
		symbol, e := tf.ParseSymbol(exchange + "." + product + m)
		if e != nil {
			return nil, e
		}
		r := Record{Exchange: exchange, Native: symbol.Instrument(), Day: d.Day, Fields: map[string]string{}, TurnoverUnit: "unknown", Evidence: d.Evidence, Row: i + 1}
		for k, v := range fields {
			if _, ok := row[v]; !ok && k != "turnover" {
				return nil, fmt.Errorf("officialdaily: JSON缺必要字段 %s", v)
			}
			value, e := text(row[v])
			if e != nil {
				return nil, e
			}
			r.Fields[k] = value
		}
		out = append(out, r)
	}
	return finish(out)
}
func parseCZCE(d Document) ([]Record, error) {
	if !utf8.Valid(d.Raw) {
		return nil, fmt.Errorf("officialdaily: TXT必须显式转码为UTF-8并绑定原件，不猜编码")
	}
	body := strings.TrimPrefix(string(d.Raw), "\ufeff")
	var lines []string
	for _, l := range strings.Split(body, "\n") {
		l = strings.TrimSpace(l)
		if l != "" {
			lines = append(lines, l)
		}
	}
	date, _ := verified.Date(d.Day)
	if len(lines) < 3 || lines[0] != "郑州商品交易所期货每日行情表("+date.Format("2006-01-02")+")" {
		return nil, fmt.Errorf("officialdaily: TXT标题/业务日期不符")
	}
	header := strings.Split(lines[1], "|")
	idx := map[string]int{}
	for i, s := range header {
		s = strings.TrimSpace(s)
		if _, ok := idx[s]; ok {
			return nil, fmt.Errorf("officialdaily: 重复列")
		}
		idx[s] = i
	}
	if strings.TrimSpace(header[0]) != "合约代码" && strings.TrimSpace(header[0]) != "品种月份" {
		return nil, fmt.Errorf("officialdaily: 未知合约表头")
	}
	fields := map[string]string{"open": "今开盘", "high": "最高价", "low": "最低价", "close": "今收盘", "settle": "今结算", "pre_settle": "昨结算", "volume": "成交量(手)", "open_interest": "持仓量", "turnover": "成交额(万元)"}
	for _, k := range fields {
		if _, ok := idx[k]; !ok {
			return nil, fmt.Errorf("officialdaily: 缺必要列 %s", k)
		}
	}
	var out []Record
	for i, l := range lines[2:] {
		values := strings.Split(l, "|")
		native := strings.TrimSpace(values[0])
		if strings.Contains(native, "小计") || strings.Contains(native, "合计") || strings.Contains(native, "总计") {
			continue
		}
		if !czceCode.MatchString(native) || len(values) != len(header) {
			return nil, fmt.Errorf("officialdaily: 非期货/截断/未知行 %d", i+3)
		}
		r := Record{Exchange: tf.CZCE, Native: native, Day: d.Day, Fields: map[string]string{}, TurnoverUnit: "wanyuan", Evidence: d.Evidence, Row: i + 3}
		for k, v := range fields {
			r.Fields[k] = strings.TrimSpace(values[idx[v]])
		}
		out = append(out, r)
	}
	return finish(out)
}
