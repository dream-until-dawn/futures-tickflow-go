package main

// H5 注入（v0.6 片一）：服务端不回数据时，带期限的 Read(ctx) 会不会按时返回、返回之后连接还能不能用。
// 注入法：连上、发一个 set_chart 与第一个 peek_message，收完那一轮之后【不再发 peek_message】——
// 按 DIFF 协议服务端就不会再推 rtn_data ⇒ 下一次 Read 没有数据可读。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/coder/websocket"
)

func probeReadTimeout(md, tok string) {
	const name = "shinny-read-timeout"
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
	bg := context.Background()
	c, _, err := websocket.Dial(bg, md, &websocket.DialOptions{
		CompressionMode: websocket.CompressionNoContextTakeover,
		HTTPHeader:      http.Header{"User-Agent": {uaTqsdk}, "Accept": {"application/json"}, "Authorization": {"Bearer " + tok}},
	})
	if err != nil {
		report(name, "FAIL", "连接失败: "+err.Error())
		return
	}
	defer c.CloseNow()
	c.SetReadLimit(64 << 20)
	send := func(ctx context.Context, v any) error {
		b, _ := json.Marshal(v)
		return c.Write(ctx, websocket.MessageText, b)
	}
	send(bg, map[string]any{"aid": "set_chart", "chart_id": "t", "ins_list": "KQ.m@SHFE.rb", "duration": int64(60) * 1e9, "view_width": 5})
	send(bg, map[string]any{"aid": "peek_message"})

	// 先把服务端主动推的那几条收完：每条读都给 3s 期限，直到读超时为止（那一刻就是「没有数据可读」）
	var b strings.Builder
	got := 0
	var firstTimeout time.Duration
	var firstErr error
	for {
		ctx, cancel := context.WithTimeout(bg, 3*time.Second)
		t0 := time.Now()
		_, _, rerr := c.Read(ctx)
		cancel()
		if rerr != nil {
			firstTimeout, firstErr = time.Since(t0), rerr
			break
		}
		got++
		if got > 50 {
			break
		}
	}
	fmt.Fprintf(&b, "websocket %s · 不再发 peek_message 之前收到 %d 条 · 随后一次 3s 期限的 Read 在 %.2fs 返回：%v\n       ", ver, got, firstTimeout.Seconds(), firstErr)
	fmt.Fprintf(&b, "  errors.Is(err, context.DeadlineExceeded)=%v\n       ", errors.Is(firstErr, context.DeadlineExceeded))

	// 超时之后这条连接还能不能用：发一个 peek_message 再读一次（10s 期限）
	werr := send(bg, map[string]any{"aid": "peek_message"})
	ctx, cancel := context.WithTimeout(bg, 10*time.Second)
	t1 := time.Now()
	_, msg, rerr := c.Read(ctx)
	cancel()
	fmt.Fprintf(&b, "超时之后：Write(peek_message) 错误=%v · 再 Read（10s 期限）%.2fs 返回，错误=%v，收到 %d 字节\n       ", werr, time.Since(t1).Seconds(), rerr, len(msg))

	// 对照：不带期限的 Read 在没数据时会不会自己返回 —— 只等 20s（外层 goroutine 计时，不靠 Read 自己）
	c2, _, err := websocket.Dial(bg, md, &websocket.DialOptions{
		CompressionMode: websocket.CompressionNoContextTakeover,
		HTTPHeader:      http.Header{"User-Agent": {uaTqsdk}, "Accept": {"application/json"}, "Authorization": {"Bearer " + tok}},
	})
	if err == nil {
		defer c2.CloseNow()
		c2.SetReadLimit(64 << 20)
		done := make(chan error, 1)
		go func() {
			for {
				_, _, e := c2.Read(bg) // 不带期限
				if e != nil {
					done <- e
					return
				}
			}
		}()
		select {
		case e := <-done:
			fmt.Fprintf(&b, "对照（不带期限、连上后什么都不发）：Read 自己返回了：%v\n       ", e)
		case <-time.After(20 * time.Second):
			fmt.Fprintf(&b, "对照（不带期限、连上后什么都不发）：20s 内 Read 没有返回 ⇒ 没数据时它会一直等\n       ")
		}
	}
	report(name, "PASS", strings.TrimRight(b.String(), " \n"))
}
