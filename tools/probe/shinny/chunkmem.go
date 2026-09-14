package main

// v0.6 片二前置：真分块拉 1m 的内存峰值（H5 内存那一半，评审方 2026-09-14 要求「不许还是推论」）。
//
// ⚠️ 本文件 import 了本库 —— 与 template.go 同一类豁免，方向是【本库被测】：
// 被测对象是 `tickflow.Bar` 的布局与「DIFF 快照 → []tickflow.Bar」这一步的真实内存开销；
// 它不拿本库去判定外部世界长什么样（交易日归属用的是 6.13 验过的规则，写在本文件里，不调本库日历）。
//
// 做法（点名才跑：go run . -only shinny-chunk-mem）：
//
//	一次连接 ⇒ 取日线得交易日列表 ⇒ 取最近 -chunkspan 个交易日 ⇒ 对 -chunkdays 里每一档 N：
//	  按 N 个交易日一块顺序拉；每块的时间窗 ＝ [上一交易日 15:15, 块末交易日 15:15)（CST）
//	  set_chart focus_datetime＝窗起点、focus_position＝0、view_width＝min(10000, N×600＋100)；
//	  窗没盖满且还有更新的根 ⇒ left_kline_id＝right_id＋1 续翻
//	  取样点（runtime.ReadMemStats）：
//	    s0 这一块发请求之前
//	    s1 这一块的根都已合进 DIFF 快照、还没转换
//	    s2 已转成 []tickflow.Bar（从 nil 开始 append，切片增长方式照常）、快照里这一块的 data 还在
//	    s3 快照这一块的 data 删掉、[]Bar 丢掉、runtime.GC() 之后
//
// ⚠️ 射程：一个序列（默认 KQ.m@SHFE.rb）、最近 chunkspan 个交易日、一次运行；HeapAlloc 受 GC 时机影响，只报读数不外推。

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"github.com/coder/websocket"
	tickflow "github.com/dream-until-dawn/futures-tickflow-go"
)

var chunkDaysFlag string
var chunkSpan int
var chunkSym string

func init() {
	flag.StringVar(&chunkDaysFlag, "chunkdays", "5,20,60", "shinny-chunk-mem 每块多少个交易日（逗号分隔，逐档跑）")
	flag.IntVar(&chunkSpan, "chunkspan", 240, "shinny-chunk-mem 量最近多少个交易日")
	flag.StringVar(&chunkSym, "chunksym", "KQ.m@SHFE.rb", "shinny-chunk-mem 量哪个序列")
}

