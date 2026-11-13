package proof

import (
	"strings"
	"testing"

	tf "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/verified"
)

func ineHistoricalFixture(t *testing.T, name string) calendarFile {
	t.Helper()
	var v calendarFile
	if err := read("../../../../calendar/verified/profiles/"+name, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

type ineHistoricalMutation struct {
	name, product, reason string
	day                   tf.TradingDay
	change                func(*verified.Record)
}

func checkINEHistoricalMutations(t *testing.T, name string, spec annualSpec, cases []ineHistoricalMutation) {
	t.Helper()
	if _, err := profileAnnual(ineHistoricalFixture(t, name), spec); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := ineHistoricalFixture(t, name)
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
			_, err := profileAnnual(v, spec)
			if err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("want %s got %v", tc.reason, err)
			}
		})
	}
}

func TestINE2023ProfileRejectsMutations(t *testing.T) {
	checkINEHistoricalMutations(t, "ine-lu-2023.json", specINE2023(), []ineHistoricalMutation{
		{"may-publication-not-effective", "lu", "annual_template_version", 20230526, func(r *verified.Record) { r.Template.From = 20230426 }},
		{"aug-publication-not-effective", "lu", "annual_template_version", 20230818, func(r *verified.Record) { r.Template.From = 20230811 }},
		{"nov-publication-not-effective", "lu", "annual_template_version", 20231121, func(r *verified.Record) { r.Template.From = 20231117 }},
		{"borrow-ec-listing-exception", "lu", "annual_absolute_session", 20230818, func(r *verified.Record) { r.Sessions = r.Sessions[1:] }},
		{"friday-to-sunday", "lu", "annual_absolute_session", 20230109, func(r *verified.Record) { r.Sessions[0].Start += 172800000; r.Sessions[0].End += 172800000 }},
		{"first-night", "lu", "annual_absolute_session", 20230103, func(r *verified.Record) {
			r.Sessions = append([]tf.Session{{Start: instant(20221230, 1260), End: instant(20221230, 1380)}}, r.Sessions...)
		}},
	})
}

func TestINE2024ProfileRejectsMutations(t *testing.T) {
	checkINEHistoricalMutations(t, "ine-2024.json", specINE2024(), []ineHistoricalMutation{
		{"borrowed-shfe-version", "lu", "annual_template_version", 20241023, func(r *verified.Record) { r.Template.From = 20241023 }},
		{"dragon-first-night", "lu", "annual_absolute_session", 20240611, func(r *verified.Record) {
			r.Sessions = append([]tf.Session{{Start: instant(20240607, 1260), End: instant(20240607, 1380)}}, r.Sessions...)
		}},
		{"friday-to-sunday", "lu", "annual_absolute_session", 20240108, func(r *verified.Record) { r.Sessions[0].Start += 172800000; r.Sessions[0].End += 172800000 }},
		{"ec-false-night", "ec", "annual_absolute_session", 20240108, func(r *verified.Record) {
			r.Sessions = append([]tf.Session{{Start: instant(20240105, 1260), End: instant(20240105, 1380)}}, r.Sessions...)
		}},
		{"makeup-sunday", "ec", "annual_holiday_open", 20240218, func(r *verified.Record) {
			r.Sessions = []tf.Session{{Start: instant(r.Day, 540), End: instant(r.Day, 615)}}
		}},
	})
	t.Run("missing-last-day", func(t *testing.T) {
		v := ineHistoricalFixture(t, "ine-2024.json")
		v.Series[0].Records = v.Series[0].Records[:365]
		_, err := profileAnnual(v, specINE2024())
		if err == nil || !strings.Contains(err.Error(), "annual_natural_coverage") {
			t.Fatalf("want annual_natural_coverage got %v", err)
		}
	})
}
