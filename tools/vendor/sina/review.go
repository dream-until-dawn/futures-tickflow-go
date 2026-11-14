package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tf "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/embedded"
	"github.com/dream-until-dawn/futures-tickflow-go/refdata/evidence"
	"github.com/dream-until-dawn/futures-tickflow-go/source/pacing"
	"github.com/dream-until-dawn/futures-tickflow-go/source/sinavendor"
	"github.com/dream-until-dawn/futures-tickflow-go/store/segfile"
)

type sampleReceipt struct {
	Name, URL, SHA256 string
	Symbol            string    `json:"canonical_symbol,omitempty"`
	At                time.Time `json:"received_at"`
	Bytes             int       `json:"bytes"`
}
type sampleReview struct {
	Symbol             string       `json:"symbol"`
	Evidence           evidence.Ref `json:"evidence"`
	RawRows            int          `json:"raw_rows"`
	TargetRows         int          `json:"target_rows"`
	First, Last        tf.TradingDay
	MechanicalComplete bool   `json:"mechanical_complete"`
	FeedRows           int    `json:"feed_rows"`
	ResumeAdded        int    `json:"resume_added"`
	Failure            string `json:"failure,omitempty"`
	CalendarCertified  bool   `json:"official_calendar_certified"`
	RosterComplete     bool   `json:"historical_roster_complete"`
	FinancialEligible  bool   `json:"financial_eligible"`
}

func writeJSON(p string, v any) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	return os.WriteFile(p, append(b, '\n'), 0600)
}

