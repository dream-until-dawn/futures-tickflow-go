package shinnysource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// gapRecord 连续记录参照帧；出现非预定结束的读错即失败，不自动重连掩盖缺口。
func gapRecord(ctx context.Context, c *Client, ins string, f *os.File) error {
	conn, err := c.dial(ctx)
	if err != nil {
		return err
	}
	defer conn.CloseNow()
	for _, rq := range []map[string]any{{"aid": "set_chart", "chart_id": "gap-reference", "ins_list": ins, "duration": minuteNs, "view_width": liveWidth}, {"aid": "subscribe_quote", "ins_list": ins}, {"aid": "peek_message"}} {
		b, _ := json.Marshal(rq)
		if err = conn.Write(ctx, websocket.MessageText, b); err != nil {
			return err
		}
	}
	enc := json.NewEncoder(f)
	for {
		_, b, e := conn.Read(ctx)
		if e != nil {
			if ctx.Err() != nil {
				return nil
			}
			return e
		}
		if e = enc.Encode(l8Rec{Conn: 1, Recv: time.Now().UnixMilli(), Raw: b}); e != nil {
			return e
		}
		if strings.Contains(string(b), `"rtn_data"`) {
			if e = conn.Write(ctx, websocket.MessageText, []byte(`{"aid":"peek_message"}`)); e != nil {
				if ctx.Err() != nil {
					return nil
				}
				return e
			}
		}
	}
}

