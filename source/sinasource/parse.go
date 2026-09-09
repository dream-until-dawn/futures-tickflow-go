// Package sinasource 从新浪财经拉期货行情。
//
// ⚠️ 这一版【只做日线】，而且【只做解析】：字节 → 行。
// 组装成 tickflow.Bar 需要交易日历（Ts/TsEnd 是时段的起止，判完结也要它），
// 那是下一片。分开的理由和 store.go / source.go 两次一样：
// **解析的不变量要先能被单独测红。** 混在一起时，一条红了分不清
// 是「新浪的响应变了」还是「日历答错了」——而这两者的处置完全相反。
//
// ⛔ **新浪的实时接口本包不实现**，也不会实现。probe.md 第三节实测它
// 自 2024-07-17 起冻结：返回 200、字段齐全、数值合理，而**六个字段与两年前
// 那天的日线逐位相同**。接一个会静默给出两年前数据的源，比没有这个源糟得多。
package sinasource

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// —— 两个「答不了」，必须和「没有数据」分得开 ——
var (
	// ErrUnknownSymbol 新浪不认识这个合约：响应体是 `var _=(null);`。
	//
	// ⚠️ **这不是「这段时间没有数据」。** 两者在字节层面差一个 `null` 与 `[]`，
	// 而在后果上差得很远：把它当成「没有数据」，一个拼错的合约代码就会被
	// coverage 记成「拉过，确认没有」，**然后永远不再重拉**。
	ErrUnknownSymbol = errors.New("sinasource: 新浪不认识这个合约（响应是 null，不是空数组）")

	// ErrNotJSONP 响应不是预期的 JSONP 形状。
	//
	// 单列出来是因为它多半意味着**接口变了**或者拿到了一张错误页，
	// 而那要人去看，不是重试能解决的。
	ErrNotJSONP = errors.New("sinasource: 响应不是预期的 JSONP 形状——多半是接口变了或拿到了错误页")
)

// jsonpPrefix 是新浪塞在最前面的那段脚本注释。
//
// 它不是可有可无的装饰：不剥掉，json.Unmarshal 第一个字节就报错，
// 而那个错误信息（"invalid character '/'"）指不到真正的原因。
const jsonpPrefix = `/*<script>location.href='//sina.com';</script>*/`

// DailyRow 是新浪日线的一行，字段名照它自己的。
//
// ⚠️ 全部是**字符串**，包括价格和手数——新浪用 JSON 字符串装数字。
// 直接声明成 float64 会让 Unmarshal 报错，而不是悄悄给 0；
// 这一点是好事，但也意味着**类型不能照抄「它看起来是什么」**。
type DailyRow struct {
	Date         string // "2026-03-16"
	Open         float64
	High         float64
	Low          float64
	Close        float64
	Volume       float64 // 成交量（手）
	OpenInterest float64 // 持仓量（手）—— 新浪的字段名是 p
	// Settle 是结算价，**`"0.000"` 已经映射成 NaN**。
	//
	// 0 不是一个「小的结算价」，它是缺失的伪装：没有任何品种的结算价会是零。
	// 而它按品种差得极远——本包 testdata 里 RB2610 与 TA2701 是 0/221 与 0/156，
	// T2612 是 **122/122**。中金所品种系统性缺失（probe.md 第一节）。
	Settle float64
}

// rawRow 是线上那份 JSON 的原样形状：每个字段都是字符串。
type rawRow struct {
	D string `json:"d"`
	O string `json:"o"`
	H string `json:"h"`
	L string `json:"l"`
	C string `json:"c"`
	V string `json:"v"`
	P string `json:"p"`
	S string `json:"s"`
}

