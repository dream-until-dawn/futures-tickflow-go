package officialdaily

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	tf "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/embedded"
)

// TestAnnualOHLCQuarantineIsRequestScoped 用合成年度产物确认隔离粒度，
// 不认证真实历史日历；异常请求整次失败，其他合约和有效日期仍可查询。
func TestAnnualOHLCQuarantineIsRequestScoped(t *testing.T) {
	d := annualDoc(t)
	var envelope map[string]any
	if err := json.Unmarshal(d.Raw, &envelope); err != nil {
		t.Fatal(err)
	}
	var records []Record
	encoded, err := json.Marshal(envelope["records"])
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(encoded, &records); err != nil {
		t.Fatal(err)
	}
	good := records[0]
	bad := good
	bad.Native = "fu2506"
	bad.Fields = map[string]string{}
	for key, value := range good.Fields {
		bad.Fields[key] = value
	}
	// 模拟真实异常的形态：收盘不在最高/最低价格区间内，禁止修平。
	bad.Fields["open"], bad.Fields["high"], bad.Fields["low"], bad.Fields["close"] = "3651", "3651", "3651", "3232"
	neighbor := good
	neighbor.Native, neighbor.Day = bad.Native, 20250103
	envelope["records"] = []Record{bad, good, neighbor}
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	replacement := document(string(raw), d.Format, d.Day)
	replacement.Origin, replacement.Original = d.Origin, d.Original
	replacement.Evidence.Version = d.Evidence.Version
	cal, err := embedded.New([]tf.TradingDay{20241231, 20250102, 20250103})
	if err != nil {
		t.Fatal(err)
	}
	src, err := New(cal, []Document{replacement}, nil, time.Date(2025, 1, 4, 0, 0, 0, 0, time.UTC).UnixMilli())
	if err != nil {
		t.Fatal("异常OHLC不应阻断整个年度产物加载", err)
	}
	badSymbol, err := tf.ParseSymbol("SHFE.fu2506")
	if err != nil {
		t.Fatal(err)
	}
	for _, to := range []tf.TradingDay{20250102, 20250103} {
		bars, err := src.Bars(context.Background(), tf.BarRequest{Symbol: badSymbol, Period: tf.Daily, From: 20250102, To: to})
		if err == nil || bars != nil || !strings.Contains(err.Error(), "OHLC隔离") {
			t.Fatalf("异常日期或跨异常区间未整次拒绝: %+v %v", bars, err)
		}
	}
	for _, row := range []Record{good, neighbor} {
		symbol, err := tf.ParseSymbol(row.Exchange + "." + row.Native)
		if err != nil {
			t.Fatal(err)
		}
		bars, err := src.Bars(context.Background(), tf.BarRequest{Symbol: symbol, Period: tf.Daily, From: row.Day, To: row.Day})
		if err != nil || len(bars) != 1 || bars[0].Close != 3001 {
			t.Fatalf("无关合约或正常日期被误隔离: %+v %v", bars, err)
		}
	}
	attachment := src.Records(badSymbol)
	if len(attachment) != 2 || attachment[0].Fields["close"] != "3232" || attachment[0].Fields["low"] != "3651" {
		t.Fatal("异常原值未完整保留")
	}
}

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
