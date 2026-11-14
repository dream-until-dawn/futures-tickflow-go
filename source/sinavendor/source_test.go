package sinavendor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	tf "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/embedded"
	"github.com/dream-until-dawn/futures-tickflow-go/refdata/evidence"
	"github.com/dream-until-dawn/futures-tickflow-go/source/pacing"
	"github.com/dream-until-dawn/futures-tickflow-go/store/segfile"
)

func frozen(t *testing.T, name string) Document {
	t.Helper()
	var receipts []struct {
		Name, URL, SHA256 string
		At                time.Time `json:"received_at"`
	}
	b, e := os.ReadFile("testdata/receipts.json")
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(b, &receipts); e != nil {
		t.Fatal(e)
	}
	for _, r := range receipts {
		if r.Name == name {
			exch := tf.DCE
			code := strings.ToLower(name)
			if name == "SI2308" {
				exch = tf.GFEX
			}
			if name == "RB2410" {
				exch = tf.SHFE
			}
			sym, e := tf.ParseSymbol(exch + "." + code)
			if e != nil {
				t.Fatal(e)
			}
			raw, e := os.ReadFile("testdata/" + name + ".jsonp")
			if e != nil {
				t.Fatal(e)
			}
			return Document{Symbol: sym, Raw: raw, Evidence: evidence.Ref{ID: name, URL: r.URL, SHA256: r.SHA256, RetrievedAt: r.At}}
		}
	}
	t.Fatal("missing receipt")
	return Document{}
}

func rewrite(d Document, b string) Document {
	d.Raw = []byte(b)
	h := sha256.Sum256(d.Raw)
	d.Evidence.SHA256 = hex.EncodeToString(h[:])
	return d
}

func TestSinaVendorFrozenFourAndStrictWrapper(t *testing.T) {
	for _, tc := range []struct {
		name     string
		count    int
		from, to tf.TradingDay
	}{{"C2105", 87, 20210104, 20210519}, {"M2105", 88, 20210104, 20210519}, {"SI2308", 156, 20221222, 20230814}, {"RB2410", 241, 20231017, 20241015}} {
		t.Run(tc.name, func(t *testing.T) {
			d := frozen(t, tc.name)
			rr, e := Parse(d)
			if e != nil {
				t.Fatal(e)
			}
			count := 0
			var first, last tf.TradingDay
			for _, r := range rr {
				if r.Day >= 20210101 {
					count++
					if first == 0 {
						first = r.Day
					}
					last = r.Day
				}
				if r.Identity() != "vendor/sina/request-bound" {
					t.Fatal("identity")
				}
			}
			if count != tc.count || first != tc.from || last != tc.to {
				t.Fatalf("rows=%d from=%d to=%d", count, first, last)
			}
		})
	}
	d := frozen(t, "C2105")
	// URL/正文没有交易所：改变规范交易所仍只绑定C2105请求，不能获得挂牌资格。
	declared := d
	declared.Symbol.Exchange = tf.GFEX
	if rows, e := Parse(declared); e != nil || rows[0].Identity() != "vendor/sina/request-bound" {
		t.Fatalf("request-bound身份 %v", e)
	}
	original := string(d.Raw)
	for _, tc := range []struct {
		name   string
		change func(Document) Document
		want   string
	}{
		{"sha", func(d Document) Document { d.Raw = append(d.Raw, 'x'); return d }, "SHA256"},
		{"url-code", func(d Document) Document {
			d.Evidence.URL = strings.Replace(d.Evidence.URL, "C2105", "M2105", 1)
			return d
		}, "请求合约"},
		{"url-host", func(d Document) Document {
			d.Evidence.URL = strings.Replace(d.Evidence.URL, "stock2.", "other.", 1)
			return d
		}, "URL"},
		{"callback", func(d Document) Document { return rewrite(d, strings.Replace(original, "var _V2105", "var _BAD", 1)) }, "包装"},
		{"tail-script", func(d Document) Document { return rewrite(d, original+"evil();") }, "额外内容"},
		{"compound-field", func(d Document) Document {
			return rewrite(d, strings.Replace(original, `"d":"2020-05-20"`, `"d":"2020-05-20","d|o":"1"`, 1))
		}, "未知字段"},
		{"duplicate-field", func(d Document) Document {
			return rewrite(d, strings.Replace(original, `"d":"2020-05-20"`, `"d":"2020-05-20","d":"2020-05-20"`, 1))
		}, "重复字段"},
		{"wrong-day", func(d Document) Document { return rewrite(d, strings.Replace(original, "2020-05-20", "2020-02-31", 1)) }, "业务日期"},
		{"duplicate-day", func(d Document) Document { return rewrite(d, strings.Replace(original, "2020-05-21", "2020-05-20", 1)) }, "重复或乱序"},
		{"null", func(d Document) Document { return rewrite(d, "var _V21052021_4_12=(null);") }, "未取得数组"},
		{"empty", func(d Document) Document { return rewrite(d, "var _V21052021_4_12=([]);") }, "空数组"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, e := Parse(tc.change(d)); e == nil || !strings.Contains(e.Error(), tc.want) {
				t.Fatalf("target=%s error=%v", tc.want, e)
			}
		})
	}
}

// 这里的日历故意由注入日期构造，仅验证机械链路，绝非历史日历认证。
func mechanical(t *testing.T, days []tf.TradingDay) tf.Calendar {
	t.Helper()
	c, e := embedded.New(days)
	if e != nil {
		t.Fatal(e)
	}
	return c
}

