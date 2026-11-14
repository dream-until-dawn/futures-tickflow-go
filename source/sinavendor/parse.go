// Package sinavendor 读取冻结的新浪供应商日线；身份仅绑定请求，不认证挂牌目录。
package sinavendor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
	"time"

	tf "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/refdata/evidence"
)

const inertComment = `/*<script>location.href='//sina.com';</script>*/`

var callbackPattern = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]{0,63}$`)

// Document 保存未经改写的原响应。Symbol由调用方提供，不能据URL推导交易所资格。
type Document struct {
	Symbol   tf.Symbol
	Evidence evidence.Ref
	Raw      []byte
}

// Record 保留八个原始字符串、文档和行号；所有记录永远是vendor/sina/request-bound。
type Record struct {
	Symbol   tf.Symbol
	Day      tf.TradingDay
	Fields   map[string]string
	Evidence evidence.Ref
	Row      int
}

// Identity 不随消费、存储或后续官方对比而改变。
func (r Record) Identity() string { return "vendor/sina/request-bound" }

// requestCallback 只接受已取证的日线服务、精确合约和固定接口标记。
func requestCallback(d Document) (string, error) {
	s, err := tf.ParseSymbol(d.Symbol.String())
	if err != nil || s != d.Symbol {
		return "", fmt.Errorf("sinavendor: 非规范合约")
	}
	u, err := url.Parse(d.Evidence.URL)
	if err != nil || u.Scheme != "https" || u.Host != "stock2.finance.sina.com.cn" || u.User != nil || u.Fragment != "" {
		return "", fmt.Errorf("sinavendor: 非批准日线URL")
	}
	const prefix = "/futures/api/jsonp.php/var "
	const suffix = "=/InnerFuturesNewService.getDailyKLine"
	if !strings.HasPrefix(u.Path, prefix) || !strings.HasSuffix(u.Path, suffix) {
		return "", fmt.Errorf("sinavendor: 服务路径不符")
	}
	cb := strings.TrimSuffix(strings.TrimPrefix(u.Path, prefix), suffix)
	q, err := url.ParseQuery(u.RawQuery)
	want := fmt.Sprintf("%s%04d", strings.ToUpper(s.Product), s.YearMon)
	if err != nil || !callbackPattern.MatchString(cb) || len(q) != 2 || len(q["symbol"]) != 1 || q.Get("symbol") != want || len(q["type"]) != 1 || q.Get("type") != "2021_04_12" {
		return "", fmt.Errorf("sinavendor: 请求合约/参数/callback不符")
	}
	return cb, nil
}

// Parse 验SHA后按准确包装惰性解析；拒绝重复键、重复日、额外脚本和空/未知响应。
func Parse(d Document) ([]Record, error) {
	if err := d.Evidence.Verify(d.Raw); err != nil {
		return nil, err
	}
	cb, err := requestCallback(d)
	if err != nil {
		return nil, err
	}
	b := bytes.TrimSpace(d.Raw)
	b = bytes.TrimSpace(bytes.TrimPrefix(b, []byte(inertComment)))
	prefix := []byte("var " + cb + "=(")
	if !bytes.HasPrefix(b, prefix) || !bytes.HasSuffix(b, []byte(");")) {
		return nil, fmt.Errorf("sinavendor: 非预期惰性包装")
	}
	b = b[len(prefix) : len(b)-2]
	dec := json.NewDecoder(bytes.NewReader(b))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('[') {
		return nil, fmt.Errorf("sinavendor: 未取得数组，不能记为空覆盖")
	}
	var out []Record
	var prev tf.TradingDay
	for dec.More() {
		tok, err = dec.Token()
		if err != nil || tok != json.Delim('{') {
			return nil, fmt.Errorf("sinavendor: 非对象行")
		}
		fields := map[string]string{}
		for dec.More() {
			k, e := dec.Token()
			if e != nil {
				return nil, e
			}
			key, ok := k.(string)
			allowed := map[string]bool{"d": true, "o": true, "h": true, "l": true, "c": true, "v": true, "p": true, "s": true}
			if !ok || !allowed[key] {
				return nil, fmt.Errorf("sinavendor: 未知字段")
			}
			if _, exists := fields[key]; exists {
				return nil, fmt.Errorf("sinavendor: 重复字段 %s", key)
			}
			var val string
			if e = dec.Decode(&val); e != nil {
				return nil, fmt.Errorf("sinavendor: 字段须为字符串: %w", e)
			}
			fields[key] = val
		}
		if _, err = dec.Token(); err != nil {
			return nil, err
		}
		for _, k := range []string{"d", "o", "h", "l", "c", "v", "p"} {
			if _, ok := fields[k]; !ok {
				return nil, fmt.Errorf("sinavendor: 缺字段 %s", k)
			}
		}
		dt, e := time.Parse("2006-01-02", fields["d"])
		if e != nil || dt.Format("2006-01-02") != fields["d"] {
			return nil, fmt.Errorf("sinavendor: 非法业务日期")
		}
		day := tf.TradingDay(dt.Year()*10000 + int(dt.Month())*100 + dt.Day())
		if day <= prev {
			return nil, fmt.Errorf("sinavendor: 日期重复或乱序")
		}
		out = append(out, Record{Symbol: d.Symbol, Day: day, Fields: fields, Evidence: d.Evidence, Row: len(out) + 1})
		prev = day
	}
	if _, err = dec.Token(); err != nil {
		return nil, err
	}
	if _, err = dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("sinavendor: 数组后有额外内容")
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("sinavendor: 空数组不证明无交易")
	}
	return out, nil
}
