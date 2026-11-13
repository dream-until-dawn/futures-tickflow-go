package proof

import (
	"strings"
	"testing"

	tf "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/verified"
)

func ine2026Fixture(t *testing.T) calendarFile {
	t.Helper()
	var v calendarFile
	if err := read("../../../../calendar/verified/profiles/ine-2026-to-1104.json", &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// 2026的三段版本必须按公告实施日切换，栏目06-22不是07-06实施日。
func TestINE2026ProfileRejectsMutations(t *testing.T) {
	if _, err := profileAnnual(ine2026Fixture(t), specINE2026()); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, product, reason string
		day                   tf.TradingDay
		change                func(*verified.Record)
	}{
		{"false-first-night", "lu", "annual_absolute_session", 20260105, func(r *verified.Record) {
			r.Sessions = append([]tf.Session{{Start: instant(20251231, 1260), End: instant(20251231, 1380)}}, r.Sessions...)
		}},
		{"june-column-not-effective", "ec", "annual_template_version", 20260706, func(r *verified.Record) { r.Template.From = 20260622 }},
		{"sept-publication-not-effective", "lu", "annual_template_version", 20260914, func(r *verified.Record) { r.Template.From = 20260911 }},
		{"friday-to-sunday", "lu", "annual_absolute_session", 20260706, func(r *verified.Record) { r.Sessions[0].Start += 172800000; r.Sessions[0].End += 172800000 }},
		{"autumn-first-night", "lu", "annual_absolute_session", 20260928, func(r *verified.Record) {
			r.Sessions = append([]tf.Session{{Start: instant(20260924, 1260), End: instant(20260924, 1380)}}, r.Sessions...)
		}},
		{"ec-template-night", "ec", "annual_template_version", 20260105, func(r *verified.Record) { r.Template.Night = []tf.Session{{Start: 1260 * 60000, End: 1380 * 60000}} }},
		{"makeup-sunday", "ec", "annual_holiday_open", 20260104, func(r *verified.Record) {
			r.Sessions = []tf.Session{{Start: instant(r.Day, 540), End: instant(r.Day, 615)}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := ine2026Fixture(t)
			for i := range v.Series {
				if v.Series[i].Product.Product == tc.product {
					for j := range v.Series[i].Records {
						r := &v.Series[i].Records[j]
						if r.Day == tc.day {
							tc.change(r)
						}
					}
				}
			}
			_, err := profileAnnual(v, specINE2026())
			if err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("want %s got %v", tc.reason, err)
			}
		})
	}
	t.Run("future-extension", func(t *testing.T) {
		v := ine2026Fixture(t)
		r := v.Series[0].Records[len(v.Series[0].Records)-1]
		r.Day = 20261105
		r.Template.To = 20261105
		r.Sessions = nil
		v.Series[0].Records = append(v.Series[0].Records, r)
		_, err := profileAnnual(v, specINE2026())
		if err == nil || !strings.Contains(err.Error(), "annual_natural_coverage") {
			t.Fatalf("want annual_natural_coverage got %v", err)
		}
	})
	t.Run("wrong-exchange", func(t *testing.T) {
		v := ine2026Fixture(t)
		v.Series[0].Product.Exchange = tf.SHFE
		_, err := profileAnnual(v, specINE2026())
		if err == nil || !strings.Contains(err.Error(), "annual_product") {
			t.Fatalf("want annual_product got %v", err)
		}
	})
}
