package shinnysource

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// TestGapOfflineBundle 走真实Live与本地协议假服务，封存明确标识的虚拟时钟材料。
// 参照帧由假服务模型构造，不声称是外部行情或独立实时参照；Python独立检查一致性。
func TestGapOfflineBundle(t *testing.T) {
	if os.Getenv("TICKFLOW_GAP_OFFLINE") != "1" {
		t.Skip("仅供合成执行单显式演练")
	}
	out := os.Getenv("TICKFLOW_GAP_OUT")
	if out == "" {
		t.Fatal("缺少新证据子目录")
	}
	if err := os.Mkdir(out, 0700); err != nil {
		t.Fatal(err)
	}
	open := func(name string) *json.Encoder {
		f, err := os.OpenFile(filepath.Join(out, name+".jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { f.Close() })
		return json.NewEncoder(f)
	}
	raw, hist, live, controls := open("reference"), open("history"), open("live"), open("events")
	put := func(enc *json.Encoder, value any) {
		if err := enc.Encode(value); err != nil {
			t.Fatal(err)
		}
	}
	start := at2(2026, 9, 4, 9, 0, 0, 0)
	env := map[string]string{"INS": liveSym.Native(), "DAYS": "20260903,20260904"}
	for key, minute := range map[string]int64{"START": 0, "BEGIN": 1, "DROP": 6, "RESUME": 38, "END": 48} {
		env[key] = time.UnixMilli(start + minute*60000).In(time.FixedZone("CST", 8*3600)).Format(time.RFC3339)
	}
	put(controls, env)
	r := newNetRig(t, start, LiveOptions{})
	i := r.id(t, start)
	r.clock.Store(r.ts(i+6) + 10000)
	r.send(span(i, i+6), nil)
	for offset := int64(0); offset < 48; offset++ {
		if offset == 6 {
			closed := make(chan struct{})
			r.fs.push <- pushCmd{drop: true, closed: closed}
			select {
			case <-closed:
			case <-time.After(5 * time.Second):
				t.Fatal("假服务未确认断开")
			}
			r.live.drop()
			put(controls, map[string]any{"event": "socket_closed", "when_ms": start + 6*60000 + 100})
			r.clock.Store(r.ts(i+67) + 10000)
			r.send(span(i+38, i+67), nil)
			put(controls, map[string]any{"event": "resume_next", "when_ms": start + 38*60000 + 200})
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		got, err := r.live.Next(ctx)
		cancel()
		if err != nil || got.Ts != start+offset*60000 {
			t.Fatalf("合成根%d: %+v / %v", offset, got, err)
		}
		b := r.bars[i+offset]
		put(hist, Row{ID: i + offset, Datetime: b.dt, Open: b.open, High: b.high, Low: b.low, Close: b.close, Volume: b.volume, CloseOI: b.closeOI})
		fields := map[string]any{"datetime": b.dt, "open": b.open, "high": b.high, "low": b.low, "close": b.close, "volume": b.volume, "close_oi": b.closeOI}
		recv := got.Ts + 500
		if offset == 0 {
			recv = start + 60000
		}
		put(raw, map[string]any{"conn": 1, "recv_ms": recv, "raw": map[string]any{"aid": "rtn_data", "data": []any{map[string]any{"klines": map[string]any{liveSym.Native(): map[string]any{minuteKey: map[string]any{"data": map[string]any{strconv.FormatInt(i+offset, 10): fields}}}}}}}})
		when := got.Ts + 60000 + 10
		if offset >= 6 && when < start+38*60000+1000+offset {
			when = start + 38*60000 + 1000 + offset
		}
		put(live, l8Out{Ts: got.Ts, Day: int(got.TradingDay), When: when, O: got.Open, H: got.High, L: got.Low, C: got.Close, V: got.Volume, OI: got.OpenInterest})
	}
	put(controls, map[string]any{"event": "capture_end", "when_ms": start + 48*60000 + 10000})
	if push, handshakes := r.counts(); push != 2 || handshakes != 3 {
		t.Fatalf("应实际重连和请求历史，得到%d/%d", push, handshakes)
	}
}
