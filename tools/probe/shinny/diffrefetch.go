package main

// v0.6 片二前置：DIFF 协议下「本地删掉快照里的根，同一连接上再要同一段」服务端还发不发。
//
// 起因：shinny-chunk-mem 第一次把三档 N 放在同一条连接上顺序跑，N=5 那轮每块拉完就删本地 data；
// 接着 N=20 / N=60 请求的时间段与 N=5 重叠，结果每块只收到 1 根 ⇒ 推论：服务端按「客户端已经有了」只发差量。
// 这一条若成立，shinnysource「为省内存删快照」与「同一连接重读同一段」就互斥 —— 设计要知道。
//
// 做法（点名才跑：go run . -only shinny-diff-refetch）：
//
//	连接一：chart r1 focus_datetime＝T、focus_position＝0、view_width＝W ⇒ 数窗里 [left_id,right_id] 收到几根 n1
//	        ⇒ 删掉本地快照的 data、放掉 r1 ⇒ chart r2 同样参数 ⇒ 数 n2
//	        ⇒ 不删本地、chart r3 同样参数 ⇒ 数 n3（对照：本地没删时同一段在快照里是否齐）
//	连接二（新连接，对照组）：chart r4 同样参数 ⇒ 数 n4
//
// 本文件不 import 本库。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
)

func probeDiffRefetch(md, tok string) {
	const name = "shinny-diff-refetch"
	if !optIn(name) {
		return
	}
	const sym = "KQ.m@SHFE.rb"
	const width = 2000
	minDur := int64(60) * 1e9
	minKey := strconv.FormatInt(minDur, 10)
	T := time.Date(2026, 7, 1, 9, 0, 0, 0, cst).UnixNano()

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
	// fetch 发一个 chart，等它就绪，返回 [left,right] 与窗里本地快照实际有的根数。
	fetch := func(ctx context.Context, c *websocket.Conn, snap map[string]any, cid string, first bool) (int64, int64, int, error) {
		send := func(v any) { b, _ := json.Marshal(v); c.Write(ctx, websocket.MessageText, b) }
		send(map[string]any{"aid": "set_chart", "chart_id": cid, "ins_list": sym, "duration": minDur, "view_width": width,
			"focus_datetime": T, "focus_position": 0})
		send(map[string]any{"aid": "peek_message"})
		dl := time.Now().Add(90 * time.Second)
		for time.Now().Before(dl) {
			rctx, rc := context.WithTimeout(ctx, 30*time.Second)
			_, msg, err := c.Read(rctx)
			rc()
			if err != nil {
				return 0, 0, 0, err
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
			if ch := obj(snap, "charts", cid); ch != nil {
				ready, _ := ch["ready"].(bool)
				more, _ := ch["more_data"].(bool)
				l, okl := ch["left_id"].(float64)
				r, okr := ch["right_id"].(float64)
				if ready && !more && okl && okr {
					data := obj(snap, "klines", sym, minKey, "data")
					n := 0
					for id := int64(l); id <= int64(r); id++ {
						if _, ok := data[strconv.FormatInt(id, 10)]; ok {
							n++
						}
					}
					return int64(l), int64(r), n, nil
				}
			}
			send(map[string]any{"aid": "peek_message"})
		}
		return 0, 0, 0, fmt.Errorf("90s 没就绪")
	}
	release := func(ctx context.Context, c *websocket.Conn, cid string) {
		b, _ := json.Marshal(map[string]any{"aid": "set_chart", "chart_id": cid, "ins_list": "", "duration": minDur, "view_width": 1})
		c.Write(ctx, websocket.MessageText, b)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var b strings.Builder
	fmt.Fprintf(&b, "序列 %s 1m · focus_datetime=%s · view_width=%d\n       ", sym, time.Unix(0, T).In(cst).Format("2006-01-02 15:04"), width)
	c1, err := dial(ctx)
	if err != nil {
		report(name, "FAIL", "连接一失败: "+err.Error())
		return
	}
	defer c1.CloseNow()
	snap := map[string]any{}
	l1, r1, n1, err := fetch(ctx, c1, snap, "r1", true)
	if err != nil {
		report(name, "FAIL", "r1: "+err.Error())
		return
	}
	fmt.Fprintf(&b, "连接一 r1（首次）：窗 [%d,%d] 应有 %d 根 · 快照里有 %d 根\n       ", l1, r1, r1-l1+1, n1)
	release(ctx, c1, "r1")
	if ser := obj(snap, "klines", sym, minKey); ser != nil {
		delete(ser, "data")
	}
	l2, r2, n2, err := fetch(ctx, c1, snap, "r2", false)
	if err != nil {
		report(name, "FAIL", "r2: "+err.Error())
		return
	}
	fmt.Fprintf(&b, "连接一 r2（本地删掉 data 之后，同一连接、同样参数）：窗 [%d,%d] 应有 %d 根 · 快照里有 %d 根\n       ", l2, r2, r2-l2+1, n2)
	l3, r3, n3, err := fetch(ctx, c1, snap, "r3", false)
	if err != nil {
		report(name, "FAIL", "r3: "+err.Error())
		return
	}
	fmt.Fprintf(&b, "连接一 r3（本地不再删，同一连接、同样参数）：窗 [%d,%d] 应有 %d 根 · 快照里有 %d 根\n       ", l3, r3, r3-l3+1, n3)
	c2, err := dial(ctx)
	if err != nil {
		report(name, "FAIL", "连接二失败: "+err.Error())
		return
	}
	defer c2.CloseNow()
	snap2 := map[string]any{}
	l4, r4, n4, err := fetch(ctx, c2, snap2, "r4", true)
	if err != nil {
		report(name, "FAIL", "r4: "+err.Error())
		return
	}
	fmt.Fprintf(&b, "连接二 r4（新连接，对照组，同样参数）：窗 [%d,%d] 应有 %d 根 · 快照里有 %d 根", l4, r4, r4-l4+1, n4)
	report(name, "PASS", b.String())
}
