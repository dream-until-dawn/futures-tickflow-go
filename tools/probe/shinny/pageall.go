package main

// v0.6 片一 a/c/d/e：主连 1m 全历史按 left_kline_id 翻页拉完，并拿同一次连接里的日线做三件事。
//
//	a 翻页端到端：窗口数、总根数、id 是否连续无重、首尾时刻、耗时
//	c 历史 1m 归交易日：规则「属于第一个『当日 15:15（北京时间）晚于这根开盘时刻』的交易日」
//	  ——交易日列表取自天勤日线（datetime ＝ 交易日当天 00:00 CST，探路实测）。
//	  归好之后按交易日聚合出 O/H/L/C/V，与天勤日线逐日对账。
//	d 没有夜盘的交易日：归到该交易日的 1m 里没有一根开盘时刻在 21:00 之后或 03:00 之前
//	e 连接行为：每个窗口从发出到就绪的耗时、有没有断连
//
// ⚠️ 规则 c 是【待验的假设】，不是本库的实现：它只用「交易日列表 ＋ 收盘时刻」，不用时段模板。
// 对账不齐的地方要逐日印出来看成因（换月日、合约切换、规则本身错），不许把不齐的日子静默丢掉。
// ⚠️ 射程：只量 KQ.m@SHFE.rb（主连、未复权）一个序列、这一刻。合约级序列、其它品种、其它周期都没量。

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

var pageWidth int
var pageSym string

func init() {
	flag.IntVar(&pageWidth, "pagew", 8000, "shinny-page-all 每个窗口的 view_width（探针实测上限 10000，12000 断连）")
	flag.StringVar(&pageSym, "pagesym", "KQ.m@SHFE.rb", "shinny-page-all 量哪个序列")
}

type kbar struct {
	id                int64
	t                 int64 // 开盘时刻，ns
	o, h, l, c, v, oi float64
}

func toBar(id int64, r map[string]any) kbar {
	f := func(k string) float64 { x, _ := r[k].(float64); return x }
	return kbar{id: id, t: int64(f("datetime")), o: f("open"), h: f("high"), l: f("low"), c: f("close"), v: f("volume"), oi: f("close_oi")}
}

