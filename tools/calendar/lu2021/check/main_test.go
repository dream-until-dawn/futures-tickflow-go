package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	tf "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/refdata/evidence"
)

// 用锁定的实际历史产物测试检查器；反例不修改磁盘原件。
func TestLUProfileRejectsTargetedMutations(t *testing.T) {
	raw, err := os.ReadFile("../../../../calendar/verified/profiles/ine-lu-2021.json")
	if err != nil {
		t.Fatal(err)
	}
	decode := func() envelope {
		var value envelope
		if err := json.Unmarshal(raw, &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	if _, n, err := profile(decode()); err != nil || n != 243 {
		t.Fatalf("positive n=%d: %v", n, err)
	}
	for _, tc := range []struct {
		name   string
		day    tf.TradingDay
		reason string
		change func(*envelope, int)
	}{
		{"missing-day", 20211231, "profile_identity", func(v *envelope, i int) { v.Series[0].Records = v.Series[0].Records[:i] }},
		{"holiday-night", 20210218, "night_presence", func(v *envelope, i int) {
			r := &v.Series[0].Records[i]
			r.Sessions = append([]tf.Session{{Start: at(20210210, 1260), End: at(20210210, 1380)}}, r.Sessions...)
		}},
		{"holiday-open", 20210212, "closed_day_opened", func(v *envelope, i int) {
			v.Series[0].Records[i].Sessions = []tf.Session{{Start: at(20210212, 540), End: at(20210212, 615)}}
		}},
		{"friday-mapping", 20210111, "absolute_session", func(v *envelope, i int) {
			r := &v.Series[0].Records[i]
			r.Sessions[0].Start += 2 * 86400000
			r.Sessions[0].End += 2 * 86400000
		}},
		{"template-phase", 20210728, "nominal_template", func(v *envelope, i int) {
			r := &v.Series[0].Records[i]
			r.Template.Night[0].Start += 60000
			r.Template.Night[0].End += 60000
		}},
		{"version", 20210728, "version_partition", func(v *envelope, i int) { v.Series[0].Records[i].Template.From = 20210610 }},
		{"empty-evidence", 20210728, "calendar_structure", func(v *envelope, i int) { v.Series[0].Records[i].Evidence = []evidence.Ref{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := decode()
			for i, r := range v.Series[0].Records {
				if r.Day == tc.day {
					tc.change(&v, i)
					break
				}
			}
			if _, _, err := profile(v); err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("want %s, got %v", tc.reason, err)
			}
		})
	}
}
