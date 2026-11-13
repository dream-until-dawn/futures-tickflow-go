package proof

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	tf "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/verified"
)

func ine2022Fixture(t *testing.T) calendarFile {
	t.Helper()
	var v calendarFile
	if err := read("../../../../calendar/verified/profiles/ine-lu-2022.json", &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestINE2022ProfileRejectsMutations(t *testing.T) {
	if _, err := profileAnnual(ine2022Fixture(t), specINE2022()); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, reason string
		day          tf.TradingDay
		change       func(*verified.Record)
	}{
		{"first-night", "annual_absolute_session", 20220104, func(r *verified.Record) {
			r.Sessions = append([]tf.Session{{Start: instant(20211231, 1260), End: instant(20211231, 1380)}}, r.Sessions...)
		}},
		{"friday-to-sunday", "annual_absolute_session", 20220110, func(r *verified.Record) { r.Sessions[0].Start += 172800000; r.Sessions[0].End += 172800000 }},
		{"autumn-first-night", "annual_absolute_session", 20220913, func(r *verified.Record) {
			r.Sessions = append([]tf.Session{{Start: instant(20220909, 1260), End: instant(20220909, 1380)}}, r.Sessions...)
		}},
		{"labor-first-night", "annual_absolute_session", 20220505, func(r *verified.Record) {
			r.Sessions = append([]tf.Session{{Start: instant(20220429, 1260), End: instant(20220429, 1380)}}, r.Sessions...)
		}},
		{"invented-rule-start", "annual_template_version", 20220110, func(r *verified.Record) { r.Template.From = 20220101 }},
		{"holiday-open", "annual_holiday_open", 20220504, func(r *verified.Record) {
			r.Sessions = []tf.Session{{Start: instant(r.Day, 540), End: instant(r.Day, 615)}}
		}},
		{"lost-break", "annual_absolute_session", 20220110, func(r *verified.Record) { r.Sessions[1].End = instant(r.Day, 630) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := ine2022Fixture(t)
			for i := range v.Series[0].Records {
				r := &v.Series[0].Records[i]
				if r.Day == tc.day {
					tc.change(r)
				}
			}
			_, err := profileAnnual(v, specINE2022())
			if err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("want %s got %v", tc.reason, err)
			}
		})
	}
	t.Run("wrong-exchange", func(t *testing.T) {
		v := ine2022Fixture(t)
		v.Series[0].Product.Exchange = tf.SHFE
		_, err := profileAnnual(v, specINE2022())
		if err == nil || !strings.Contains(err.Error(), "annual_product") {
			t.Fatalf("want annual_product got %v", err)
		}
	})
}

// CI不持有仓外原件，但必须防止提交的事实文件脱离Go固定审核表。
func TestINEAnnualFactsPins(t *testing.T) {
	for _, tc := range []struct {
		file  string
		spec  annualSpec
		input calendarFile
	}{
		{"ine-2022-facts.json", specINE2022(), ine2022Fixture(t)},
		{"ine-2023-facts.json", specINE2023(), ineHistoricalFixture(t, "ine-lu-2023.json")},
		{"ine-2024-facts.json", specINE2024(), ineHistoricalFixture(t, "ine-2024.json")},
		{"ine-2025-facts.json", specINE2025(), ine2025Fixture(t)},
		{"ine-2026-facts.json", specINE2026(), ine2026Fixture(t)},
	} {
		t.Run(tc.file, func(t *testing.T) {
			b, err := os.ReadFile("../../../../docs/evidence/" + tc.file)
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(b)
			if hex.EncodeToString(sum[:]) != tc.spec.factsSHA || tc.input.FactsSHA != tc.spec.factsSHA {
				t.Fatal("annual_facts_pin")
			}
		})
	}
}