func TestSinaVendorMissingWholeRequestAndQuality(t *testing.T) {
	d := frozen(t, "C2105")
	now := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC).UnixMilli()
	cal := mechanical(t, []tf.TradingDay{20210513, 20210514, 20210517})
	s, e := New(cal, []Document{d}, now)
	if e != nil {
		t.Fatal(e)
	}
	req := tf.BarRequest{Symbol: d.Symbol, Period: tf.Daily, From: 20210513, To: 20210517}
	if b, e := s.Bars(context.Background(), req); e == nil || b != nil || !strings.Contains(e.Error(), "2021-05-14") {
		t.Fatalf("缺日须整体失败 %v %v", b, e)
	}
	if _, e := New(cal, []Document{d, d}, now); e == nil || !strings.Contains(e.Error(), "重复合约日") {
		t.Fatal(e)
	}
	rb := frozen(t, "RB2410")
	cal = mechanical(t, []tf.TradingDay{20240925})
	s, e = New(cal, []Document{rb}, now)
	if e != nil {
		t.Fatal(e)
	}
	req = tf.BarRequest{Symbol: rb.Symbol, Period: tf.Daily, From: 20240925, To: 20240925}
	b, e := s.Bars(context.Background(), req)
	if e != nil || len(b) != 1 || b[0].Close != 3136 || !math.IsNaN(b[0].Settle) || !math.IsNaN(b[0].Turnover) || b[0].Flags != tf.FlagSrcSina {
		t.Fatalf("%+v %v", b, e)
	}
	rr := s.Records(rb.Symbol)
	rr[0].Fields["c"] = "changed"
	if s.Records(rb.Symbol)[0].Fields["c"] == "changed" {
		t.Fatal("alias")
	}
	for _, v := range []string{"1.00000000000000000001", "9007199254740992", "NaN", "-1", "1.5", "0e1000000000"} {
		if _, e := numeric("v", v); e == nil {
			t.Fatalf("量仓坏值通过 %s", v)
		}
	}
	for _, v := range []string{"0", "42", "1e3"} {
		if _, e := numeric("p", v); e != nil {
			t.Fatal(e)
		}
	}
	// 使用独立定制单日原件，保证坏值确实命中请求日。
	for _, tc := range []struct{ field, value, want string }{{"h", "1", "OHLC隔离"}, {"v", "1.0000000000000000001", "量仓"}, {"p", "NaN", "非法非负十进制"}} {
		body := `var _V21052021_4_12=([{"d":"2024-09-25","o":"3100","h":"3200","l":"3000","c":"3136","v":"42","p":"12","s":"0"}]);`
		old := map[string]string{"h": "3200", "v": "42", "p": "12"}[tc.field]
		bad := rewrite(rb, strings.Replace(body, `"`+tc.field+`":"`+old+`"`, `"`+tc.field+`":"`+tc.value+`"`, 1))
		ss, e := New(cal, []Document{bad}, now)
		if e != nil {
			t.Fatal(e)
		}
		if bars, e := ss.Bars(context.Background(), req); e == nil || bars != nil || !strings.Contains(e.Error(), tc.want) {
			t.Fatalf("%s passed %v", tc.field, e)
		}
	}
	s.now = 1
	if _, e = s.Bars(context.Background(), req); e == nil || !strings.Contains(e.Error(), "未完结") {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = s.Bars(ctx, req); e != context.Canceled {
		t.Fatal(e)
	}
}

func TestSinaVendorSyncStoreFeedResume(t *testing.T) {
	d := frozen(t, "M2105")
	cal := mechanical(t, []tf.TradingDay{20210513, 20210514, 20210517})
	now := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC).UnixMilli()
	src, e := New(cal, []Document{d}, now)
	if e != nil {
		t.Fatal(e)
	}
	root := t.TempDir()
	req := tf.SyncRequest{Symbol: d.Symbol, Period: tf.Daily, From: 20210513, To: 20210517}
	want, e := src.Bars(context.Background(), tf.BarRequest{Symbol: d.Symbol, Period: tf.Daily, From: req.From, To: req.To})
	if e != nil {
		t.Fatal(e)
	}
	for round := 0; round < 2; round++ {
		store, tail, e := segfile.Open(root, tf.Daily)
		if e != nil || tail != 0 {
			t.Fatalf("%d %v", tail, e)
		}
		syncer, e := tf.NewSyncer(tf.SyncerConfig{Calendar: cal, Store: store, NewSource: func(*http.Client) tf.Source { return src }, Pacer: pacing.NoPacing(), Timeout: time.Second})
		if e != nil {
			t.Fatal(e)
		}
		report, e := syncer.Sync(context.Background(), req, now)
		if e != nil || !report.Complete() || (round == 0 && report.Bars != 3) || (round == 1 && report.Bars != 0) {
			t.Fatalf("%+v %v", report, e)
		}
		feed, e := tf.NewFeed(store, tf.FeedConfig{Key: d.Symbol.ProductKey(), Calendar: cal, Base: tf.Daily, Rule: tf.AggTradingAxis, From: req.From, To: req.To})
		if e != nil {
			t.Fatal(e)
		}
		count := 0
		for feed.Next() {
			got := feed.View().Bar()
			w := want[count]
			if got.Close != w.Close || got.Volume != w.Volume || got.OpenInterest != w.OpenInterest || got.Flags != tf.FlagSrcSina {
				t.Fatalf("readback %+v %+v", got, w)
			}
			count++
		}
		if e = feed.Err(); e != nil || count != 3 {
			t.Fatalf("%d %v", count, e)
		}
		feed.Close()
		if e = store.Close(); e != nil {
			t.Fatal(e)
		}
	}
}
