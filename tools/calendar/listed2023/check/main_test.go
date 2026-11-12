package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	tf "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/verified"
)

// 锁定真实产物的目标反例，让破坏时段、首夜或版本的检查器不能只靠正向样本过关。
func TestListedProfileRejectsMutations(t *testing.T) {
	raw, e := os.ReadFile("../../../../calendar/verified/profiles/listed-2023.json")
	if e != nil {
		t.Fatal(e)
	}
	decode := func() calendarFile {
		var v calendarFile
		if e := json.Unmarshal(raw, &v); e != nil {
			t.Fatal(e)
		}
		return v
	}
	if _, e := profile(decode()); e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		name, product, reason string
		day                   int
		change                func(*verified.Record)
	}{
		{"first-night", "ao", "absolute_session", 20230619, func(r *verified.Record) {
			r.Sessions = append([]tf.Session{{Start: instant(20230616, 1260), End: instant(20230616, 1500)}}, r.Sessions...)
		}},
		{"cross-midnight", "ao", "absolute_session", 20230703, func(r *verified.Record) { r.Sessions[0].End -= 120 * 60000 }},
		{"friday-to-sunday", "br", "absolute_session", 20230731, func(r *verified.Record) { r.Sessions[0].Start += 2 * 86400000; r.Sessions[0].End += 2 * 86400000 }},
		{"holiday-open", "ec", "holiday_open", 20231002, func(r *verified.Record) {
			r.Sessions = []tf.Session{{Start: instant(20231002, 540), End: instant(20231002, 615)}}
		}},
		{"publication-date", "ec", "template_version_phase", 20230818, func(r *verified.Record) { r.Template.From = 20230811 }},
		{"nov-publication-date", "ec", "template_version_phase", 20231121, func(r *verified.Record) { r.Template.From = 20231117 }},
		{"phase", "ao", "template_version_phase", 20230703, func(r *verified.Record) { r.Template.Night[0].Start += 60000; r.Template.Night[0].End += 60000 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := decode()
			for i := range v.Series {
				if v.Series[i].Product.Product != tc.product {
					continue
				}
				for j := range v.Series[i].Records {
					r := &v.Series[i].Records[j]
					if int(r.Day) == tc.day {
						tc.change(r)
					}
				}
			}
			_, e := profile(v)
			if e == nil || !strings.Contains(e.Error(), tc.reason) {
				t.Fatalf("want %s got %v", tc.reason, e)
			}
		})
	}
}
