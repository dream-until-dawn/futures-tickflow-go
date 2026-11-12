package proof

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	tf "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/verified"
)

func TestAnnual2024ProfileRejectsMutations(t *testing.T) {
	raw, e := os.ReadFile("../../../../calendar/verified/profiles/shfe-2024.json")
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
	if _, e := profile2024(decode()); e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		name, reason string
		day          tf.TradingDay
		change       func(*verified.Record)
	}{
		{"false-first-night", "annual_absolute_session", 20240102, func(r *verified.Record) {
			r.Sessions = append([]tf.Session{{Start: instant(20231229, 1260), End: instant(20231229, 1500)}}, r.Sessions...)
		}},
		{"published-not-effective", "annual_template_version", 20241023, func(r *verified.Record) { r.Template.From = 20240823 }},
		{"friday-to-sunday", "annual_absolute_session", 20240108, func(r *verified.Record) { r.Sessions[0].Start += 172800000; r.Sessions[0].End += 172800000 }},
		{"holiday-open", "annual_holiday_open", 20240212, func(r *verified.Record) {
			r.Sessions = []tf.Session{{Start: instant(20240212, 540), End: instant(20240212, 615)}}
		}},
		{"lost-midnight-hour", "annual_absolute_session", 20240108, func(r *verified.Record) { r.Sessions[0].End -= 7200000 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := decode()
			for i := range v.Series {
				if v.Series[i].Product.Product != "ao" {
					continue
				}
				for j := range v.Series[i].Records {
					r := &v.Series[i].Records[j]
					if r.Day == tc.day {
						tc.change(r)
					}
				}
			}
			_, e := profile2024(v)
			if e == nil || !strings.Contains(e.Error(), tc.reason) {
				t.Fatalf("want %s got %v", tc.reason, e)
			}
		})
	}
	t.Run("missing-leap-day", func(t *testing.T) {
		v := decode()
		r := v.Series[0].Records
		for i, x := range r {
			if x.Day == 20240229 {
				v.Series[0].Records = append(r[:i], r[i+1:]...)
				break
			}
		}
		_, e := profile2024(v)
		if e == nil || !strings.Contains(e.Error(), "annual_structure: verified: SHFE.ao 有缺日/乱序") {
			t.Fatalf("missing leap day: %v", e)
		}
	})
	t.Run("unqualified-INE", func(t *testing.T) {
		v := decode()
		v.Series[0].Product = tf.ProductKey{Exchange: tf.INE, Product: "ec"}
		_, e := profile2024(v)
		if e == nil || !strings.Contains(e.Error(), "annual_product") {
			t.Fatalf("unqualified profile: %v", e)
		}
	})
}