func probePageAll(md, tok string) {
	const name = "shinny-page-all"
	if !optIn(name) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
	defer cancel()
	t0 := time.Now()
	c, _, err := websocket.Dial(ctx, md, &websocket.DialOptions{
		CompressionMode: websocket.CompressionNoContextTakeover,
		HTTPHeader: http.Header{"User-Agent": {uaTqsdk}, "Accept": {"application/json"},
			"Authorization": {"Bearer " + tok}},
	})
	if err != nil {
		report(name, "FAIL", "连接失败: "+err.Error())
		return
	}
	defer c.CloseNow()
	c.SetReadLimit(512 << 20)
	send := func(v any) error { b, _ := json.Marshal(v); return c.Write(ctx, websocket.MessageText, b) }
	min, day := int64(60)*1e9, int64(86400)*1e9
	minKey, dayKey := strconv.FormatInt(min, 10), strconv.FormatInt(day, 10)

	snap := map[string]any{}
	// pump 读到 cond 为真；每条 rtn_data 之后都回一个 peek_message（协议要求）。
	pump := func(cond func() bool, limit time.Duration) error {
		dl := time.Now().Add(limit)
		for !cond() {
			if time.Now().After(dl) {
				return fmt.Errorf("等了 %s 没等到", limit)
			}
			_, msg, rerr := c.Read(ctx)
			if rerr != nil {
				return rerr
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
			if err := send(map[string]any{"aid": "peek_message"}); err != nil {
				return err
			}
		}
		return nil
	}
	chartAt := func(id string, left, right int64) func() bool {
		return func() bool {
			ch := obj(snap, "charts", id)
			if ch == nil {
				return false
			}
			ready, _ := ch["ready"].(bool)
			more, _ := ch["more_data"].(bool)
			l, _ := ch["left_id"].(float64)
			r, _ := ch["right_id"].(float64)
			return ready && !more && int64(l) == left && int64(r) >= right
		}
	}

	// —— 日线：一窗全拿 ——
	send(map[string]any{"aid": "set_chart", "chart_id": "pd", "ins_list": pageSym, "duration": day,
		"view_width": 10000, "left_kline_id": 0})
	send(map[string]any{"aid": "peek_message"})
	if err := pump(func() bool {
		ser := obj(snap, "klines", pageSym, dayKey)
		if ser == nil {
			return false
		}
		last, ok := ser["last_id"].(float64)
		return ok && chartAt("pd", 0, int64(last))()
	}, 90*time.Second); err != nil {
		report(name, "FAIL", "日线没拉齐: "+err.Error())
		return
	}
	dser := obj(snap, "klines", pageSym, dayKey)
	dLast := int64(dser["last_id"].(float64))
	var daily []kbar
	for k, v := range obj(dser, "data") {
		id, _ := strconv.ParseInt(k, 10, 64)
		daily = append(daily, toBar(id, v.(map[string]any)))
	}
	sort.Slice(daily, func(i, j int) bool { return daily[i].id < daily[j].id })
	send(map[string]any{"aid": "set_chart", "chart_id": "pd", "ins_list": "", "duration": day, "view_width": 1}) // 放掉日线订阅

	// —— 1m：从 id 0 起逐窗翻 ——
	var bars []kbar
	var winDur []time.Duration
	left, windows := int64(0), 0
	var mLast int64 = -1
	var stopErr error
	for {
		w0 := time.Now()
		if err := send(map[string]any{"aid": "set_chart", "chart_id": "pa", "ins_list": pageSym, "duration": min,
			"view_width": pageWidth, "left_kline_id": left}); err != nil {
			stopErr = err
			break
		}
		if windows == 0 {
			send(map[string]any{"aid": "peek_message"})
		}
		want := left + int64(pageWidth) - 1
		err := pump(func() bool {
			ser := obj(snap, "klines", pageSym, minKey)
			if ser == nil {
				return false
			}
			l, ok := ser["last_id"].(float64)
			if !ok {
				return false
			}
			r := want
			if int64(l) < r {
				r = int64(l)
			}
			return chartAt("pa", left, r)()
		}, 120*time.Second)
		if err != nil {
			stopErr = fmt.Errorf("第 %d 窗（left=%d）: %w", windows+1, left, err)
			break
		}
		ser := obj(snap, "klines", pageSym, minKey)
		mLast = int64(ser["last_id"].(float64))
		right := int64(obj(snap, "charts", "pa")["right_id"].(float64))
		data := obj(ser, "data")
		for id := left; id <= right; id++ {
			r, ok := data[strconv.FormatInt(id, 10)].(map[string]any)
			if ok {
				bars = append(bars, toBar(id, r))
			}
		}
		// 收完就从本地快照里删掉这一窗 —— 快照不删会一路涨到几十万个 map
		for k := range data {
			delete(data, k)
		}
		windows++
		winDur = append(winDur, time.Since(w0))
		if right >= mLast {
			break
		}
		left = right + 1
	}
	elapsed := time.Since(t0)

	var b strings.Builder
	st := "PASS"
	// —— a ——
	sort.Slice(bars, func(i, j int) bool { return bars[i].id < bars[j].id })
	dup, gaps := 0, 0
	for i := 1; i < len(bars); i++ {
		switch d := bars[i].id - bars[i-1].id; {
		case d == 0:
			dup++
		case d > 1:
			gaps += int(d - 1)
		}
	}
	sd := append([]time.Duration(nil), winDur...)
	sort.Slice(sd, func(i, j int) bool { return sd[i] < sd[j] })
	p := func(q float64) time.Duration {
		if len(sd) == 0 {
			return 0
		}
		return sd[int(q*float64(len(sd)-1))]
	}
	fmt := fmt.Sprintf
	b.WriteString(fmt("a 翻页：%s 1m · view_width=%d · %d 窗 · 收到 %d 根（last_id=%d ⇒ 应有 %d）· 重复 %d · id 缺口 %d 根\n       ",
		pageSym, pageWidth, windows, len(bars), mLast, mLast+1, dup, gaps))
	if len(bars) > 0 {
		b.WriteString(fmt("  首根 id %d %s · 末根 id %d %s\n       ", bars[0].id, time.Unix(0, bars[0].t).In(cst).Format("2006-01-02 15:04"),
			bars[len(bars)-1].id, time.Unix(0, bars[len(bars)-1].t).In(cst).Format("2006-01-02 15:04")))
	}
	b.WriteString(fmt("e 连接：总耗时 %.1fs（含日线）· 每窗 p50 %.2fs · p90 %.2fs · max %.2fs · 中途错误：%v\n       ",
		elapsed.Seconds(), p(0.5).Seconds(), p(0.9).Seconds(), p(1).Seconds(), stopErr))
	if stopErr != nil || int64(len(bars)) != mLast+1 || dup > 0 || gaps > 0 {
		st = "FAIL"
	}
	b.WriteString(fmt("  日线：%d 根（last_id=%d）· 首 %s · 末 %s\n       ", len(daily), dLast,
		time.Unix(0, daily[0].t).In(cst).Format("2006-01-02"), time.Unix(0, daily[len(daily)-1].t).In(cst).Format("2006-01-02")))

	// —— c：归交易日 ——
	type agg struct {
		o, h, l, c, v float64
		n             int
		night         int
		// 诊断开盘价不齐的成因（不参与对账判定）：
		dayOpen         float64 // 当日日盘第一根的开盘价
		haveDayOpen     bool
		firstOI, lastOI float64 // 当日第一根 / 最后一根 1m 的持仓量
		prevLastOI      float64 // 上一交易日最后一根 1m 的持仓量
	}
	dayEnd := make([]int64, len(daily)) // 交易日 D 的「当日 15:15 CST」
	for i, d := range daily {
		dayEnd[i] = time.Unix(0, d.t).In(cst).Add(15*time.Hour + 15*time.Minute).UnixNano()
	}
	aggs := make([]agg, len(daily))
	before, after := 0, 0
	j := 0
	for _, x := range bars {
		for j < len(daily) && dayEnd[j] <= x.t {
			j++
		}
		if j >= len(daily) {
			after++
			continue
		}
		if j == 0 && x.t < time.Unix(0, daily[0].t).In(cst).Add(-6*time.Hour).UnixNano() {
			before++
		}
		a := &aggs[j]
		if a.n == 0 {
			a.o, a.h, a.l = x.o, x.h, x.l
			a.firstOI = x.oi
			if j > 0 {
				a.prevLastOI = aggs[j-1].lastOI
			}
		}
		if hh := time.Unix(0, x.t).In(cst).Hour(); !a.haveDayOpen && hh >= 8 && hh < 16 {
			a.dayOpen, a.haveDayOpen = x.o, true // 诊断：当日日盘第一根的开盘价
		}
		a.lastOI = x.oi
		if x.h > a.h {
			a.h = x.h
		}
		if x.l < a.l {
			a.l = x.l
		}
		a.c = x.c
		a.v += x.v
		a.n++
		if hh := time.Unix(0, x.t).In(cst).Hour(); hh >= 21 || hh < 3 {
			a.night++
		}
	}
	match, empty := 0, 0
	dayOIJump, jumpButMatch := 0, 0 // 诊断：持仓量跳变（×<0.8 或 ×>1.25）的交易日数，及其中对账全对上的天数
	var mism []string
	fieldBad := map[string]int{}
	for i, d := range daily {
		a := aggs[i]
		if a.n == 0 {
			empty++
			mism = append(mism, fmt("%s 没有归到任何 1m", time.Unix(0, d.t).In(cst).Format("2006-01-02")))
			continue
		}
		var bad []string
		for _, f := range []struct {
			k    string
			x, y float64
		}{{"O", a.o, d.o}, {"H", a.h, d.h}, {"L", a.l, d.l}, {"C", a.c, d.c}, {"V", a.v, d.v}} {
			if f.x != f.y {
				bad = append(bad, fmt("%s 1m聚合=%v 日线=%v", f.k, f.x, f.y))
				fieldBad[f.k]++
			}
		}
		// 诊断两个假设，只印读数、不改判定：
		//	甲 日线开盘价 ＝ 当日【日盘】第一根的开盘价（夜盘那几根不算进开盘）
		//	乙 这一天主连换了合约：上一交易日最后一根 1m 的持仓量与当日第一根差得远（换月时持仓量跳变）
		oiJump := 0.0
		if a.prevLastOI > 0 {
			oiJump = a.firstOI / a.prevLastOI
		}
		if a.prevLastOI > 0 {
			if oiJump < 0.8 || oiJump > 1.25 {
				dayOIJump++
				if len(bad) == 0 {
					jumpButMatch++
				}
			}
		}
		if len(bad) == 0 {
			match++
		} else {
			mism = append(mism, fmt("%s（%d 根）%s ‖ 诊断：日线O=日盘首根开盘价? %v（日盘首根=%v）· 持仓量 上日末根→当日首根 ×%.2f",
				time.Unix(0, d.t).In(cst).Format("2006-01-02"), a.n, strings.Join(bad, " · "), a.haveDayOpen && a.dayOpen == d.o, a.dayOpen, oiJump))
		}
	}
	b.WriteString(fmt("c 归交易日（规则：第一个「当日 15:15 晚于开盘时刻」的交易日）：日线 %d 天 · O/H/L/C/V 全对上 %d 天 · 不齐 %d 天（各字段不齐天数 %v）· 没归到 1m 的交易日 %d · 落在首个交易日之前很早的 1m %d · 落在末个交易日之后的 1m %d\n       ",
		len(daily), match, len(mism)-empty, fieldBad, empty, before, after))
	b.WriteString(fmt("  诊断：持仓量跳变（上日末根→当日首根 ×<0.8 或 ×>1.25）的交易日 %d 天，其中对账全对上的 %d 天\n       ", dayOIJump, jumpButMatch))
	for _, s := range mism {
		b.WriteString("  不齐 " + s + "\n       ")
	}

	// —— d：没有夜盘的交易日 ——
	var noNight []string
	for i := 1; i < len(daily); i++ {
		if aggs[i].n > 0 && aggs[i].night == 0 {
			prev := time.Unix(0, daily[i-1].t).In(cst)
			cur := time.Unix(0, daily[i].t).In(cst)
			noNight = append(noNight, fmt("%s（上一交易日 %s，隔 %d 个自然日）", cur.Format("2006-01-02 Mon"), prev.Format("01-02"), int(cur.Sub(prev).Hours()/24)))
		}
	}
	b.WriteString(fmt("d 没有夜盘的交易日（归到它的 1m 里没有 21:00 后或 03:00 前的）：%d 天\n       ", len(noNight)))
	for _, s := range noNight {
		b.WriteString("  " + s + "\n       ")
	}
	report(name, st, strings.TrimRight(b.String(), " \n"))
}
