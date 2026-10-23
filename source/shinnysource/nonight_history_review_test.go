package shinnysource

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dream-until-dawn/futures-tickflow-go/calendar/embedded"
)

// noNightExpectedOct8 按公告后的日盘时段手列，不从被测日历抽取真值。
func noNightExpectedOct8() []int64 {
	var stamps []int64
	for _, s := range [][4]int{{9, 0, 10, 15}, {10, 30, 11, 30}, {13, 30, 15, 0}} {
		for ts := nnAt(2026, 10, 8, s[0], s[1]); ts < nnAt(2026, 10, 8, s[2], s[3]); ts += 60000 {
			stamps = append(stamps, ts)
		}
	}
	return stamps
}

func checkNoNightOct8(rows []Row) error {
	expected := noNightExpectedOct8()
	if len(rows) != len(expected) {
		return fmt.Errorf("历史根 %d，应 %d", len(rows), len(expected))
	}
	for i, r := range rows {
		if r.Datetime != expected[i]*1e6 {
			return fmt.Errorf("第 %d 根标签不符：%s，应 %s", i, fmtMs(r.Datetime/1e6), fmtMs(expected[i]))
		}
		if i > 0 && r.ID <= rows[i-1].ID {
			return fmt.Errorf("历史 ID 不递增")
		}
	}
	return nil
}

func TestNoNightHistoricalReviewCalibration(t *testing.T) {
	build := func() []Row {
		var rows []Row
		for i, ts := range noNightExpectedOct8() {
			rows = append(rows, Row{ID: int64(i), Datetime: ts * 1e6})
		}
		return rows
	}
	if e := checkNoNightOct8(build()); e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"night", "missing", "duplicate", "wrong-time", "reversed-id"} {
		t.Run(name, func(t *testing.T) {
			r := build()
			switch name {
			case "night":
				r = append([]Row{{Datetime: nnEve * 1e6}}, r...)
			case "missing":
				r = append(r[:10], r[11:]...)
			case "duplicate":
				r[10] = r[9]
			case "wrong-time":
				r[10].Datetime += 60000 * 1e6
			case "reversed-id":
				r[10].ID = -1
			}
			if e := checkNoNightOct8(r); e == nil {
				t.Fatal("异常历史未拒绝")
			}
		})
	}
}

func TestNoNightHistoricalReviewReal(t *testing.T) {
	if os.Getenv("TICKFLOW_NN_HISTORY") != "1" {
		t.Skip("TICKFLOW_NN_HISTORY=1 点名才联网")
	}
	cal, err := embedded.New(nnDays, embedded.NoNightAfter(20260930))
	if err != nil {
		t.Fatal(err)
	}
	cfg := liveConfig(t, cal)
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	out := os.Getenv("TICKFLOW_NN_HISTORY_OUT")
	if out == "" {
		t.Fatal("缺 TICKFLOW_NN_HISTORY_OUT")
	}
	out, err = filepath.Abs(out)
	if err != nil {
		t.Fatal(err)
	}
	root, _ := filepath.Abs(filepath.Join("..", ".."))
	rel, e := filepath.Rel(strings.ToLower(root), strings.ToLower(out))
	if e != nil || rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
		t.Fatal("输出须在仓库外")
	}
	if err = os.Mkdir(out, 0700); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(out, "history.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	rows, err := c.fetch(ctx, "SHFE.rb2701", nnFetchFrom, nnDayClose, viewWidth(600))
	if err != nil {
		t.Fatal(err)
	}
	enc := json.NewEncoder(f)
	for _, r := range rows {
		if err = enc.Encode(r); err != nil {
			t.Fatal(err)
		}
	}
	if err = f.Sync(); err != nil {
		t.Fatal(err)
	}
	if err = checkNoNightOct8(rows); err != nil {
		t.Fatal(err)
	}
	t.Logf("历史复核通过：固定 SHFE.rb2701，查询区间夜盘/假期根 0，10/8 日盘 225 根完整；不构成当晚 Live 或 L9 节点证据；目录 %s", out)
}
