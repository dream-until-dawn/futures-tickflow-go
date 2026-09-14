package main

// v0.6 片 A 前置（评审方 2026-09-14 点名）：websocket 握手走不走传进去的 *http.Client ——
// 决定 shinnysource 声明 ClientUseHTTP 还是 ClientUseNone。
//
// 依据（读 coder/websocket v1.8.15 源码，推论，本探针要把它变成读数）：
//
//	dial.go:24-27  DialOptions.HTTPClient「is used for the connection」，且「Its Transport must return writable bodies」
//	dial.go:78-83  HTTPClient.Timeout > 0 ⇒ 挪成握手 ctx 的期限，复制一份 client 把 Timeout 置 0
//	dial.go:223    opts.HTTPClient.Do(req) —— 握手就是经这个 client 发的
//
// 四格（点名才跑：go run . -only shinny-client-use）：
//
//	A 基线    不给 HTTPClient（coder 用 http.DefaultClient）
//	B 计数    Transport ＝ 计数包装 → http.Transport
//	C 复刻    Transport ＝ 计数包装 → 限流形状的包装（Wait 用请求自己的 ctx）→ http.Transport，Timeout＝5s
//	          ⇒ 照 sync.go 的 gateCounter 与 source/pacing 的 transport 复刻形状（不 import 本库）；
//	          另等过 Timeout 再开一张新 chart —— 核「握手期限到点」会不会顺手关掉已建好的连接
//	D 反标定  Transport ＝ 计数包装 → 把 resp.Body 包成只读 → http.Transport
//	          ⇒ 这一格【必须红】：它红，才说明「可写的 body」这条要求真被检查、而 B/C 的包装没破坏它
//
// 每格读数：握手成败 · 握手后计数 · 一张 chart 就绪时的根数 · 收完之后计数（有没有额外 HTTP）。
//
// 本文件不 import 本库。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

type cuCounting struct {
	next http.RoundTripper
	n    atomic.Int64
}

func (c *cuCounting) RoundTrip(r *http.Request) (*http.Response, error) {
	c.n.Add(1)
	return c.next.RoundTrip(r)
}

// cuPacingShape 复刻 source/pacing 的 transport：先按请求自己的 ctx 等，再转发。
type cuPacingShape struct {
	next http.RoundTripper
	wait time.Duration
}

func (p *cuPacingShape) RoundTrip(r *http.Request) (*http.Response, error) {
	select {
	case <-time.After(p.wait):
	case <-r.Context().Done():
		return nil, fmt.Errorf("等待被取消：%w", r.Context().Err())
	}
	return p.next.RoundTrip(r)
}

// cuReadOnlyBody 把 resp.Body 包成只有 Read/Close 的值 —— 反标定用。
type cuReadOnlyBody struct{ next http.RoundTripper }

func (t *cuReadOnlyBody) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := t.next.RoundTrip(r)
	if err == nil && resp.Body != nil {
		resp.Body = struct{ io.ReadCloser }{resp.Body}
	}
	return resp, err
}

