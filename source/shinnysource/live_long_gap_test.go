package shinnysource

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"
)

// TestLiveLongGapCalibration 在同一实例上标定暂停恢复，不保持旧连接空转。
func TestLiveLongGapCalibration(t *testing.T) {
	for _, gap := range []int64{29, 31} {
		for _, missing := range []bool{false, true} {
			name := strconv.FormatInt(gap, 10)
			if missing {
				name += "/missing-history"
			}
			t.Run(name, func(t *testing.T) {
				m := at2(2026, 9, 4, 9, 0, 0, 0)
				r := newNetRig(t, m, LiveOptions{})
				i := r.id(t, m)
				r.clock.Store(r.ts(i+2) + 10000)
				r.send(span(i, i+2), nil)
				r.next(t, r.ts(i), r.ts(i+1))
				// 队列已排空；drop 同步关闭 socket，再推进假时钟。
				closed := make(chan struct{})
				r.fs.push <- pushCmd{drop: true, closed: closed}
				select {
				case <-closed:
				case <-time.After(5 * time.Second):
					t.Fatal("假服务器未确认停止推送")
				}
				r.live.drop()
				if len(r.live.q) != 0 || r.live.conn != nil {
					t.Fatal("暂停前连接或队列未清理")
				}
				first := i + 2 + gap
				r.clock.Store(r.ts(first+29) + 10000)
				if missing {
					// 历史源与推送源独立：仅删除缺口中的历史根。
					r.fs.mu.Lock()
					rows := append([]fakeBar(nil), r.bars...)
					r.fs.series[liveSym.Native()] = append(rows[:i+3], rows[i+4:]...)
					r.fs.mu.Unlock()
				}
				r.send(span(first, first+29), nil)
				if missing {
					if err := r.nextErr(t); !errors.Is(err, ErrIDGap) && !errors.Is(err, ErrChannelsDisagree) {
						t.Fatalf("缺历史根应报告编号或通道不一致，得到 %v", err)
					}
					return
				}
				for id := i + 2; id < first+29; id++ {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					got, err := r.live.Next(ctx)
					cancel()
					want := r.bars[id]
					if err != nil || got.Ts != r.ts(id) || got.TsEnd != r.ts(id)+60000 || got.TradingDay != 20260904 || got.Open != want.open || got.High != want.high || got.Low != want.low || got.Close != want.close || got.Volume != want.volume || got.OpenInterest != want.closeOI {
						t.Fatalf("根 %d 不一致：%+v / %v", id, got, err)
					}
				}
				if p, h := r.counts(); p != 2 || h != 3 {
					t.Fatalf("连接计数 %d/%d，应为两次推送、一次历史", p, h)
				}
			})
		}
	}
}