// TestLiveLongGapReal 仅在显式启用时联网；不复用九月固定日历入口。
func TestLiveLongGapReal(t *testing.T) {
	if os.Getenv("TICKFLOW_GAP_REAL") != "1" {
		t.Skip("TICKFLOW_GAP_REAL=1 点名才联网")
	}
	env := map[string]string{}
	for _, k := range []string{"INS", "DAYS", "START", "BEGIN", "DROP", "RESUME", "END"} {
		env[k] = os.Getenv("TICKFLOW_GAP_" + k)
	}
	p, err := parseGapPlan(env, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if dl, ok := t.Deadline(); ok && dl.Before(time.UnixMilli(p.end).Add(2*time.Minute)) {
		t.Fatal("测试期限不足，使用 -timeout 0")
	}
	out := os.Getenv("TICKFLOW_GAP_OUT")
	if out == "" {
		t.Fatal("缺 TICKFLOW_GAP_OUT")
	}
	out, err = filepath.Abs(out)
	if err != nil {
		t.Fatal(err)
	}
	repo, _ := filepath.Abs(filepath.Join("..", ".."))
	if rel, e := filepath.Rel(strings.ToLower(repo), strings.ToLower(out)); e != nil || rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
		t.Fatal("输出必须在仓库外")
	}
	cfg := liveConfig(t, p.cal)
	// Mkdir 不递归：要求父目录存在且证据目录不存在，拒绝覆盖旧数据。
	if err = os.Mkdir(out, 0700); err != nil {
		t.Fatal(err)
	}
	open := func(name string) *os.File {
		f, e := os.OpenFile(filepath.Join(out, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { f.Close() })
		return f
	}
	raw, delivered, events, historical := open("reference.jsonl"), open("live.jsonl"), open("events.jsonl"), open("history.jsonl")
	log := json.NewEncoder(events)
	event := func(kind string) {
		if e := log.Encode(map[string]any{"event": kind, "when_ms": time.Now().UnixMilli()}); e != nil {
			t.Fatal(e)
		}
	}
	if e := log.Encode(env); e != nil {
		t.Fatal(e)
	}
	for time.Now().UnixMilli() < p.begin {
		time.Sleep(time.Second)
	}
	if time.Now().UnixMilli() > p.begin+5000 {
		t.Fatal("起跑晚于 BEGIN 五秒，拒绝错位运行")
	}
	mk := func() (*Client, *l8Counter) {
		count := &l8Counter{next: http.DefaultTransport}
		c := cfg
		c.HTTPClient = &http.Client{Timeout: 30 * time.Second, Transport: count}
		cl, e := New(c)
		if e != nil {
			t.Fatal(e)
		}
		return cl, count
	}
	ref, _ := mk()
	subject, count := mk()
	// END 是历史对账的右开边界；再观察十秒，给末根收盘确认留出余量。
	captureEnd := p.end + 10000
	ctx, cancel := context.WithDeadline(context.Background(), time.UnixMilli(captureEnd))
	defer cancel()
	rawDone := make(chan error, 1)
	var rawWG sync.WaitGroup
	rawWG.Add(1)
	go func() { defer rawWG.Done(); rawDone <- gapRecord(ctx, ref, p.sym.Native(), raw) }()
	defer func() { cancel(); rawWG.Wait() }()
	live, err := subject.Live(p.sym, p.start, LiveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	enc := json.NewEncoder(delivered)
	var outputs []l8Out
	paused := false
	for time.Now().UnixMilli() < captureEnd {
		if !paused && time.Now().UnixMilli() >= p.drop {
			// 已交付队列排空后才能暂停；期限到达不再从 socket 取帧。
			for len(live.q) > 0 {
				b, e := live.Next(context.Background())
				if e != nil {
					t.Fatal(e)
				}
				o := l8Out{When: time.Now().UnixMilli(), Ts: b.Ts, Day: int(b.TradingDay), O: b.Open, H: b.High, L: b.Low, C: b.Close, V: b.Volume, OI: b.OpenInterest}
				if e = enc.Encode(o); e != nil {
					t.Fatal(e)
				}
				outputs = append(outputs, o)
			}
			live.drop()
			event("socket_closed")
			for time.Now().UnixMilli() < p.resume {
				time.Sleep(time.Second)
			}
			paused = true
			event("resume_next")
		}
		until := captureEnd
		if !paused {
			until = p.drop
		}
		nctx, stop := context.WithDeadline(ctx, time.UnixMilli(until))
		b, e := live.Next(nctx)
		stop()
		if errors.Is(e, context.DeadlineExceeded) {
			continue
		}
		if e != nil {
			t.Fatal(e)
		}
		o := l8Out{When: time.Now().UnixMilli(), Ts: b.Ts, Day: int(b.TradingDay), O: b.Open, H: b.High, L: b.Low, C: b.Close, V: b.Volume, OI: b.OpenInterest}
		if e = enc.Encode(o); e != nil {
			t.Fatal(e)
		}
		outputs = append(outputs, o)
	}
	live.Close()
	cancel()
	if err = <-rawDone; err != nil {
		t.Fatalf("参照断开，无结论：%v", err)
	}
	event("capture_end")
	hctx, stop := context.WithTimeout(context.Background(), time.Minute)
	defer stop()
	hist, err := ref.fetch(hctx, p.sym.Native(), p.start, p.end, viewWidth(int((p.end-p.start)/60000)))
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range hist {
		if e := json.NewEncoder(historical).Encode(row); e != nil {
			t.Fatal(e)
		}
	}
	if err = raw.Sync(); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(raw.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var recs []l8Rec
	dec := json.NewDecoder(f)
	for dec.More() {
		var r l8Rec
		if e := dec.Decode(&r); e != nil {
			t.Fatal(e)
		}
		recs = append(recs, r)
	}
	conns, err := l8Parse(recs, p.sym.Native())
	if err != nil {
		t.Fatal(err)
	}
	if err = gapCompare(p, conns, hist, outputs); err != nil {
		t.Fatal(err)
	}
	if live.conns != 2 || count.ws.Load() < 3 {
		t.Fatalf("未证明同一实例重连并请求历史：推送 %d、握手 %d", live.conns, count.ws.Load())
	}
	t.Logf("长缺口通过：%s，输出 %d 根，推送 %d，握手 %d，目录 %s", p.sym.Native(), len(outputs), live.conns, count.ws.Load(), out)
}

// gapCompare 校验全区间与缺口参照覆盖；空记录不能通过。
func gapCompare(p gapPlan, conns map[int]*l8Conn, hist []Row, outputs []l8Out) error {
	r := l8Analyze(conns, hist, outputs)
	want := int((p.end - p.start) / 60000)
	if len(hist) != want || len(outputs) != want || r.r3Compared != want || r.r3IDDiff != 0 || len(r.r3ValDiff) != 0 || !r.r4Contiguous || r.r4NotInHist != 0 || len(r.r4Diff) != 0 {
		return fmt.Errorf("全区间不一致：历史 %d 输出 %d 参照 %d，期望 %d；%+v", len(hist), len(outputs), r.r3Compared, want, r)
	}
	gap := 0
	for i, o := range outputs {
		if o.Ts != p.start+int64(i)*60000 || o.Day != int(p.day) {
			return fmt.Errorf("时间或交易日不一致")
		}
		if o.Ts >= p.drop && o.Ts+60000 <= p.resume {
			gap++
		}
	}
	if gap < 31 {
		return fmt.Errorf("实际缺口根不足 31：%d", gap)
	}
	return nil
}
