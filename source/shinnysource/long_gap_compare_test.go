package shinnysource

import "testing"

// TestGapCompareCalibration 的参照含末尾当前根，只有前 48 根可算已完结。
func TestGapCompareCalibration(t *testing.T) {
	start := at2(2026, 10, 23, 13, 30, 0, 0)
	p := gapPlan{start: start, end: start + 48*60000, drop: start + 6*60000, resume: start + 38*60000, day: 20261023}
	build := func() (map[int]*l8Conn, []Row, []l8Out) {
		c := &l8Conn{final: map[int64]Row{}, maxID: 48}
		var hist []Row
		var live []l8Out
		for id := int64(0); id <= 48; id++ {
			r := Row{ID: id, Datetime: (start + id*60000) * 1e6, Open: 100, High: 102, Low: 99, Close: 101, Volume: 2, CloseOI: 30}
			c.final[id] = r
			if id < 48 {
				hist = append(hist, r)
				live = append(live, l8Out{Ts: r.Datetime / 1e6, Day: int(p.day), O: r.Open, H: r.High, L: r.Low, C: r.Close, V: r.Volume, OI: r.CloseOI})
			}
		}
		return map[int]*l8Conn{1: c}, hist, live
	}
	c, h, l := build()
	if e := gapCompare(p, c, h, l); e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"empty", "missing-reference", "reference-price", "missing-history", "duplicate-output", "output-price", "wrong-day", "no-recovery"} {
		t.Run(name, func(t *testing.T) {
			c, h, l := build()
			switch name {
			case "empty":
				l = nil
			case "missing-reference":
				delete(c[1].final, 15)
			case "reference-price":
				r := c[1].final[15]
				r.Close++
				c[1].final[15] = r
			case "missing-history":
				h = append(h[:15], h[16:]...)
			case "duplicate-output":
				l[15] = l[14]
			case "output-price":
				l[15].C++
			case "wrong-day":
				l[15].Day++
			case "no-recovery":
				l = l[:6]
			}
			if e := gapCompare(p, c, h, l); e == nil {
				t.Fatal("错误证据被接受")
			}
		})
	}
}