func probeClientUse(md, tok string) {
	const name = "shinny-client-use"
	if !optIn(name) {
		return
	}
	ver := "?"
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, d := range bi.Deps {
			if d.Path == "github.com/coder/websocket" {
				ver = d.Version
			}
		}
	}
	const sym, sym2 = "KQ.m@SHFE.rb", "KQ.m@SHFE.au"
	minDur := int64(60) * 1e9
	minKey := strconv.FormatInt(minDur, 10)
	hdr := func() http.Header {
		return http.Header{"User-Agent": {uaSelf}, "Accept": {"application/json"}, "Authorization": {"Bearer " + tok}}
	}
	// chart 发一张 view_width=5 的 1m chart，等就绪，返回窗里快照实际有的根数。
	// ⚠️ 第二张 chart 换序列：同一连接上同一序列服务端不补发（probe.md 6.18），用同一序列会把「连接还活着」与「只收到差量」混在一起。
	chart := func(ctx context.Context, c *websocket.Conn, cid, sym string) (int, error) {
		send := func(v any) error { b, _ := json.Marshal(v); return c.Write(ctx, websocket.MessageText, b) }
		if err := send(map[string]any{"aid": "set_chart", "chart_id": cid, "ins_list": sym, "duration": minDur, "view_width": 5}); err != nil {
			return 0, fmt.Errorf("写 set_chart: %w", err)
		}
		snap := map[string]any{}
		for i := 0; i < 50; i++ {
			if err := send(map[string]any{"aid": "peek_message"}); err != nil {
				return 0, fmt.Errorf("写 peek_message: %w", err)
			}
			rctx, rc := context.WithTimeout(ctx, 20*time.Second)
			_, msg, err := c.Read(rctx)
			rc()
			if err != nil {
				return 0, fmt.Errorf("读: %w", err)
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
				l, okl := ch["left_id"].(float64)
				r, okr := ch["right_id"].(float64)
				if ready && okl && okr {
					data := obj(snap, "klines", sym, minKey, "data")
					n := 0
					for id := int64(l); id <= int64(r); id++ {
						if _, ok := data[strconv.FormatInt(id, 10)]; ok {
							n++
						}
					}
					if n > 0 {
						return n, nil
					}
				}
			}
		}
		return 0, fmt.Errorf("50 轮 peek 之后 chart %s 仍未就绪", cid)
	}

	type cell struct {
		label    string
		client   func() (*http.Client, *cuCounting)
		waitPast time.Duration // >0 ⇒ 第一张 chart 之后等这么久再开第二张
	}
	base := func() http.RoundTripper { return http.DefaultTransport.(*http.Transport).Clone() }
	cells := []cell{
		{"A 基线（不给 HTTPClient）", func() (*http.Client, *cuCounting) { return nil, nil }, 0},
		{"B 计数 → http.Transport", func() (*http.Client, *cuCounting) {
			g := &cuCounting{next: base()}
			return &http.Client{Transport: g}, g
		}, 0},
		{"C 计数 → 限流形状 → http.Transport · Timeout=5s", func() (*http.Client, *cuCounting) {
			g := &cuCounting{next: &cuPacingShape{next: base(), wait: 100 * time.Millisecond}}
			return &http.Client{Transport: g, Timeout: 5 * time.Second}, g
		}, 8 * time.Second},
		{"D 反标定：计数 → 只读 body → http.Transport（必须红）", func() (*http.Client, *cuCounting) {
			g := &cuCounting{next: &cuReadOnlyBody{next: base()}}
			return &http.Client{Transport: g}, g
		}, 0},
	}

	var b strings.Builder
	fmt.Fprintf(&b, "websocket %s · UA=本库名 · 序列 %s 1m view_width=5\n       ", ver, sym)
	st := "PASS"
	cnt := func(g *cuCounting) string {
		if g == nil {
			return "—"
		}
		return strconv.FormatInt(g.n.Load(), 10)
	}
	for _, ce := range cells {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		hc, g := ce.client()
		t0 := time.Now()
		c, resp, err := websocket.Dial(ctx, md, &websocket.DialOptions{
			HTTPClient:      hc,
			CompressionMode: websocket.CompressionNoContextTakeover,
			HTTPHeader:      hdr(),
		})
		code := 0
		if resp != nil {
			code = resp.StatusCode
		}
		if err != nil {
			fmt.Fprintf(&b, "%s：握手失败（%.2fs，HTTP %d）· 计数 %s · 错误原文：%v\n       ", ce.label, time.Since(t0).Seconds(), code, cnt(g), err)
			if !strings.HasPrefix(ce.label, "D ") {
				st = "FAIL"
			}
			cancel()
			continue
		}
		c.SetReadLimit(64 << 20)
		afterDial := cnt(g)
		n1, err1 := chart(ctx, c, "c1", sym)
		line := fmt.Sprintf("%s：握手成功（%.2fs，HTTP %d）· 握手后计数 %s · chart c1 %d 根 错误=%v", ce.label, time.Since(t0).Seconds(), code, afterDial, n1, err1)
		if err1 != nil {
			st = "FAIL"
		}
		if ce.waitPast > 0 {
			time.Sleep(ce.waitPast)
			n2, err2 := chart(ctx, c, "c2", sym2)
			line += fmt.Sprintf(" · 等 %v（过了 Timeout）再开 chart c2（%s）%d 根 错误=%v", ce.waitPast, sym2, n2, err2)
			if err2 != nil {
				st = "FAIL"
			}
		}
		line += " · 收完之后计数 " + cnt(g)
		if strings.HasPrefix(ce.label, "D ") {
			line += " ⛔ 反标定格握手成功了 ⇒「可写 body」这一维没被检查，B/C 在这一维上不提供信息"
			st = "FAIL"
		}
		fmt.Fprintf(&b, "%s\n       ", line)
		c.CloseNow()
		cancel()
	}
	report(name, st, strings.TrimRight(b.String(), " \n"))
}
