package proof

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	tf "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/verified"
)

// 每个反例保留合法结构，断言具体业务门禁，防止别的校验掩盖漏检。
func TestAnnual2025ProfileRejectsMutations(t *testing.T) {
	raw, e := os.ReadFile("../../../../calendar/verified/profiles/shfe-2025.json")
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
	if _, e := profileAnnual(decode(), spec2025()); e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		name, product, reason string
		day                   tf.TradingDay
		change                func(*verified.Record)
	}{
		{"false-first-night", "ao", "annual_absolute_session", 20250102, func(r *verified.Record) {
			r.Sessions = append([]tf.Session{{Start: instant(20241231, 1260), End: instant(20241231, 1500)}}, r.Sessions...)
		}},
		{"ad-prelisting-night", "ad", "annual_absolute_session", 20250610, func(r *verified.Record) {
			r.Sessions = append([]tf.Session{{Start: instant(20250609, 1260), End: instant(20250609, 1500)}}, r.Sessions...)
		}},
		{"op-prelisting-night", "op", "annual_absolute_session", 20250910, func(r *verified.Record) {
			r.Sessions = append([]tf.Session{{Start: instant(20250909, 1260), End: instant(20250909, 1380)}}, r.Sessions...)
		}},
		{"published-not-effective", "ao", "annual_template_version", 20250808, func(r *verified.Record) { r.Template.From = 20250708 }},
		{"migration-not-effective", "br", "annual_template_version", 20250808, func(r *verified.Record) { r.Template.From = 20250807 }},
		{"lost-midnight-hour", "ad", "annual_absolute_session", 20250616, func(r *verified.Record) { r.Sessions[0].End -= 7200000 }},
		{"friday-to-sunday", "ao", "annual_absolute_session", 20250106, func(r *verified.Record) { r.Sessions[0].Start += 172800000; r.Sessions[0].End += 172800000 }},
		{"holiday-open", "br", "annual_holiday_open", 20251001, func(r *verified.Record) {
			r.Sessions = []tf.Session{{Start: instant(20251001, 540), End: instant(20251001, 615)}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := decode()
			for i := range v.Series {
				if v.Series[i].Product.Product != tc.product {
					continue
				}
				for j := range v.Series[i].Records {
					r := &v.Series[i].Records[j]
					if r.Day == tc.day {
						tc.change(r)
					}
				}
			}
			_, e := profileAnnual(v, spec2025())
			if e == nil || !strings.Contains(e.Error(), tc.reason) {
				t.Fatalf("want %s got %v", tc.reason, e)
			}
		})
	}
	t.Run("prelisting-closed-is-uncovered", func(t *testing.T) {
		v := decode()
		s := &v.Series[2]
		first := s.Records[0]
		first.Day = 20250609
		first.Template.From = 20250609
		first.Sessions = nil
		s.Records = append([]verified.Record{first}, s.Records...)
		_, e := profileAnnual(v, spec2025())
		if e == nil || !strings.Contains(e.Error(), "annual_natural_coverage") {
			t.Fatalf("prelisting scope: %v", e)
		}
	})
	t.Run("unknown-product", func(t *testing.T) {
		v := decode()
		v.Series[0].Product = tf.ProductKey{Exchange: tf.INE, Product: "lu"}
		_, e := profileAnnual(v, spec2025())
		if e == nil || !strings.Contains(e.Error(), "annual_product") {
			t.Fatalf("unqualified: %v", e)
		}
	})
}