// ParseDaily 把一份日线响应的原样字节解析成行。
//
// 它做四件事，每一件都对应一个实测出来的粗糙面（probe.md 第一、二节）：
//
//	一、剥 JSONP 外壳         `/*…*/\nvar _=(` … `);`
//	二、null 与 [] 分开        null ⇒ ErrUnknownSymbol；[] ⇒ 空切片、无错
//	三、字符串数字逐个转       转不动就报错，**不吞成 0**
//	四、s == 0 映射成 NaN      0 是缺失的伪装，不是一个小价格
//
// ⚠️ 它**不**做的两件，写在这里而不是留给下一个人去发现：
//
//	不判完结     日线要靠交易日历判，这里没有日历
//	不排序       返回顺序原样保留 —— 「升序」是 Source 的约定，
//	             由组装那一层连同 tickflow.CheckBars 一起负责。
//	             在这里悄悄排一次，会把**上游乱序**这个事实抹掉。
func ParseDaily(b []byte) ([]DailyRow, error) {
	body, err := stripJSONP(b)
	if err != nil {
		return nil, err
	}
	if bytes.Equal(bytes.TrimSpace(body), []byte("null")) {
		return nil, ErrUnknownSymbol
	}

	var raw []rawRow
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("sinasource: 解析 JSON 失败：%w（前 80 字节：%s）",
			err, head(body, 80))
	}

	out := make([]DailyRow, 0, len(raw))
	for i, r := range raw {
		num := func(field, s string) (float64, error) {
			v, err := strconv.ParseFloat(s, 64)
			if err != nil {
				return 0, fmt.Errorf("sinasource: 第 %d 行 %s=%q 不是数字：%w", i, field, s, err)
			}
			return v, nil
		}
		var row DailyRow
		row.Date = r.D
		if row.Date == "" {
			return nil, fmt.Errorf("sinasource: 第 %d 行没有日期字段 d", i)
		}
		var errs []error
		for _, f := range []struct {
			name string
			src  string
			dst  *float64
		}{
			{"o", r.O, &row.Open}, {"h", r.H, &row.High},
			{"l", r.L, &row.Low}, {"c", r.C, &row.Close},
			{"v", r.V, &row.Volume}, {"p", r.P, &row.OpenInterest},
			{"s", r.S, &row.Settle},
		} {
			v, err := num(f.name, f.src)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			*f.dst = v
		}
		if err := errors.Join(errs...); err != nil {
			return nil, err
		}
		// 四：0 是缺失的伪装。放在这里而不是留给上层，是因为
		// **上层看不出这个 0 是新浪给的还是解析出的零值**。
		if row.Settle == 0 {
			row.Settle = math.NaN()
		}
		out = append(out, row)
	}
	return out, nil
}

// stripJSONP 剥掉外壳，返回里面那段 JSON。
//
// 判据写成「找到 `(` 和最后一个 `)`」，而不是「按固定长度切」：
// 前缀那段脚本注释是新浪塞的，**它随时可以改长度**，
// 而按长度切的解析器在那一天会安静地把 JSON 的头几个字节也切掉。
func stripJSONP(b []byte) ([]byte, error) {
	s := strings.TrimSpace(string(b))

	// ⚠️ **不校验前缀是否等于 jsonpPrefix。** 那段脚本注释是新浪塞的，
	// 它随时可以改，而校验它只会在改的那天把一份【本可以解析】的响应判死。
	// 真正必需的是下面那对括号 —— 它是 JSONP 这个格式本身要求的。
	//
	// ⛔ 而「不校验」不等于「能扛住前缀变化」——**第一版就没扛住，测试当场抓到**：
	// 原实现直接取第一个 `(`，于是前缀里只要出现一个括号（比如
	// `/*<script>whatever();</script>*/`）就会取到注释里那个，JSON 从括号中间开始切。
	//
	//	它在真实数据上一直是对的，**而理由是碰巧**：
	//	线上那段前缀 `/*<script>location.href='//sina.com';</script>*/` 恰好没有括号。
	//	⇒ **一个碰巧成立的性质，会替一句声明它的注释背书。**
	//
	// ⇒ 所以先把开头那段块注释整段掐掉，再找括号。
	if strings.HasPrefix(s, "/*") {
		if k := strings.Index(s, "*/"); k >= 0 {
			s = strings.TrimSpace(s[k+2:])
		}
	}

	i := strings.Index(s, "(")
	j := strings.LastIndex(s, ")")
	if i < 0 || j < 0 || j <= i {
		return nil, fmt.Errorf("%w（前 80 字节：%s）", ErrNotJSONP, head([]byte(s), 80))
	}
	return []byte(strings.TrimSpace(s[i+1 : j])), nil
}

func head(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}
