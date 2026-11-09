package catalog

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	tf "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/verified"
	"github.com/dream-until-dawn/futures-tickflow-go/refdata/evidence"
)

// ParseCZCEReference 用官方XML的YrCd/MnthCd消歧三位代码。
// FrstTrdDt/LstTrdDt有值时记录生命周期；ExpiryTime不替代最后交易日。
// day须与原件URL中明确的日期路径一致，不能把今日参考列表倒灌历史。
func ParseCZCEReference(raw []byte, ref evidence.Ref, day tf.TradingDay) ([]Entry, error) {
	if e := ref.Verify(raw); e != nil {
		return nil, e
	}
	date, e := verified.Date(day)
	if e != nil {
		return nil, e
	}
	if !strings.Contains(ref.URL, "/"+date.Format("20060102")+"/") {
		return nil, fmt.Errorf("catalog: 参考原件URL未绑定观察日期")
	}
	if bytes.Contains(bytes.ToUpper(raw), []byte("<!DOCTYPE")) {
		return nil, fmt.Errorf("catalog: 拒绝XML外部声明")
	}
	// encoding/xml的结构体解码会接受重复字段，先独立按Token拒绝冲突字段。
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	depth, contractDepth := 0, 0
	var tags map[string]bool
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := token.(type) {
		case xml.StartElement:
			depth++
			if t.Name.Local == "Contract" {
				if contractDepth != 0 {
					return nil, fmt.Errorf("catalog: 嵌套Contract")
				}
				contractDepth = depth
				tags = map[string]bool{}
			} else if contractDepth != 0 && depth == contractDepth+1 {
				if tags[t.Name.Local] {
					return nil, fmt.Errorf("catalog: XML重复字段 %s", t.Name.Local)
				}
				tags[t.Name.Local] = true
			}
		case xml.EndElement:
			if depth == contractDepth {
				contractDepth = 0
			}
			depth--
		}
	}
	var doc struct {
		XMLName xml.Name `xml:"Contracts"`
		Rows    []struct {
			Native   string `xml:"CtrCd"`
			Product  string `xml:"PrdCd"`
			Type     string `xml:"PrdTp"`
			Expiry   string `xml:"ExpiryTime"`
			Exchange string `xml:"ExchCd"`
			Year     string `xml:"YrCd"`
			Month    string `xml:"MnthCd"`
			First    string `xml:"FrstTrdDt"`
			Last     string `xml:"LstTrdDt"`
		} `xml:"Contract"`
	}
	if e = xml.Unmarshal(raw, &doc); e != nil {
		return nil, e
	}
	out := []Entry{}
	seen := map[tf.Symbol]bool{}
	pattern := regexp.MustCompile(`^[A-Z]{1,3}[0-9]{3}$`)
	for _, r := range doc.Rows {
		if strings.TrimSpace(r.Type) != "期货" {
			continue
		}
		native := strings.TrimSpace(r.Native)
		p := strings.TrimSpace(r.Product)
		if r.Exchange != "XZCE" || !pattern.MatchString(native) {
			return nil, fmt.Errorf("catalog: XML市场/代码异常")
		}
		expiry, e := time.ParseInLocation("2006-01-02 15:04 北京时间", strings.TrimSpace(r.Expiry), time.FixedZone("CST", 8*3600))
		if e != nil || expiry.Year() < 2000 || expiry.Year() > 2099 {
			return nil, fmt.Errorf("catalog: 到期时间缺失/格式未知")
		}
		year, ye := strconv.Atoi(r.Year)
		mon, me := strconv.Atoi(r.Month)
		if ye != nil || me != nil || len(r.Year) != 4 || len(r.Month) != 2 || year < 2000 || year > 2099 || mon < 1 || mon > 12 || expiry.Year() != year || int(expiry.Month()) != mon {
			return nil, fmt.Errorf("catalog: 缺少显式交割年月或与到期不符")
		}
		s, e := tf.ParseSymbol(fmt.Sprintf("CZCE.%s%02d%02d", p, year%100, mon))
		if e != nil || s.Instrument() != native || seen[s] {
			return nil, fmt.Errorf("catalog: 原码与有据公历年月不符/重复")
		}
		seen[s] = true
		entry := Entry{Symbol: s, Observed: []Observation{{Day: day, Evidence: ref}}, ExpireTs: expiry.UnixMilli()}
		for _, item := range []struct {
			value  string
			target *DateFact
		}{{r.First, &entry.Listed}, {r.Last, &entry.LastTrading}} {
			if item.value == "" || item.value == "n.a." {
				continue
			}
			t, err := time.Parse("2006-01-02", item.value)
			if err != nil {
				return nil, err
			}
			item.target.Day = tf.TradingDay(t.Year()*10000 + int(t.Month())*100 + t.Day())
			item.target.Evidence = []evidence.Ref{ref}
		}
		out = append(out, entry)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("catalog: 无期货参考合约")
	}
	if _, e = New(out); e != nil {
		return nil, e
	}
	return out, nil
}