func probeChunkMem(md, tok string) {
	const name = "shinny-chunk-mem"
	if !optIn(name) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
	defer cancel()
	c, _, err := websocket.Dial(ctx, md, &websocket.DialOptions{
		CompressionMode: websocket.CompressionNoContextTakeover,
		HTTPHeader:      http.Header{"User-Agent": {uaSelf}, "Accept": {"application/json"}, "Authorization": {"Bearer " + tok}},
	})
	if err != nil {
		report(name, "FAIL", "连接失败: "+err.Error())
		return
	}
	defer c.CloseNow()
	c.SetReadLimit(512 << 20)
	send := func(v any) error { b, _ := json.Marshal(v); return c.Write(ctx, websocket.MessageText, b) }
	minDur, dayDur := int64(60)*1e9, int64(86400)*1e9
	minKey, dayKey := strconv.FormatInt(minDur, 10), strconv.FormatInt(dayDur, 10)
	snap := map[string]any{}
	pump := func(cond func() bool, limit time.Duration) error {
		dl := time.Now().Add(limit)
		for !cond() {
			if time.Now().After(dl) {
				return fmt.Errorf("等了 %s 没等到", limit)
			}
			rctx, rc := context.WithTimeout(ctx, limit)
			_, msg, rerr := c.Read(rctx)
			rc()
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

	// —— 交易日列表：日线一窗全拿 ——
	send(map[string]any{"aid": "set_chart", "chart_id": "cd", "ins_list": chunkSym, "duration": dayDur, "view_width": 10000, "left_kline_id": 0})
	send(map[string]any{"aid": "peek_message"})
	if err := pump(func() bool {
		ch, ser := obj(snap, "charts", "cd"), obj(snap, "klines", chunkSym, dayKey)
		if ch == nil || ser == nil {
			return false
		}
		ready, _ := ch["ready"].(bool)
		more, _ := ch["more_data"].(bool)
		r, _ := ch["right_id"].(float64)
		l, ok := ser["last_id"].(float64)
		return ready && !more && ok && int64(r) >= int64(l)
	}, 90*time.Second); err != nil {
		report(name, "FAIL", "日线没拉齐: "+err.Error())
		return
	}
	var days []time.Time
	for _, v := range obj(obj(snap, "klines", chunkSym, dayKey), "data") {
		ts, _ := v.(map[string]any)["datetime"].(float64)
		days = append(days, time.Unix(0, int64(ts)).In(cst))
	}
	sort.Slice(days, func(i, j int) bool { return days[i].Before(days[j]) })
	send(map[string]any{"aid": "set_chart", "chart_id": "cd", "ins_list": "", "duration": dayDur, "view_width": 1})
	delete(snap, "klines")
	if chunkSpan+1 > len(days) {
		chunkSpan = len(days) - 1
	}
	base := len(days) - chunkSpan // days[base-1] 是窗起点所需的「上一交易日」
	cut := func(t time.Time) int64 { return t.Add(15*time.Hour + 15*time.Minute).UnixNano() }

	var ms runtime.MemStats
	read := func() runtime.MemStats { runtime.ReadMemStats(&ms); return ms }
	var b strings.Builder
	fmt.Fprintf(&b, "序列 %s · 最近 %d 个交易日（%s…%s）· unsafe.Sizeof(tickflow.Bar{})=%d 字节 · GOGC 默认\n       ",
		chunkSym, chunkSpan, days[base].Format("2006-01-02"), days[len(days)-1].Format("2006-01-02"), unsafe.Sizeof(tickflow.Bar{}))
	st := "PASS"
	chartN := 0
	for _, ns := range strings.Split(chunkDaysFlag, ",") {
		n, _ := strconv.Atoi(strings.TrimSpace(ns))
		if n <= 0 {
			continue
		}
		width := n*600 + 100
		if width > 10000 {
			width = 10000
		}
		type row struct {
			bars, wins             int
			h1, h2, h3, total, sys uint64
			gc                     uint32
			dur                    time.Duration
			lenCap                 string
		}
		var rows []row
		var chunkErr error
		for i := base; i < len(days); i += n {
			j := i + n - 1
			if j >= len(days) {
				j = len(days) - 1
			}
			winStart, winEnd := cut(days[i-1]), cut(days[j])
			runtime.GC()
			s0 := read()
			t0 := time.Now()
			chartN++
			cid := fmt.Sprintf("cm%d", chartN)
			wins := 0
			var got []int64 // 窗内的 id
			left := int64(-1)
			for {
				req := map[string]any{"aid": "set_chart", "chart_id": cid, "ins_list": chunkSym, "duration": minDur, "view_width": width}
				if left < 0 {
					req["focus_datetime"], req["focus_position"] = winStart, 0
				} else {
					req["left_kline_id"] = left
				}
				send(req)
				if chartN == 1 && wins == 0 {
					send(map[string]any{"aid": "peek_message"})
				}
				wantLeft := left
				if err := pump(func() bool {
					ch := obj(snap, "charts", cid)
					if ch == nil {
						return false
					}
					ready, _ := ch["ready"].(bool)
					more, _ := ch["more_data"].(bool)
					l, ok := ch["left_id"].(float64)
					return ready && !more && ok && (wantLeft < 0 || int64(l) == wantLeft)
				}, 120*time.Second); err != nil {
					chunkErr = fmt.Errorf("N=%d 块 %s…%s 第 %d 窗: %w", n, days[i].Format("01-02"), days[j].Format("01-02"), wins+1, err)
					break
				}
				wins++
				ch := obj(snap, "charts", cid)
				l, r := int64(ch["left_id"].(float64)), int64(ch["right_id"].(float64))
				ser := obj(snap, "klines", chunkSym, minKey)
				data := obj(ser, "data")
				last := int64(ser["last_id"].(float64))
				var maxT int64
				for id := l; id <= r; id++ {
					row, ok := data[strconv.FormatInt(id, 10)].(map[string]any)
					if !ok {
						continue
					}
					ts := int64(row["datetime"].(float64))
					if ts >= winStart && ts < winEnd {
						got = append(got, id)
					}
					if ts > maxT {
						maxT = ts
					}
				}
				if maxT >= winEnd || r >= last {
					break
				}
				left = r + 1
			}
			if chunkErr != nil {
				break
			}
			s1 := read()
			// —— 转换 ——
			data := obj(snap, "klines", chunkSym, minKey, "data")
			var bars []tickflow.Bar
			for _, id := range got {
				row := data[strconv.FormatInt(id, 10)].(map[string]any)
				f := func(k string) float64 { x, _ := row[k].(float64); return x }
				ts := int64(f("datetime"))
				k := i
				for k <= j && cut(days[k]) <= ts {
					k++
				}
				y, mo, d := days[k].Date()
				bars = append(bars, tickflow.Bar{
					Ts: ts / 1e6, TsEnd: ts/1e6 + 60000,
					TradingDay: tickflow.TradingDay(y*10000 + int(mo)*100 + d),
					Open:       f("open"), High: f("high"), Low: f("low"), Close: f("close"),
					Volume: f("volume"), OpenInterest: f("close_oi"),
				})
			}
			s2 := read()
			lenCap := fmt.Sprintf("%d/%d", len(bars), cap(bars))
			// —— 释放 ——
			for k := range data {
				delete(data, k)
			}
			send(map[string]any{"aid": "set_chart", "chart_id": cid, "ins_list": "", "duration": minDur, "view_width": 1})
			bars = nil
			runtime.GC()
			s3 := read()
			rows = append(rows, row{bars: len(got), wins: wins, h1: s1.HeapAlloc, h2: s2.HeapAlloc, h3: s3.HeapAlloc,
				total: s3.TotalAlloc - s0.TotalAlloc, sys: s3.Sys, gc: s3.NumGC - s0.NumGC, dur: time.Since(t0), lenCap: lenCap})
		}
		if chunkErr != nil {
			st = "FAIL"
			fmt.Fprintf(&b, "N=%d 中途失败：%v\n       ", n, chunkErr)
			continue
		}
		var maxBars, sumWins int
		var maxH1, maxH2, maxH3, sumTotal, maxSys uint64
		var sumGC uint32
		var sumDur time.Duration
		maxLC := ""
		for _, r := range rows {
			if r.bars > maxBars {
				maxBars, maxLC = r.bars, r.lenCap
			}
			sumWins += r.wins
			if r.h1 > maxH1 {
				maxH1 = r.h1
			}
			if r.h2 > maxH2 {
				maxH2 = r.h2
			}
			if r.h3 > maxH3 {
				maxH3 = r.h3
			}
			if r.sys > maxSys {
				maxSys = r.sys
			}
			sumTotal += r.total
			sumGC += r.gc
			sumDur += r.dur
		}
		mib := func(x uint64) float64 { return float64(x) / (1 << 20) }
		fmt.Fprintf(&b, "N=%-3d %d 块 · 共 %d 窗 · 每块根数 max %d（len/cap %s）· HeapAlloc 峰值 s1 %.1f MiB · s2 %.1f MiB · 释放后 s3 max %.1f MiB · 每块 TotalAlloc 平均 %.1f MiB · Sys max %.1f MiB · GC %d 次 · 每块平均 %.2fs · max 根数×Sizeof(Bar) %.2f MiB\n       ",
			n, len(rows), sumWins, maxBars, maxLC, mib(maxH1), mib(maxH2), mib(maxH3), mib(sumTotal/uint64(len(rows))), mib(maxSys), sumGC,
			sumDur.Seconds()/float64(len(rows)), mib(uint64(maxBars)*uint64(unsafe.Sizeof(tickflow.Bar{}))))
	}
	report(name, st, strings.TrimRight(b.String(), " \n"))
}