// reviewSamples 不联网，结果目录必须全新；报价日期并集只作合成日历，绝不冒充独立证据。
func reviewSamples(input, root string) error {
	b, e := os.ReadFile(input)
	if e != nil {
		return e
	}
	var rr []sampleReceipt
	if e = json.Unmarshal(b, &rr); e != nil {
		return e
	}
	if len(rr) == 0 {
		return fmt.Errorf("零样本")
	}
	var docs []sinavendor.Document
	var parsed [][]sinavendor.Record
	union := map[tf.TradingDay]bool{}
	for _, r := range rr {
		exch := tf.DCE
		var declared tf.Symbol
		if r.Symbol != "" {
			declared, e = tf.ParseSymbol(r.Symbol)
			if e != nil {
				return e
			}
			if r.Name != fmt.Sprintf("%s%04d", strings.ToUpper(declared.Product), declared.YearMon) {
				return fmt.Errorf("声明代码不符")
			}
			exch = declared.Exchange
		} else {
			switch r.Name {
			case "C2105", "M2105":
			case "SI2308":
				exch = tf.GFEX
			case "RB2410":
				exch = tf.SHFE
			default:
				return fmt.Errorf("离线回读仅四份已审阅原样本")
			}
		}
		sym, e := tf.ParseSymbol(exch + "." + strings.ToLower(r.Name))
		if e != nil {
			return e
		}
		raw, e := os.ReadFile(filepath.Join(filepath.Dir(input), r.Name+".jsonp"))
		if e != nil {
			return e
		}
		if len(raw) != r.Bytes {
			return fmt.Errorf("样本长度不符")
		}
		d := sinavendor.Document{Symbol: sym, Raw: raw, Evidence: evidence.Ref{ID: r.Name, URL: r.URL, SHA256: r.SHA256, RetrievedAt: r.At, Version: "sina-frozen-v1"}}
		rows, e := sinavendor.Parse(d)
		if e != nil {
			return e
		}
		docs = append(docs, d)
		parsed = append(parsed, rows)
		if exch == tf.DCE {
			for _, row := range rows {
				if row.Day >= 20210101 {
					union[row.Day] = true
				}
			}
		}
	}
	if e = os.Mkdir(root, 0700); e != nil {
		return fmt.Errorf("结果必须使用全新私有目录: %w", e)
	}
	var results []sampleReview
	for i, d := range docs {
		rows := parsed[i]
		r := sampleReview{Symbol: d.Symbol.String(), Evidence: d.Evidence, RawRows: len(rows)}
		set := map[tf.TradingDay]bool{}
		for _, row := range rows {
			if row.Day < 20210101 {
				continue
			}
			r.TargetRows++
			if r.First == 0 {
				r.First = row.Day
			}
			r.Last = row.Day
			set[row.Day] = true
		}
		if d.Symbol.Exchange == tf.DCE {
			for day := range union {
				if day >= r.First && day <= r.Last {
					set[day] = true
				}
			}
		}
		var days []tf.TradingDay
		for day := range set {
			days = append(days, day)
		}
		sort.Slice(days, func(i, j int) bool { return days[i] < days[j] })
		cal, e := embedded.New(days)
		if e != nil {
			return e
		}
		now := time.Now().UnixMilli()
		src, e := sinavendor.New(cal, []sinavendor.Document{d}, now)
		if e != nil {
			return e
		}
		if e = writeJSON(filepath.Join(root, d.Evidence.ID+"-records.json"), src.Records(d.Symbol)); e != nil {
			return e
		}
		req := tf.BarRequest{Symbol: d.Symbol, Period: tf.Daily, From: r.First, To: r.Last}
		want, e := src.Bars(context.Background(), req)
		if e != nil {
			r.Failure = e.Error()
			results = append(results, r)
			continue
		}
		for round := 0; round < 2; round++ {
			store, tail, e := segfile.Open(filepath.Join(root, d.Evidence.ID+"-store"), tf.Daily)
			if e != nil {
				return e
			}
			if tail != 0 {
				store.Close()
				return fmt.Errorf("存储发生截断")
			}
			syncer, e := tf.NewSyncer(tf.SyncerConfig{Calendar: cal, Store: store, NewSource: func(*http.Client) tf.Source { return src }, Pacer: pacing.NoPacing(), Timeout: 15 * time.Second})
			if e != nil {
				store.Close()
				return e
			}
			report, e := syncer.Sync(context.Background(), tf.SyncRequest{Symbol: d.Symbol, Period: tf.Daily, From: r.First, To: r.Last}, now)
			if e != nil || !report.Complete() {
				store.Close()
				return fmt.Errorf("机械同步失败: %+v %v", report, e)
			}
			if (round == 0 && report.Bars != len(want)) || (round == 1 && report.Bars != 0) {
				store.Close()
				return fmt.Errorf("新增/恢复行数不符")
			}
			if round == 1 {
				r.ResumeAdded = report.Bars
			}
			feed, e := tf.NewFeed(store, tf.FeedConfig{Key: d.Symbol.ProductKey(), Calendar: cal, Base: tf.Daily, Rule: tf.AggTradingAxis, From: r.First, To: r.Last})
			if e != nil {
				store.Close()
				return e
			}
			count := 0
			for feed.Next() {
				if count >= len(want) || !sameBar(feed.View().Bar(), want[count]) {
					feed.Close()
					store.Close()
					return fmt.Errorf("Feed逐字段位值不符")
				}
				count++
			}
			err := feed.Err()
			feed.Close()
			store.Close()
			if err != nil || count != len(want) {
				return fmt.Errorf("Feed条数/退出异常: %d %v", count, err)
			}
			r.FeedRows = count
		}
		r.MechanicalComplete = true
		results = append(results, r)
	}
	// 即使机械链路成功，缺独立历史日历/挂牌全集，所有金融与官方覆盖资格仍为false。
	return writeJSON(filepath.Join(root, "coverage.json"), map[string]any{"class": "vendor/sina/request-bound", "calendar_basis": "synthetic: quotation-date union; NOT independent historical schedule", "new_market_requests": 0, "published": false, "samples": results})
}

func sameBar(a, b tf.Bar) bool {
	if a.Ts != b.Ts || a.TsEnd != b.TsEnd || a.TradingDay != b.TradingDay || a.Flags != b.Flags {
		return false
	}
	x := []float64{a.Open, a.High, a.Low, a.Close, a.Volume, a.OpenInterest, a.Settle, a.Turnover}
	y := []float64{b.Open, b.High, b.Low, b.Close, b.Volume, b.OpenInterest, b.Settle, b.Turnover}
	for i := range x {
		if math.Float64bits(x[i]) != math.Float64bits(y[i]) {
			return false
		}
	}
	return true
}
