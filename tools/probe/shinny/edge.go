package main

// v0.6 片 A 前置：具体合约 1m 的协议边界 —— shinnysource 的 Bars 在这些输入上要怎么答，得先知道服务端怎么答。
//
// 每格一条新连接（6.18）：set_chart focus_datetime＝T、focus_position＝0、view_width＝2000 ⇒ 最多等 30s，
// 印出：非 rtn_data 的 aid · chart 的全部键值 · klines 序列里除 data 以外的键值 · 窗 [left_id,right_id] 里快照有几根、首末根时刻。
//
//	E1  过期之后的窗          SHFE.rb2605  T＝2026-07-01 09:00
//	E1b 过期合约、寿命内的窗  SHFE.rb2605  T＝2026-03-02 09:00
//	E2  上市之前的窗          SHFE.rb2701  T＝2025-06-02 09:00
//	E3  不存在的合约          SHFE.rb9901 · SHFE.zz2601
//	E4  郑商所三位 / 四位     CZCE.TA701 · CZCE.TA2701  T＝2026-09-01 09:00
//	E5  窗在未来              SHFE.rb2610  T＝2026-12-01 09:00
//
// 点名才跑：go run . -only shinny-edge。本文件不 import 本库。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
)

func probeEdge(md, tok string) {
	const name = "shinny-edge"
	if !optIn(name) {
		return
	}
	minDur := int64(60) * 1e9
	minKey := strconv.FormatInt(minDur, 10)
	at := func(y int, m time.Month, d int) int64 { return time.Date(y, m, d, 9, 0, 0, 0, cst).UnixNano() }
	cells := []struct {
		label, sym string
		T          int64
	}{
		{"E1 过期之后的窗", "SHFE.rb2605", at(2026, 7, 1)},
		{"E1b 过期合约、寿命内的窗", "SHFE.rb2605", at(2026, 3, 2)},
		{"E2 上市之前的窗", "SHFE.rb2701", at(2025, 6, 2)},
		{"E3 不存在的合约", "SHFE.rb9901", at(2026, 9, 1)},
		{"E3 不存在的品种", "SHFE.zz2601", at(2026, 9, 1)},
		{"E4 郑商所三位", "CZCE.TA701", at(2026, 9, 1)},
		{"E4 郑商所四位", "CZCE.TA2701", at(2026, 9, 1)},
		{"E5 窗在未来", "SHFE.rb2610", at(2026, 12, 1)},
	}
	fmtKV := func(m map[string]any, skip string) string {
		var ks []string
		for k := range m {
			if k != skip {
				ks = append(ks, k)
			}
		}
		sort.Strings(ks)
		var parts []string
		for _, k := range ks {
			v, _ := json.Marshal(m[k])
			parts = append(parts, k+"="+string(v))
		}
		return "{" + strings.Join(parts, " ") + "}"
	}
	var b strings.Builder
	for _, ce := range cells {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		c, _, err := websocket.Dial(ctx, md, &websocket.DialOptions{
			CompressionMode: websocket.CompressionNoContextTakeover,
			HTTPHeader:      http.Header{"User-Agent": {uaSelf}, "Accept": {"application/json"}, "Authorization": {"Bearer " + tok}},
		})
		if err != nil {
			fmt.Fprintf(&b, "%s %s：连接失败 %v\n       ", ce.label, ce.sym, err)
			cancel()
			continue
		}
		c.SetReadLimit(64 << 20)
		send := func(v any) { bb, _ := json.Marshal(v); c.Write(ctx, websocket.MessageText, bb) }
		send(map[string]any{"aid": "set_chart", "chart_id": "e", "ins_list": ce.sym, "duration": minDur, "view_width": 2000,
			"focus_datetime": ce.T, "focus_position": 0})
		send(map[string]any{"aid": "peek_message"})
		snap := map[string]any{}
		aids := map[string]int{}
		var other []string
		t0 := time.Now()
		state := "30s 内未就绪"
		msgs := 0
		for time.Since(t0) < 30*time.Second {
			rctx, rc := context.WithTimeout(ctx, 8*time.Second)
			_, msg, rerr := c.Read(rctx)
			rc()
			if rerr != nil {
				state = fmt.Sprintf("读失败（%.1fs）：%v", time.Since(t0).Seconds(), rerr)
				break
			}
			msgs++
			var m struct {
				Aid  string           `json:"aid"`
				Data []map[string]any `json:"data"`
			}
			if json.Unmarshal(msg, &m) != nil {
				other = append(other, "非 JSON："+string(msg[:min(200, len(msg))]))
				continue
			}
			aids[m.Aid]++
			if m.Aid != "rtn_data" {
				other = append(other, string(msg[:min(300, len(msg))]))
				continue
			}
			for _, d := range m.Data {
				merge(snap, d)
			}
			if ch := obj(snap, "charts", "e"); ch != nil {
				ready, _ := ch["ready"].(bool)
				more, _ := ch["more_data"].(bool)
				if ready && !more {
					state = fmt.Sprintf("就绪（%.1fs）", time.Since(t0).Seconds())
					break
				}
			}
			send(map[string]any{"aid": "peek_message"})
		}
		fmt.Fprintf(&b, "%s %s（T=%s）：%s · 消息 %d 条 · aid %v\n       ", ce.label, ce.sym,
			time.Unix(0, ce.T).In(cst).Format("2006-01-02 15:04"), state, msgs, aids)
		for _, o := range other {
			fmt.Fprintf(&b, "  非 rtn_data：%s\n       ", o)
		}
		ch := obj(snap, "charts", "e")
		if ch != nil {
			fmt.Fprintf(&b, "  chart %s\n       ", fmtKV(ch, ""))
		} else {
			fmt.Fprintf(&b, "  chart 不在快照里\n       ")
		}
		ser := obj(snap, "klines", ce.sym, minKey)
		if ser != nil {
			fmt.Fprintf(&b, "  klines 序列（除 data）%s · data 键数 %d\n       ", fmtKV(ser, "data"), len(obj(ser, "data")))
		} else {
			fmt.Fprintf(&b, "  klines 序列不在快照里 · 快照顶层键 %s\n       ", fmtKV(map[string]any{"keys": keysOf(snap)}, ""))
		}
		if ch != nil && ser != nil {
			l, okl := ch["left_id"].(float64)
			r, okr := ch["right_id"].(float64)
			if okl && okr && l >= 0 && r >= l {
				data := obj(ser, "data")
				n := 0
				var first, last int64
				var firstVol, zeroVol int
				for id := int64(l); id <= int64(r); id++ {
					row, ok := data[strconv.FormatInt(id, 10)].(map[string]any)
					if !ok {
						continue
					}
					ts := int64(row["datetime"].(float64))
					if n == 0 {
						first = ts
						if v, _ := row["volume"].(float64); v > 0 {
							firstVol = 1
						}
					}
					last = ts
					if v, _ := row["volume"].(float64); v == 0 {
						zeroVol++
					}
					n++
				}
				fmt.Fprintf(&b, "  窗 [%d,%d] 快照有 %d 根 · 首 %s · 末 %s · 首根有量 %d · 零成交 %d 根\n       ", int64(l), int64(r), n,
					time.Unix(0, first).In(cst).Format("2006-01-02 15:04"), time.Unix(0, last).In(cst).Format("2006-01-02 15:04"), firstVol, zeroVol)
			}
		}
		c.CloseNow()
		cancel()
	}
	report(name, "PASS", strings.TrimRight(b.String(), " \n"))
}

func keysOf(m map[string]any) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
