package officialdaily

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	tf "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/embedded"
	"github.com/dream-until-dawn/futures-tickflow-go/refdata/catalog"
	"github.com/dream-until-dawn/futures-tickflow-go/refdata/evidence"
	"github.com/dream-until-dawn/futures-tickflow-go/source/pacing"
	"github.com/dream-until-dawn/futures-tickflow-go/store/segfile"
)

func document(raw, format string, day tf.TradingDay) Document {
	b := []byte(raw)
	sum := sha256.Sum256(b)
	return Document{Format: format, Day: day, Raw: b, Evidence: evidence.Ref{ID: fmt.Sprint(day), URL: "https://example.org/official", SHA256: hex.EncodeToString(sum[:]), RetrievedAt: time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)}}
}
func rawJSON(day tf.TradingDay) string {
	return fmt.Sprintf(`{"o_code":"0000","report_date":"%d","o_curinstrument":[{"PRODUCTID":"rb_f    ","DELIVERYMONTH":"2701","OPENPRICE":"3000.00","HIGHESTPRICE":"3010","LOWESTPRICE":"2990","CLOSEPRICE":"3001","SETTLEMENTPRICE":"3002.000","PRESETTLEMENTPRICE":"2999.000","VOLUME":12,"OPENINTEREST":"42","TURNOVER":"360000"}]}`, day)
}

const txt = "郑州商品交易所期货每日行情表(2026-11-03)\n品种月份|昨结算|今开盘|最高价|最低价|今收盘|今结算|成交量(手)|持仓量|成交额(万元)\nRM701|2,300.00|2310|2320|2290|2301|2302.000|100|200|230.00\n"

