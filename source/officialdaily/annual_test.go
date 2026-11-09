package officialdaily

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func annualDoc(t *testing.T) Document {
	t.Helper()
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	f, e := z.Create("synthetic-workbook.xlsx")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.Write([]byte("synthetic-workbook-data")); e != nil {
		t.Fatal(e)
	}
	if e = z.Close(); e != nil {
		t.Fatal(e)
	}
	origin := document(buf.String(), "", 20250101)
	r := Record{Exchange: "SHFE", Native: "rb2505", Day: 20250102, Fields: map[string]string{"open": "3000", "high": "3010", "low": "2990", "close": "3001", "settle": "3002", "pre_settle": "3003.00", "volume": "1", "open_interest": "2", "turnover": "30000"}, Member: "official2025.1.xlsx", Row: 3, TurnoverUnit: "unknown"}
	body, e := json.Marshal(map[string]any{"schema": "official-annual-decoded-v1", "year": 2025, "original_sha256": origin.Evidence.SHA256, "decoder_sha256": strings.Repeat("1", 64), "records": []Record{r}})
	if e != nil {
		t.Fatal(e)
	}
	d := document(string(body), "annual-decoded-v1", 20250101)
	d.Evidence.Version = strings.Repeat("1", 64)
	d.Origin = origin.Evidence
	d.Original = origin.Raw
	return d
}
func TestAnnualOriginAndArtifactAreBothBound(t *testing.T) {
	d := annualDoc(t)
	records, e := Parse(d)
	if e != nil || len(records) != 1 || records[0].Artifact == nil || records[0].Evidence != d.Origin || records[0].Fields["pre_settle"] != "3003.00" {
		t.Fatalf("%+v %v", records, e)
	}
	for _, kind := range []string{"original", "artifact", "date", "unit", "row", "duplicate"} {
		bad := annualDoc(t)
		switch kind {
		case "original":
			bad.Original = append(bad.Original, '\n') // ZIP仍可读取，必须由原件哈希门禁抓住。
		case "artifact":
			bad.Raw = append(bad.Raw, 'x')
		default:
			var body map[string]any
			if e = json.Unmarshal(bad.Raw, &body); e != nil {
				t.Fatal(e)
			}
			rows := body["records"].([]any)
			r := rows[0].(map[string]any)
			switch kind {
			case "date":
				r["day"] = 20260102
			case "unit":
				r["turnover_unit"] = "yuan"
			case "row":
				r["row"] = 0
			case "duplicate":
				body["records"] = append(rows, r)
			}
			raw, e := json.Marshal(body)
			if e != nil {
				t.Fatal(e)
			}
			replacement := document(string(raw), bad.Format, bad.Day)
			replacement.Origin = bad.Origin
			replacement.Evidence.Version = bad.Evidence.Version
			replacement.Original = bad.Original
			bad = replacement
		}
		if _, e = Parse(bad); e == nil {
			t.Fatal("年包反例通过", kind)
		}
	}
}
