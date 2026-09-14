package main

// 片一 b 最小版（评审方 2026-09-14 点名）：rb 交易日集合 —— 天勤日线（KQ.m@SHFE.rb）vs 新浪日线（RB0）。
// 只比日期集合，不比 OHLC。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
)

func probeSinaDays(md, tok string) {
	const name = "shinny-b-sinadays"
	if !optIn(name) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// —— 新浪 RB0 日线 ——
	u := "https://stock2.finance.sina.com.cn/futures/api/jsonp.php/var%20_=/InnerFuturesNewService.getDailyKLine?symbol=RB0"
	rq, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
	rq.Header.Set("Referer", "https://finance.sina.com.cn")
	rs, err := http.DefaultClient.Do(rq)
	if err != nil {
		report(name, "FAIL", "新浪请求失败: "+err.Error())
		return
	}
	body, _ := io.ReadAll(rs.Body)
	rs.Body.Close()
	s := string(body)
	i, j := strings.Index(s, "("), strings.LastIndex(s, ")")
	if i < 0 || j <= i {
		report(name, "FAIL", fmt.Sprintf("新浪响应不是 JSONP（HTTP %d，前 120 字节 %q）", rs.StatusCode, s[:min(120, len(s))]))
		return
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(s[i+1:j]), &rows); err != nil {
		report(name, "FAIL", "新浪 JSON 解析失败: "+err.Error())
		return
	}
	sina := map[string]bool{}
	for _, r := range rows {
		if d, ok := r["d"].(string); ok {
			sina[d] = true
		}
	}

	// —— 天勤日线 ——
	c, _, err := websocket.Dial(ctx, md, &websocket.DialOptions{
		CompressionMode: websocket.CompressionNoContextTakeover,
		HTTPHeader:      http.Header{"User-Agent": {uaTqsdk}, "Accept": {"application/json"}, "Authorization": {"Bearer " + tok}},
	})
	if err != nil {
		report(name, "FAIL", "天勤连接失败: "+err.Error())
		return
	}
	defer c.CloseNow()
	c.SetReadLimit(256 << 20)
	send := func(v any) { b, _ := json.Marshal(v); c.Write(ctx, websocket.MessageText, b) }
	const sym = "KQ.m@SHFE.rb"
	day := int64(86400) * 1e9
	send(map[string]any{"aid": "set_chart", "chart_id": "b", "ins_list": sym, "duration": day, "view_width": 10000, "left_kline_id": 0})
	send(map[string]any{"aid": "peek_message"})
	snap := map[string]any{}
	for {
		_, msg, rerr := c.Read(ctx)
		if rerr != nil {
			report(name, "FAIL", "天勤读失败: "+rerr.Error())
			return
		}
		var m struct {
			Aid  string           `json:"aid"`
			Data []map[string]any `json:"data"`
		}
		if json.Unmarshal(msg, &m) == nil && m.Aid == "rtn_data" {
			for _, d := range m.Data {
				merge(snap, d)
			}
		}
		ch := obj(snap, "charts", "b")
		ser := obj(snap, "klines", sym, strconv.FormatInt(day, 10))
		if ch != nil && ser != nil {
			ready, _ := ch["ready"].(bool)
			more, _ := ch["more_data"].(bool)
			r, _ := ch["right_id"].(float64)
			l, ok := ser["last_id"].(float64)
			if ready && !more && ok && int64(r) >= int64(l) {
				break
			}
		}
		send(map[string]any{"aid": "peek_message"})
	}
	tq := map[string]bool{}
	for _, v := range obj(obj(snap, "klines", sym, strconv.FormatInt(day, 10)), "data") {
		r := v.(map[string]any)
		ts, _ := r["datetime"].(float64)
		tq[time.Unix(0, int64(ts)).In(cst).Format("2006-01-02")] = true
	}

	keys := func(m map[string]bool) []string {
		var out []string
		for k := range m {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
	sk, tk := keys(sina), keys(tq)
	lo, hi := tk[0], tk[len(tk)-1]
	if sk[0] > lo {
		lo = sk[0]
	}
	if sk[len(sk)-1] < hi {
		hi = sk[len(sk)-1]
	}
	var onlySina, onlyTq []string
	nBoth := 0
	for _, d := range sk {
		if d >= lo && d <= hi {
			if tq[d] {
				nBoth++
			} else {
				onlySina = append(onlySina, d)
			}
		}
	}
	for _, d := range tk {
		if d >= lo && d <= hi && !sina[d] {
			onlyTq = append(onlyTq, d)
		}
	}
	st := "PASS"
	if len(onlySina)+len(onlyTq) > 0 {
		st = "FAIL" // FAIL 在这里的意思是「两个源的交易日集合不一致」，不是探针坏了
	}
	var b strings.Builder
	fmt.Fprintf(&b, "新浪 RB0 日线 %d 天（%s…%s）· 天勤 %s 日线 %d 天（%s…%s）\n       重叠区间 %s…%s：两边都有 %d 天 · 只在新浪 %d 天 %v · 只在天勤 %d 天 %v\n       ",
		len(sk), sk[0], sk[len(sk)-1], sym, len(tk), tk[0], tk[len(tk)-1], lo, hi, nBoth, len(onlySina), onlySina, len(onlyTq), onlyTq)

	// —— 第三方：中金所每日行情 XML（与 cffexsource 同一个端点）——
	// 读数：该日 URL 的 HTTP 状态、字节数、<tradingday> 记录条数与第一个值；对照组是两源并集里的前一天与后一天。
	// ⚠️ 用它当裁判是一个【判断】，前提是「中金所与上期所共用法定节假日」—— 探针不验这个前提。
	union := map[string]bool{}
	for d := range sina {
		union[d] = true
	}
	for d := range tq {
		union[d] = true
	}
	uk := keys(union)
	idx := map[string]int{}
	for i, d := range uk {
		idx[d] = i
	}
	cffex := func(d string) string {
		ymd := strings.ReplaceAll(d, "-", "")
		u := "http://www.cffex.com.cn/sj/hqsj/rtj/" + ymd[:6] + "/" + ymd[6:] + "/index.xml"
		cl := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		rq, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
		rs, err := cl.Do(rq)
		if err != nil {
			return "请求失败: " + err.Error()
		}
		bb, _ := io.ReadAll(rs.Body)
		rs.Body.Close()
		n := strings.Count(string(bb), "<tradingday>")
		first := ""
		if k := strings.Index(string(bb), "<tradingday>"); k >= 0 && k+20 <= len(bb) {
			first = string(bb[k+12 : k+20])
		}
		return fmt.Sprintf("HTTP %d · %d 字节 · <tradingday> %d 条 · 首个 %q", rs.StatusCode, len(bb), n, first)
	}
	for _, d := range append(append([]string{}, onlySina...), onlyTq...) {
		where := "只在新浪"
		if tq[d] {
			where = "只在天勤"
		}
		i := idx[d]
		fmt.Fprintf(&b, "中金所核 %s（%s）：%s\n       ", d, where, cffex(d))
		if i > 0 {
			fmt.Fprintf(&b, "  对照前一天 %s：%s\n       ", uk[i-1], cffex(uk[i-1]))
		}
		if i+1 < len(uk) {
			fmt.Fprintf(&b, "  对照后一天 %s：%s\n       ", uk[i+1], cffex(uk[i+1]))
		}
	}
	report(name, st, strings.TrimRight(b.String(), " \n"))
}