func calendar(t *testing.T) tf.Calendar {
	t.Helper()
	c, e := embedded.New([]tf.TradingDay{20261030, 20261102, 20261103, 20261104})
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func TestOfficialParseAndNegativeDocuments(t *testing.T) {
	d := document(rawJSON(20261103), "shfe-json", 20261103)
	r, e := Parse(d)
	if e != nil || len(r) != 1 || r[0].Fields["pre_settle"] != "2999.000" || r[0].TurnoverUnit != "unknown" {
		t.Fatalf("%+v %v", r, e)
	}
	for _, name := range []string{"challenge", "date", "status", "duplicate", "truncate", "tamper", "bad-number", "bad-month"} {
		t.Run(name, func(t *testing.T) {
			body := rawJSON(20261103)
			switch name {
			case "challenge":
				body = "<html>challenge</html>"
			case "date":
				body = strings.ReplaceAll(body, "20261103", "20261102")
			case "status":
				body = strings.ReplaceAll(body, "0000", "500")
			case "duplicate":
				row := body[strings.Index(body, "[{\"")+1 : len(body)-2]
				body = body[:len(body)-2] + "," + row + "]}"
			case "truncate":
				body = body[:len(body)-10]
			case "bad-number":
				body = strings.ReplaceAll(body, "3000.00", "NaN")
			case "bad-month":
				body = strings.ReplaceAll(body, "2701", "27XX")
			}
			bad := document(body, "shfe-json", 20261103)
			if name == "tamper" {
				bad.Raw = append(bad.Raw, 'x')
			}
			if _, e := Parse(bad); e == nil {
				t.Fatal("坏原件通过")
			}
		})
	}
	for _, raw := range []string{strings.Replace(txt, "品种月份", "合约代码", 1), txt} {
		d := document(raw, "czce-txt", 20261103)
		rr, e := Parse(d)
		if e != nil || rr[0].Fields["pre_settle"] != "2,300.00" {
			t.Fatalf("%+v %v", rr, e)
		}
	}
	for _, raw := range []string{strings.Replace(txt, "2026-11-03", "2026-11-04", 1), strings.Replace(txt, "|持仓量|", "|UNKNOWN|", 1), txt + "RM701|2300|2310|2320|2290|2301|2302|100|200|230\n", strings.Replace(txt, "RM701", "RM701C2300", 1), txt[:len(txt)-9]} {
		if _, e := Parse(document(raw, "czce-txt", 20261103)); e == nil {
			t.Fatal("坏TXT通过")
		}
	}
}
func TestOfficialSourceQuarantineMissingAndCatalog(t *testing.T) {
	cal := calendar(t)
	now := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC).UnixMilli()
	sym, _ := tf.ParseSymbol("SHFE.rb2701")
	req := tf.BarRequest{Symbol: sym, Period: tf.Daily, From: 20261103, To: 20261103}
	d := document(rawJSON(20261103), "shfe-json", 20261103)
	src, e := New(cal, []Document{d}, nil, now)
	if e != nil {
		t.Fatal(e)
	}
	bars, e := src.Bars(context.Background(), req)
	if e != nil || len(bars) != 1 || bars[0].Settle != 3002 || !math.IsNaN(bars[0].Turnover) {
		t.Fatalf("%+v %v", bars, e)
	}
	attachments := src.Records(sym)
	attachments[0].Fields["settle"] = "999"
	if src.Records(sym)[0].Fields["settle"] != "3002.000" {
		t.Fatal("原附件可被外部修改")
	}
	req.From = 20261102
	if b, e := src.Bars(context.Background(), req); e == nil || b != nil {
		t.Fatal("缺日返回部分成功")
	}
	req.From = 20261103
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = src.Bars(ctx, req); e == nil {
		t.Fatal("取消被忽略")
	}
	for _, replacement := range []string{"0", "3020", "--"} {
		bad := document(strings.ReplaceAll(rawJSON(20261103), "3000.00", replacement), "shfe-json", 20261103)
		s, e := New(cal, []Document{bad}, nil, now)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = s.Bars(context.Background(), req); e == nil {
			t.Fatal("异常OHLC进入Bar")
		}
	}
	dz := document(txt, "czce-txt", 20261103)
	if _, e = New(cal, []Document{dz}, nil, now); e == nil {
		t.Fatal("郑码无有据目录通过")
	}
	rm, _ := tf.ParseSymbol("CZCE.RM2701")
	cat, e := catalog.New([]catalog.Entry{{Symbol: rm, Observed: []catalog.Observation{{Day: 20261103, Evidence: dz.Evidence}}}})
	if e != nil {
		t.Fatal(e)
	}
	s, e := New(cal, []Document{dz}, cat, now)
	if e != nil {
		t.Fatal(e)
	}
	req.Symbol = rm
	b, e := s.Bars(context.Background(), req)
	if e != nil || b[0].Turnover != 2300000 || b[0].Settle != 2302 {
		t.Fatalf("%+v %v", b, e)
	}
}
func TestOfficialSyncStoreFeedResume(t *testing.T) {
	cal := calendar(t)
	sym, _ := tf.ParseSymbol("SHFE.rb2701")
	now := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC).UnixMilli()
	src, e := New(cal, []Document{document(rawJSON(20261102), "shfe-json", 20261102), document(rawJSON(20261103), "shfe-json", 20261103)}, nil, now)
	if e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	request := tf.SyncRequest{Symbol: sym, Period: tf.Daily, From: 20261102, To: 20261103}
	for round := 0; round < 2; round++ {
		store, tail, e := segfile.Open(dir, tf.Daily)
		if e != nil {
			t.Fatal(e)
		}
		if tail != 0 {
			store.Close()
			t.Fatal("新库截断")
		}
		syncer, e := tf.NewSyncer(tf.SyncerConfig{Calendar: cal, Store: store, NewSource: func(*http.Client) tf.Source { return src }, Pacer: pacing.NoPacing(), Timeout: time.Second})
		if e != nil {
			store.Close()
			t.Fatal(e)
		}
		report, e := syncer.Sync(context.Background(), request, now)
		if e != nil || !report.Complete() {
			store.Close()
			t.Fatalf("同步 %+v %v", report, e)
		}
		if (round == 0 && report.Bars != 2) || (round == 1 && report.Bars != 0) {
			store.Close()
			t.Fatalf("恢复条数%d", report.Bars)
		}
		feed, e := tf.NewFeed(store, tf.FeedConfig{Key: sym.ProductKey(), Calendar: cal, Base: tf.Daily, Rule: tf.AggTradingAxis, From: 20261102, To: 20261103})
		if e != nil {
			store.Close()
			t.Fatal(e)
		}
		count := 0
		for feed.Next() {
			count++
			if feed.View().Bar().Close != 3001 {
				t.Fatal("Feed字段变更")
			}
		}
		if e = feed.Err(); e != nil {
			t.Fatal(e)
		}
		feed.Close()
		if count != 2 {
			t.Fatal(count)
		}
		if e = store.Close(); e != nil {
			t.Fatal(e)
		}
	}
}
