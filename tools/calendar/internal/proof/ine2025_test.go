package proof

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tf "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/verified"
	"github.com/dream-until-dawn/futures-tickflow-go/refdata/evidence"
)

func ine2025Fixture(t *testing.T) calendarFile {
	t.Helper()
	var v calendarFile
	if err := read("../../../../calendar/verified/profiles/ine-2025.json", &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// 反例保持合法结构，且断言具体拒绝原因，避免其他守卫遮住失效校验。
func TestINE2025ProfileRejectsMutations(t *testing.T) {
	if _, err := profileAnnual(ine2025Fixture(t), specINE2025()); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, product, reason string
		day                   tf.TradingDay
		change                func(*verified.Record)
	}{
		{"first-night", "lu", "annual_absolute_session", 20250102, func(r *verified.Record) {
			r.Sessions = append([]tf.Session{{Start: instant(20241231, 1260), End: instant(20241231, 1380)}}, r.Sessions...)
		}},
		{"friday-to-sunday", "lu", "annual_absolute_session", 20250106, func(r *verified.Record) { r.Sessions[0].Start += 172800000; r.Sessions[0].End += 172800000 }},
		{"spring-first-night", "lu", "annual_absolute_session", 20250205, func(r *verified.Record) {
			r.Sessions = append([]tf.Session{{Start: instant(20250127, 1260), End: instant(20250127, 1380)}}, r.Sessions...)
		}},
		{"ec-false-night", "ec", "annual_absolute_session", 20250106, func(r *verified.Record) {
			r.Sessions = append([]tf.Session{{Start: instant(20250103, 1260), End: instant(20250103, 1380)}}, r.Sessions...)
		}},
		{"ec-template-night", "ec", "annual_template_version", 20250106, func(r *verified.Record) { r.Template.Night = []tf.Session{{Start: 1260 * 60000, End: 1380 * 60000}} }},
		{"makeup-sunday", "ec", "annual_holiday_open", 20250126, func(r *verified.Record) {
			r.Sessions = []tf.Session{{Start: instant(r.Day, 540), End: instant(r.Day, 615)}}
		}},
		{"lost-break", "ec", "annual_absolute_session", 20250106, func(r *verified.Record) { r.Sessions[0].End = instant(r.Day, 630) }},
		{"invented-rule-start", "ec", "annual_template_version", 20250106, func(r *verified.Record) { r.Template.From = 20250101 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := ine2025Fixture(t)
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
			_, err := profileAnnual(v, specINE2025())
			if err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("want %s got %v", tc.reason, err)
			}
		})
	}
	t.Run("wrong-exchange", func(t *testing.T) {
		v := ine2025Fixture(t)
		v.Series[0].Product.Exchange = tf.SHFE
		_, err := profileAnnual(v, specINE2025())
		if err == nil || !strings.Contains(err.Error(), "annual_product") {
			t.Fatalf("want annual_product got %v", err)
		}
	})
	t.Run("missing-last-day", func(t *testing.T) {
		v := ine2025Fixture(t)
		v.Series[0].Records = v.Series[0].Records[:364]
		_, err := profileAnnual(v, specINE2025())
		if err == nil || !strings.Contains(err.Error(), "annual_natural_coverage") {
			t.Fatalf("want annual_natural_coverage got %v", err)
		}
	})
}

func TestINE2025RejectsSelfRehashedFacts(t *testing.T) {
	b := []byte(`{"sources":{}}`)
	sum := sha256.Sum256(b)
	v := ine2025Fixture(t)
	v.FactsSHA = hex.EncodeToString(sum[:])
	p := filepath.Join(t.TempDir(), "facts.json")
	if err := os.WriteFile(p, b, 0600); err != nil {
		t.Fatal(err)
	}
	if err := bindAnnual(v, sourceLock{}, p, t.TempDir(), specINE2025()); err == nil || !strings.Contains(err.Error(), "annual_facts_pin") {
		t.Fatalf("want annual_facts_pin got %v", err)
	}
}

// 合成序号只验证软件守卫；真实原件交叉核查单独保存在批次证据目录。
func TestINE2025OrdinalControls(t *testing.T) {
	cal, err := profileAnnual(ine2025Fixture(t), specINE2025())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"positive", "rehashed-counter", "missing-anchor", "wrong-date", "bad-code"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			var files []fileRef
			counts := []int{1, 18, 36, 57, 78, 97, 117, 140, 161, 183, 200, 220, 243}
			for i, day := range specINE2025().ordinalDates {
				n, code := counts[i], "0000"
				if i == 0 {
					switch name {
					case "rehashed-counter":
						n++
					case "wrong-date":
						day++
					case "bad-code":
						code = "9999"
					}
				}
				b, _ := json.Marshal(map[string]any{"report_date": fmt.Sprintf("%d", day), "o_year_num": n, "o_code": code})
				sum := sha256.Sum256(b)
				file := fmt.Sprintf("synthetic-%d.json", i)
				if err := os.WriteFile(filepath.Join(dir, file), b, 0600); err != nil {
					t.Fatal(err)
				}
				files = append(files, fileRef{File: file, Ref: evidence.Ref{ID: "synthetic", URL: "https://example.invalid/software-control", SHA256: hex.EncodeToString(sum[:]), RetrievedAt: time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)}})
			}
			if name == "missing-anchor" {
				files = files[:12]
			}
			b, _ := json.Marshal(files)
			if err := os.WriteFile(filepath.Join(dir, "annual-ordinal-input.json"), b, 0600); err != nil {
				t.Fatal(err)
			}
			n, err := annualOrdinals(cal, dir, specINE2025())
			if name == "positive" {
				if err != nil || n != 26 {
					t.Fatalf("positive %d %v", n, err)
				}
				return
			}
			reason := map[string]string{"rehashed-counter": "annual_ordinal_mismatch", "missing-anchor": "annual_ordinal_anchors", "wrong-date": "annual_ordinal_identity", "bad-code": "ordinal_status"}[name]
			if err == nil || !strings.Contains(err.Error(), reason) {
				t.Fatalf("want %s got %v", reason, err)
			}
		})
	}
}
