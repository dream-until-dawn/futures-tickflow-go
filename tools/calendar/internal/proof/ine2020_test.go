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

func TestINE2020ProfileRejectsMutations(t *testing.T) {
	checkINEHistoricalMutations(t, "ine-lu-2020-listed.json", specINE2020(), []ineHistoricalMutation{
		{"prelisting-night", "lu", "annual_absolute_session", 20200622, func(r *verified.Record) {
			r.Sessions = append([]tf.Session{{Start: instant(20200619, 1260), End: instant(20200619, 1380)}}, r.Sessions...)
		}},
		{"dragon-first-night", "lu", "annual_absolute_session", 20200629, func(r *verified.Record) {
			r.Sessions = append([]tf.Session{{Start: instant(20200624, 1260), End: instant(20200624, 1380)}}, r.Sessions...)
		}},
		{"friday-to-sunday", "lu", "annual_absolute_session", 20200706, func(r *verified.Record) { r.Sessions[0].Start += 172800000; r.Sessions[0].End += 172800000 }},
		{"copper-listing-not-rule", "lu", "annual_template_version", 20201119, func(r *verified.Record) { r.Template.From = 20201119 }},
	})
	t.Run("prelisting-closed-still-uncovered", func(t *testing.T) {
		v := ineHistoricalFixture(t, "ine-lu-2020-listed.json")
		r := v.Series[0].Records[0]
		r.Day = 20200621
		r.Sessions = nil
		v.Series[0].Records = append([]verified.Record{r}, v.Series[0].Records...)
		_, err := profileAnnual(v, specINE2020())
		if err == nil || !strings.Contains(err.Error(), "annual_natural_coverage") {
			t.Fatalf("want annual_natural_coverage got %v", err)
		}
	})
}

// 合成官方字段只验证软件：固定112基数，不能把整串序号一起平移后继续通过。
func TestINE2020OrdinalBaseControls(t *testing.T) {
	cal, err := profileAnnual(ineHistoricalFixture(t, "ine-lu-2020-listed.json"), specINE2020())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"positive", "missing-base", "wrong-base-day", "shifted-whole-counter", "wrong-anchor", "nonlisting-range", "base-removed-from-spec"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			spec := specINE2020()
			write := func(file string, v any) fileRef {
				t.Helper()
				b, err := json.Marshal(v)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, file), b, 0600); err != nil {
					t.Fatal(err)
				}
				sum := sha256.Sum256(b)
				return fileRef{File: file, Ref: evidence.Ref{ID: "synthetic", URL: "https://example.invalid/software-control", SHA256: hex.EncodeToString(sum[:]), RetrievedAt: time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)}}
			}
			baseDay, base := 20200619, 112
			if name == "wrong-base-day" {
				baseDay = 20200618
			}
			if name == "shifted-whole-counter" {
				base++
			}
			ref := write("synthetic-base.json", map[string]any{"report_date": fmt.Sprint(baseDay), "o_year_num": base, "o_code": "0000"})
			if name != "missing-base" {
				write("annual-base-input.json", ref)
			}
			counts := []int{113, 117, 140, 161, 183, 199, 220, 243}
			var files []fileRef
			for i, day := range spec.ordinalDates {
				n := counts[i]
				if name == "shifted-whole-counter" {
					n++
				}
				if name == "wrong-anchor" && i == 0 {
					n++
				}
				files = append(files, write(fmt.Sprintf("synthetic-%d.json", i), map[string]any{"report_date": fmt.Sprintf("%d", day), "o_year_num": n, "o_code": "0000"}))
			}
			write("annual-ordinal-input.json", files)
			if name == "nonlisting-range" {
				p := spec.products["lu"]
				p.listed = 20200621
				spec.products["lu"] = p
			}
			if name == "base-removed-from-spec" {
				spec.ordinalBase = 0
				spec.ordinalBaseCount = 0
			}
			n, err := annualOrdinals(cal, dir, spec)
			if name == "positive" {
				if err != nil || n != 8 {
					t.Fatalf("positive %d %v", n, err)
				}
				return
			}
			reason := map[string]string{"missing-base": "annual_ordinal_base:", "wrong-base-day": "annual_ordinal_base_identity", "shifted-whole-counter": "annual_ordinal_base_identity", "wrong-anchor": "annual_ordinal_mismatch", "nonlisting-range": "annual_ordinal_scope", "base-removed-from-spec": "annual_ordinal_scope"}[name]
			if err == nil || !strings.Contains(err.Error(), reason) {
				t.Fatalf("want %s got %v", reason, err)
			}
		})
	}
}
