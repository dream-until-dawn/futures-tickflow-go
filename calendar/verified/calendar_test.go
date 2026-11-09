package verified

import (
	"errors"
	"strings"
	"testing"
	"time"

	tf "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/refdata/evidence"
)

func ref() evidence.Ref {
	return evidence.Ref{ID: "synthetic-session-proof", URL: "https://example.org/session", SHA256: strings.Repeat("0", 64), RetrievedAt: time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)}
}
func ms(s string) int64 {
	v, e := time.Parse(time.RFC3339, s)
	if e != nil {
		panic(e)
	}
	return v.UnixMilli()
}
func series() Series {
	s := Series{Product: tf.ProductKey{Exchange: "CZCE", Product: "PF"}}
	for _, d := range []tf.TradingDay{20261030, 20261031, 20261101, 20261102, 20261103} {
		r := Record{Day: d, Template: tf.SessionTemplate{From: 20261030, To: 20261103, Day: []tf.Session{{Start: 9 * 3600000, End: 15 * 3600000}}, Night: []tf.Session{{Start: 21 * 3600000, End: 23 * 3600000}}}, Evidence: []evidence.Ref{ref()}}
		if d == 20261030 {
			r.Sessions = []tf.Session{{Start: ms("2026-10-30T09:00:00+08:00"), End: ms("2026-10-30T15:00:00+08:00")}}
		}
		if d == 20261102 {
			r.Sessions = []tf.Session{{Start: ms("2026-10-30T21:00:00+08:00"), End: ms("2026-10-30T23:00:00+08:00")}, {Start: ms("2026-11-02T09:00:00+08:00"), End: ms("2026-11-02T15:00:00+08:00")}}
		}
		if d == 20261103 {
			r.Sessions = []tf.Session{{Start: ms("2026-11-03T09:00:00+08:00"), End: ms("2026-11-03T15:00:00+08:00")}}
			r.Template.Night = nil
		}
		s.Records = append(s.Records, r)
	}
	return s
}

// 合成制度fixture验证软件，绝不作为PF历史时段认证证据。
func TestVerifiedCalendarBoundariesAndCopies(t *testing.T) {
	s := series()
	c, e := New([]Series{s})
	if e != nil {
		t.Fatal(e)
	}
	d, e := c.DayAt(s.Product, ms("2026-10-30T21:30:00+08:00"))
	if e != nil || d.Num != 20261102 {
		t.Fatalf("跨周末归属: %+v %v", d, e)
	}
	if _, e = c.DayAt(s.Product, ms("2026-10-30T23:00:00+08:00")); !errors.Is(e, tf.ErrClosed) {
		t.Fatal(e)
	}
	if _, e = c.DayOf(s.Product, 20261031); !errors.Is(e, tf.ErrNotTradingDay) {
		t.Fatal(e)
	}
	if _, e = c.DayOf(s.Product, 20261029); !errors.Is(e, tf.ErrUncovered) {
		t.Fatal(e)
	}
	for _, stamp := range []string{"2026-10-30T08:59:59+08:00", "2026-11-03T15:00:00+08:00", "2026-11-03T21:00:00+08:00"} {
		if _, err := c.DayAt(s.Product, ms(stamp)); !errors.Is(err, tf.ErrUncovered) {
			t.Fatalf("边界外不能报成休市: %s %v", stamp, err)
		}
	}
	if _, e = c.DayOf(tf.ProductKey{Exchange: "CZCE", Product: "PR"}, 20261102); !errors.Is(e, tf.ErrUncovered) {
		t.Fatal(e)
	}
	n := 0
	if e = c.Walk(s.Product, 20261029, 20261103, func(tf.Day) bool { n++; return true }); !errors.Is(e, tf.ErrUncovered) || n != 0 {
		t.Fatalf("部分回调 %d %v", n, e)
	}
	n = 0
	if e = c.Walk(s.Product, 20261030, 20261103, func(tf.Day) bool { n++; return true }); e != nil || n != 3 {
		t.Fatalf("%d %v", n, e)
	}
	s.Records[3].Sessions[0].Start = 0
	d.Sessions[0].Start = 0
	d, e = c.DayOf(s.Product, 20261102)
	if e != nil || d.Sessions[0].Start == 0 {
		t.Fatal("可变切片泄漏")
	}
	tmpl, e := c.Template(s.Product, 20261103)
	if e != nil || len(tmpl.Night) != 0 {
		t.Fatal("未应用历史模板变化")
	}
}
func TestVerifiedCalendarRejectsGapsAndInvalidProof(t *testing.T) {
	for _, which := range []string{"gap", "evidence", "overlap", "date", "template", "crossday"} {
		t.Run(which, func(t *testing.T) {
			s := series()
			switch which {
			case "gap":
				s.Records = append(s.Records[:1], s.Records[2:]...)
			case "evidence":
				s.Records[0].Evidence = nil
			case "overlap":
				s.Records[3].Sessions[1] = s.Records[3].Sessions[0]
			case "date":
				s.Records[0].Day = 20260230
			case "template":
				s.Records[0].Template.From = 20261102
			case "crossday":
				s.Records[0].Sessions[0].End += 86400000
			}
			if _, e := New([]Series{s}); e == nil {
				t.Fatal("坏输入通过")
			}
		})
	}
}
