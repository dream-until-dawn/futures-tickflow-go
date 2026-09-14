package main

// v0.6 D-A 重裁前的补量（评审方 2026-09-14 点名）：同一品种的主连、近月、远月、新上市合约，一年里每个交易日的夜盘分四类。
//
//	a 有夜盘根且夜盘量 > 0
//	b 有夜盘根而夜盘量合计 = 0
//	c 没有夜盘根
//	d 日盘也没有量 > 0 的根（整天零成交；与 a/b/c 独立计）
//
// 另数：同一天主连是 a、而该合约是 b 或 c 的天数 ——「按单个具体合约判夜盘会判错」的直接计数。
//
// 做法（点名才跑：go run . -only shinny-da-matrix）：
//
//	交易日列表 ← 主连日线（datetime ＝ 交易日 00:00 CST，6.13）
//	每个序列   ← 一条新连接，focus_datetime＝窗口起点前 5 天，left_kline_id 续翻到窗口终点之后
//	            删快照时留住 id > right_id 的（捎上的那一根，shinnysource 离线复刻撞出来的那一格）
//	归交易日   ← 6.13 c 验过的规则：属于第一个「当日 15:15 晚于开盘时刻」的交易日
//	夜盘根     ← 开盘时刻（CST）在 20:00 之后或 04:00 之前
//	只统计该合约「首根所属交易日 … 末根所属交易日」与窗口的交集；主连按整个窗口
//
// ⚠️ 射程：rb 一个品种 · 模板上每个交易日都有夜盘（rb ≥ 2020-05-06）· 这一刻的数据。
// 本文件不 import 本库。

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
)

var daSyms, daFrom, daTo string

func init() {
	flag.StringVar(&daSyms, "dasyms", "KQ.m@SHFE.rb,SHFE.rb2605,SHFE.rb2609,SHFE.rb2708", "shinny-da-matrix 的序列（第一个是主连对照组）")
	flag.StringVar(&daFrom, "dafrom", "2025-09-15", "shinny-da-matrix 窗口起（交易日，含）")
	flag.StringVar(&daTo, "dato", "2026-09-11", "shinny-da-matrix 窗口止（交易日，含）")
}

type daDay struct {
	nightBars      int
	nightVol, dayV float64
	dayBars        int
}

