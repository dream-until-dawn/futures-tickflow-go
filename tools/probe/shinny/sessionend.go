package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"strconv"
	"time"
)

var sessionClose = flag.String("session-close", "", "待检查的末根收盘时刻，RFC3339")
var sessionSymbol = flag.String("session-symbol", tailSym, "原始记录中的合约键；默认 rb 主连")

type sessionEndReadout struct {
	first, last                 int64
	changes, repeats, preceding int
	final                       barVal
}

// inspectSessionEnd 不调用本库日历；收盘时刻由执行单明确给定。
func inspectSessionEnd(frames []tailFrame, symbol string, closeMs int64) (sessionEndReadout, error) {
	var r sessionEndReadout
	snap := map[string]any{}
	seenBefore := map[int64]bool{}
	var prev int64
	for _, fr := range frames {
		if prev != 0 && fr.RecvMs < prev {
			return r, fmt.Errorf("接收时刻倒退，不能直接解释延迟")
		}
		prev = fr.RecvMs
		var msg struct {
			Aid  string           `json:"aid"`
			Data []map[string]any `json:"data"`
		}
		if e := json.Unmarshal(fr.Raw, &msg); e != nil {
			return r, e
		}
		if msg.Aid != "rtn_data" {
			continue
		}
		touched := map[string]bool{}
		for _, d := range msg.Data {
			for id := range obj(d, "klines", symbol, strconv.FormatInt(tailDur, 10), "data") {
				touched[id] = true
			}
			merge(snap, d)
		}
		for id := range touched {
			b := obj(snap, "klines", symbol, strconv.FormatInt(tailDur, 10), "data", id)
			stamp := num(b["datetime"])
			if math.IsNaN(stamp) {
				continue
			}
			open := int64(stamp) / 1e6
			if open >= closeMs-5*60000 && open < closeMs-60000 && fr.RecvMs >= open && fr.RecvMs < open+60000 {
				seenBefore[open] = true
			}
			if open != closeMs-60000 {
				continue
			}
			v := barVal{num(b["open"]), num(b["high"]), num(b["low"]), num(b["close"]), num(b["volume"]), num(b["close_oi"]), num(b["open_oi"])}
			for _, n := range []float64{v.o, v.h, v.l, v.c, v.v, v.coi, v.ooi} {
				if math.IsNaN(n) || math.IsInf(n, 0) {
					return r, fmt.Errorf("末根字段不完整或非有限")
				}
			}
			if r.first == 0 {
				r.first, r.last, r.final = fr.RecvMs, fr.RecvMs, v
				continue
			}
			if sameVal(r.final, v) {
				r.repeats++
				continue
			}
			r.final, r.last = v, fr.RecvMs
			r.changes++
		}
	}
	r.preceding = len(seenBefore)
	if r.first == 0 || r.first >= closeMs || r.preceding < 2 || prev < closeMs {
		return r, fmt.Errorf("末根或收盘前推进/收盘覆盖证据不足")
	}
	return r, nil
}

func probeSessionEnd() {
	const name = "shinny-session-end"
	if !optIn(name) {
		return
	}
	c, e := time.Parse(time.RFC3339, *sessionClose)
	if e != nil || c.UnixMilli()%60000 != 0 {
		report(name, "FAIL", "须给分钟对齐的 -session-close RFC3339")
		return
	}
	frames, e := readFrames(*tailIn)
	if e != nil {
		report(name, "FAIL", e.Error())
		return
	}
	r, e := inspectSessionEnd(frames, *sessionSymbol, c.UnixMilli())
	if e != nil {
		report(name, "FAIL", "无结论："+e.Error())
		return
	}
	status := "PASS"
	if r.last-c.UnixMilli() > 4000 {
		status = "FAIL"
	}
	report(name, status, fmt.Sprintf("仅本机接收时刻筛查：L-C=%dms，真改 %d，原样重发 %d，前序根 %d；不含历史对账、时钟偏差和录制完整性验收", r.last-c.UnixMilli(), r.changes, r.repeats, r.preceding))
}
