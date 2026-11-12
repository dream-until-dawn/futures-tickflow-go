package officialdaily

import (
	"math"
	"strings"
	"testing"
)

func TestOfficialScientificNumbersPreserveLiteral(t *testing.T) {
	for text, want := range map[string]float64{"1.1441791526E7": 11441791.526, "1e+3": 1000, "1.25e-2": 0.0125, "0e999999999": 0, "1,234.50": 1234.5} {
		got, err := Number(text)
		if err != nil || got != want {
			t.Fatalf("%s: %v %v", text, got, err)
		}
	}
	for _, text := range []string{"", "-", "--"} {
		v, err := Number(text)
		if err != nil || !math.IsNaN(v) {
			t.Fatalf("missing %q: %v %v", text, v, err)
		}
	}
	for _, text := range []string{"-1e3", "+1e3", "01e3", "1,000e3", "1e", "1e309", "1e-999", "NaN", "Infinity", "1,00", "0x10"} {
		if _, err := Number(text); err == nil {
			t.Fatalf("invalid %q accepted", text)
		}
	}
	body := strings.Replace(rawJSON(20261103), `"TURNOVER":"360000"`, `"TURNOVER":1.1441791526E7`, 1)
	rows, err := Parse(document(body, "shfe-json", 20261103))
	if err != nil || len(rows) != 1 || rows[0].Fields["turnover"] != "1.1441791526E7" {
		t.Fatalf("raw literal changed: %+v %v", rows, err)
	}
}

func TestOfficialQuantitiesExactBeforeFloat(t *testing.T) {
	for _, tc := range []struct {
		name, literal string
		ok            bool
	}{
		{"scientific", "1.2e1", true}, {"leading-zero", `"00008"`, true}, {"safe-max", "9007199254740991", true}, {"decimal-integer", "12.000", true},
		{"near-integer", "1.00000000000000001", false}, {"fraction", "1.2e-1", false}, {"unsafe", "9007199254740992", false}, {"tiny", "1e-999", false}, {"negative", "-1", false}, {"missing", `"--"`, false}, {"over-budget", `"` + strings.Repeat("0", 4096) + `1"`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := strings.Replace(rawJSON(20261103), `"VOLUME":12`, `"VOLUME":`+tc.literal, 1)
			_, err := Parse(document(body, "shfe-json", 20261103))
			if (err == nil) != tc.ok {
				t.Fatalf("%s accepted=%v: %v", tc.literal, err == nil, err)
			}
		})
	}
}