func probeDAMatrix(md, tok string) {
	const name = "shinny-da-matrix"
	if !optIn(name) {
		return
	}
	from, err1 := time.ParseInLocation("2006-01-02", daFrom, cst)
	to, err2 := time.ParseInLocation("2006-01-02", daTo, cst)
	if err1 != nil || err2 != nil {
		report(name, "FAIL", fmt.Sprintf("窗口参数不对：%v %v", err1, err2))
		return
	}
	syms := strings.Split(daSyms, ",")
	minDur, dayDur := int64(60)*1e9, int64(86400)*1e9
	minKey, dayKey := strconv.FormatInt(minDur, 10), strconv.FormatInt(dayDur, 10)

	dial := func(ctx context.Context) (*websocket.Conn, error) {
		c, _, err := websocket.Dial(ctx, md, &websocket.DialOptions{
			CompressionMode: websocket.CompressionNoContextTakeover,
			HTTPHeader:      http.Header{"User-Agent": {uaSelf}, "Accept": {"application/json"}, "Authorization": {"Bearer " + tok}},
		})
		if err == nil {
			c.SetReadLimit(256 << 20)
		}
		return c, err
	}
	type conn struct {
		c    *websocket.Conn
		snap map[string]any
	}
	pump := func(ctx context.Context, cn *conn, cond func() bool) error {
		for !cond() {
			rctx, rc := context.WithTimeout(ctx, 60*time.Second)
			_, msg, err := cn.c.Read(rctx)
			rc()
			if err != nil {
				return err
			}
			var m struct {
				Aid  string           `json:"aid"`
				Data []map[string]any `json:"data"`
			}
			if json.Unmarshal(msg, &m) == nil && m.Aid == "rtn_data" {
				for _, d := range m.Data {
					merge(cn.snap, d)
				}
			}
			b, _ := json.Marshal(map[string]any{"aid": "peek_message"})
			if err := cn.c.Write(ctx, websocket.MessageText, b); err != nil {
				return err
			}
		}
		return nil
	}
	send := func(ctx context.Context, cn *conn, v any) {
		b, _ := json.Marshal(v)
		cn.c.Write(ctx, websocket.MessageText, b)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	var b strings.Builder

	// —— 交易日列表：主连日线 ——
	c0, err := dial(ctx)
	if err != nil {
		report(name, "FAIL", "连接失败: "+err.Error())
		return
	}
	cn0 := &conn{c: c0, snap: map[string]any{}}
	send(ctx, cn0, map[string]any{"aid": "set_chart", "chart_id": "dd", "ins_list": syms[0], "duration": dayDur, "view_width": 10000, "left_kline_id": 0})
	send(ctx, cn0, map[string]any{"aid": "peek_message"})
	if err := pump(ctx, cn0, func() bool {
		ch, ser := obj(cn0.snap, "charts", "dd"), obj(cn0.snap, "klines", syms[0], dayKey)
		if ch == nil || ser == nil {
			return false
		}
		ready, _ := ch["ready"].(bool)
		more, _ := ch["more_data"].(bool)
		r, _ := ch["right_id"].(float64)
		l, ok := ser["last_id"].(float64)
		return ready && !more && ok && r >= l
	}); err != nil {
		report(name, "FAIL", "日线没拉齐: "+err.Error())
		return
	}
	var tdays []time.Time
	for _, v := range obj(obj(cn0.snap, "klines", syms[0], dayKey), "data") {
		ts, _ := v.(map[string]any)["datetime"].(float64)
		tdays = append(tdays, time.Unix(0, int64(ts)).In(cst))
	}
	c0.CloseNow()
	sort.Slice(tdays, func(i, j int) bool { return tdays[i].Before(tdays[j]) })
	cut := func(d time.Time) int64 { return time.Date(d.Year(), d.Month(), d.Day(), 15, 15, 0, 0, cst).UnixNano() }
	dayOf := func(ts int64) (string, bool) {
		i := sort.Search(len(tdays), func(i int) bool { return cut(tdays[i]) > ts })
		if i >= len(tdays) {
			return "", false
		}
		return tdays[i].Format("2006-01-02"), true
	}
	var window []string
	for _, d := range tdays {
		if !d.Before(from) && !d.After(to) {
			window = append(window, d.Format("2006-01-02"))
		}
	}
	fmt.Fprintf(&b, "窗口 %s…%s：主连日线给出 %d 个交易日\n       ", daFrom, daTo, len(window))

	// —— 每个序列 ——
	type result struct {
		sym         string
		days        map[string]*daDay
		first, last string
		bars        int
		err         error
		wins        int
	}
	var results []result
	startNs := from.AddDate(0, 0, -5).UnixNano()
	endNs := cut(to)
	for _, sym := range syms {
		res := result{sym: sym, days: map[string]*daDay{}}
		c, err := dial(ctx)
		if err != nil {
			res.err = err
			results = append(results, res)
			continue
		}
		cn := &conn{c: c, snap: map[string]any{}}
		left := int64(-1)
		var maxT int64
		for {
			rq := map[string]any{"aid": "set_chart", "chart_id": "dm", "ins_list": sym, "duration": minDur, "view_width": 10000}
			if left < 0 {
				rq["focus_datetime"], rq["focus_position"] = startNs, 0
			} else {
				rq["left_kline_id"] = left
			}
			send(ctx, cn, rq)
			if res.wins == 0 {
				send(ctx, cn, map[string]any{"aid": "peek_message"})
			}
			want := left
			if err := pump(ctx, cn, func() bool {
				ch := obj(cn.snap, "charts", "dm")
				ready, _ := ch["ready"].(bool)
				more, _ := ch["more_data"].(bool)
				l, ok := ch["left_id"].(float64)
				return ready && !more && ok && l >= 0 && (want < 0 || int64(l) == want)
			}); err != nil {
				res.err = fmt.Errorf("第 %d 窗: %w", res.wins+1, err)
				break
			}
			res.wins++
			ch := obj(cn.snap, "charts", "dm")
			l, r := int64(ch["left_id"].(float64)), int64(ch["right_id"].(float64))
			ser := obj(cn.snap, "klines", sym, minKey)
			last := int64(ser["last_id"].(float64))
			data := obj(ser, "data")
			for id := l; id <= r && id <= last; id++ {
				row, ok := data[strconv.FormatInt(id, 10)].(map[string]any)
				if !ok {
					res.err = fmt.Errorf("窗 [%d,%d] 缺 id %d", l, r, id)
					break
				}
				kb := toBar(id, row)
				if kb.t > maxT {
					maxT = kb.t
				}
				if kb.t >= endNs {
					continue
				}
				d, ok := dayOf(kb.t)
				if !ok {
					continue
				}
				res.bars++
				if res.first == "" || d < res.first {
					res.first = d
				}
				if d > res.last {
					res.last = d
				}
				dd := res.days[d]
				if dd == nil {
					dd = &daDay{}
					res.days[d] = dd
				}
				hh := time.Unix(0, kb.t).In(cst).Hour()
				if hh >= 20 || hh < 4 {
					dd.nightBars++
					dd.nightVol += kb.v
				} else {
					dd.dayBars++
					dd.dayV += kb.v
				}
			}
			for k := range data {
				if id, e := strconv.ParseInt(k, 10, 64); e != nil || id <= r {
					delete(data, k)
				}
			}
			if res.err != nil || maxT >= endNs || r >= last {
				break
			}
			left = r + 1
		}
		c.CloseNow()
		results = append(results, res)
	}

	classify := func(d *daDay) (cls string, dayless bool) {
		switch {
		case d == nil || d.nightBars == 0:
			cls = "c"
		case d.nightVol > 0:
			cls = "a"
		default:
			cls = "b"
		}
		dayless = d == nil || d.dayV <= 0
		return
	}
	mainRes := results[0]
	st := "PASS"
	for _, res := range results {
		if res.err != nil {
			st = "FAIL"
			fmt.Fprintf(&b, "%s：中途失败 %v（已收 %d 根、%d 窗）\n       ", res.sym, res.err, res.bars, res.wins)
			continue
		}
		lo, hi := daFrom, daTo
		if res.sym != syms[0] {
			if res.first > lo {
				lo = res.first
			}
			if res.last < hi {
				hi = res.last
			}
		}
		cnt := map[string]int{}
		cntDayOK := map[string]int{}
		dless := 0
		var cross []string
		crossDayOK := 0
		n := 0
		for _, d := range window {
			if d < lo || d > hi {
				continue
			}
			n++
			cls, dayless := classify(res.days[d])
			cnt[cls]++
			if dayless {
				dless++
			} else {
				cntDayOK[cls]++
			}
			if res.sym != syms[0] {
				mcls, _ := classify(mainRes.days[d])
				if mcls == "a" && cls != "a" {
					cross = append(cross, d+"("+cls+")")
					if !dayless {
						crossDayOK++
					}
				}
			}
		}
		fmt.Fprintf(&b, "%s：%d 窗 · 窗口内 %d 根 · 首根交易日 %s · 末根交易日 %s · 统计区间 %s…%s 共 %d 个交易日\n       ",
			res.sym, res.wins, res.bars, res.first, res.last, lo, hi, n)
		fmt.Fprintf(&b, "  a %d · b %d · c %d · d（整天日盘零成交）%d\n       ", cnt["a"], cnt["b"], cnt["c"], dless)
		fmt.Fprintf(&b, "  只算日盘有量 > 0 的日子（D-B：日盘都没量的那天不判夜盘）：a %d · b %d · c %d\n       ", cntDayOK["a"], cntDayOK["b"], cntDayOK["c"])
		if res.sym != syms[0] {
			show := cross
			if len(show) > 12 {
				show = append(append([]string{}, cross[:6]...), append([]string{"…"}, cross[len(cross)-6:]...)...)
			}
			fmt.Fprintf(&b, "  主连是 a 而本合约不是 a：%d 天（其中本合约日盘有量 %d 天）%v\n       ", len(cross), crossDayOK, show)
		} else {
			var cs []string
			for _, d := range window {
				if cls, _ := classify(res.days[d]); cls != "a" {
					cs = append(cs, d+"("+cls+")")
				}
			}
			fmt.Fprintf(&b, "  主连不是 a 的日子：%v\n       ", cs)
		}
	}
	// —— 并集（评审方列的乙′）：同一交易日，列出的具体合约里任何一个夜盘量 > 0 ⇒ a；都没有量而有根 ⇒ b；都没根 ⇒ c ——
	// ⚠️ 并集只含本次列出的那几个合约，不是「该品种当天所有挂牌合约」。
	if len(results) > 2 {
		var mainA, unionA []string
		ucnt := map[string]int{}
		for _, d := range window {
			ucls := "c"
			for _, res := range results[1:] {
				if res.err != nil || d < res.first || d > res.last {
					continue
				}
				cls, _ := classify(res.days[d])
				if cls == "a" {
					ucls = "a"
					break
				}
				if cls == "b" {
					ucls = "b"
				}
			}
			ucnt[ucls]++
			mcls, _ := classify(mainRes.days[d])
			if mcls == "a" && ucls != "a" {
				mainA = append(mainA, d+"("+ucls+")")
			}
			if ucls == "a" && mcls != "a" {
				unionA = append(unionA, d+"("+mcls+")")
			}
		}
		fmt.Fprintf(&b, "并集（%s）：a %d · b %d · c %d\n       ", strings.Join(syms[1:], "+"), ucnt["a"], ucnt["b"], ucnt["c"])
		fmt.Fprintf(&b, "  主连是 a 而并集不是 a：%d 天 %v\n       ", len(mainA), mainA)
		fmt.Fprintf(&b, "  并集是 a 而主连不是 a：%d 天 %v\n       ", len(unionA), unionA)
	}
	report(name, st, strings.TrimRight(b.String(), " \n"))
}
