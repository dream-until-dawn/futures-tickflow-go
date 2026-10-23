package shinnysource

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestGapRecordCalibration 验证原始参照录制的结束、连接失败和落盘失败三条路径。
func TestGapRecordCalibration(t *testing.T) {
	for _, mode := range []string{"normal", "connection-failed", "write-failed"} {
		t.Run(mode, func(t *testing.T) {
			fs := newFakeServer(t)
			cfg := fs.config(testCalendar(t), 0)
			cl, e := New(cfg)
			if e != nil {
				t.Fatal(e)
			}
			path := filepath.Join(t.TempDir(), "raw.jsonl")
			f, e := os.Create(path)
			if e != nil {
				t.Fatal(e)
			}
			defer f.Close()
			if mode == "connection-failed" {
				fs.mdDown = true
			}
			if mode == "write-failed" {
				f.Close()
			}
			// 提前排队一帧，确认结束不能使正常录制变成空文件通过。
			fs.push <- pushCmd{data: map[string]any{"quotes": map[string]any{liveSym.Native(): map[string]any{"last_price": 100}}}}
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()
			err := gapRecord(ctx, cl, liveSym.Native(), f)
			if mode != "normal" {
				if err == nil {
					t.Fatal("异常通道未报告错误")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			f.Close()
			r, e := os.Open(path)
			if e != nil {
				t.Fatal(e)
			}
			defer r.Close()
			var rec l8Rec
			if e = json.NewDecoder(r).Decode(&rec); e != nil {
				t.Fatal(e)
			}
			if rec.Conn != 1 || rec.Recv <= 0 || len(rec.Raw) == 0 {
				t.Fatalf("落盘元数据不完整：%+v", rec)
			}
		})
	}
}
