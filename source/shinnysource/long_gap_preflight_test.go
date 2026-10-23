package shinnysource

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestGapMarketPreflight 在等待窗口前验证鉴权、名称服务、具体合约的历史通道。
// 只读取最近已结束交易段的五根，不创建 Live，不将凭证打印或落盘。
func TestGapMarketPreflight(t *testing.T) {
	if os.Getenv("TICKFLOW_GAP_CHECK") != "1" {
		t.Skip("TICKFLOW_GAP_CHECK=1 点名才联网")
	}
	env := map[string]string{}
	for _, k := range []string{"INS", "DAYS", "START", "BEGIN", "DROP", "RESUME", "END"} {
		env[k] = os.Getenv("TICKFLOW_GAP_" + k)
	}
	p, err := parseGapPlan(env, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	cfg := liveConfig(t, p.cal)
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	d, err := p.cal.DayOf(p.sym.ProductKey(), p.day)
	if err != nil {
		t.Fatal(err)
	}
	var end int64
	for _, s := range d.Sessions {
		if s.End < time.Now().UnixMilli() && s.End > end {
			end = s.End
		}
	}
	if end == 0 {
		t.Fatal("今日还没有已结束交易段可预检")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	rows, err := c.fetch(ctx, p.sym.Native(), end-5*60000, end, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 5 {
		t.Fatalf("已结束段末五根取回 %d 根", len(rows))
	}
	for i, r := range rows {
		if r.Datetime/1e6 != end-int64(5-i)*60000 {
			t.Fatal("历史标签不连续")
		}
	}
	t.Logf("预检通过：%s，历史五根完整；末标签 %s", p.sym.Native(), fmtMs(rows[4].Datetime/1e6))
	uCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	if u, e := l8Underlying(uCtx); e != nil {
		t.Logf("公开主连映射无结论：%v；本次固定合约，不声称当前主力", e)
	} else {
		t.Logf("公开主连映射 %s；本次固定 %s", u.Native(), p.sym.Native())
	}
}
