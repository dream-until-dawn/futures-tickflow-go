// historical 执行既有官方原件的离线Source/Syncer/segfile/Feed探针；绝不联网。
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	tf "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/archive/batch"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/embedded"
	"github.com/dream-until-dawn/futures-tickflow-go/refdata/catalog"
	"github.com/dream-until-dawn/futures-tickflow-go/refdata/evidence"
	"github.com/dream-until-dawn/futures-tickflow-go/source/officialdaily"
	"github.com/dream-until-dawn/futures-tickflow-go/source/pacing"
	"github.com/dream-until-dawn/futures-tickflow-go/store/segfile"
)

type inputFile struct {
	Path         string
	Format       string
	Day          tf.TradingDay
	Evidence     evidence.Ref
	Origin       evidence.Ref
	OriginalPath string `json:"original_path"`
}
type input struct {
	Documents, References []inputFile
	CalendarPath          string `json:"calendar_path"`
	CalendarSHA           string `json:"calendar_sha256"`
	ExtensionPath         string `json:"extension_path"`
	ExtensionSHA          string `json:"extension_sha256"`
	Now                   time.Time
	From                  tf.TradingDay `json:"from_day"`
	To                    tf.TradingDay `json:"to_day"`
	Symbols               []string
	Products              []tf.ProductKey
}
type sample struct {
	Symbol  string                 `json:"symbol"`
	Records []officialdaily.Record `json:"records"`
	Bars    []map[string]any       `json:"bars"`
	Rounds  []tf.SyncReport        `json:"rounds"`
}

func read(path string, v any) error {
	raw, e := os.ReadFile(path)
	if e != nil {
		return e
	}
	return json.Unmarshal(raw, v)
}
func hash(raw []byte) string { h := sha256.Sum256(raw); return hex.EncodeToString(h[:]) }
func clean(b tf.Bar) map[string]any {
	m := map[string]any{"day": b.TradingDay, "ts": b.Ts, "ts_end": b.TsEnd, "open": b.Open, "high": b.High, "low": b.Low, "close": b.Close, "settle": b.Settle, "volume": b.Volume, "open_interest": b.OpenInterest, "turnover": b.Turnover, "flags": b.Flags}
	for k, v := range m {
		if f, ok := v.(float64); ok && math.IsNaN(f) {
			m[k] = nil
		}
	}
	return m
}
func run(inPath, out string) error {
	var in input
	if e := read(inPath, &in); e != nil {
		return e
	}
	// 探针整轮使用全新目录；内部恢复通过明确的第二次Run/Sync验证。
	// 失败目录保留诊断，不自动把未知旧运行当作可重启任务。
	if e := os.Mkdir(out, 0700); e != nil {
		return fmt.Errorf("探针输出须为全新目录: %w", e)
	}
	raw, e := os.ReadFile(in.CalendarPath)
	if e != nil {
		return e
	}
	if hash(raw) != in.CalendarSHA {
		return fmt.Errorf("calendar SHA mismatch")
	}
	var days struct {
		Days    []tf.TradingDay `json:"trading_days"`
		NoNight []tf.TradingDay `json:"no_night_dates"`
	}
	if e = json.Unmarshal(raw, &days); e != nil {
		return e
	}
	extRaw, e := os.ReadFile(in.ExtensionPath)
	if e != nil {
		return e
	}
	if hash(extRaw) != in.ExtensionSHA {
		return fmt.Errorf("extension SHA mismatch")
	}
	var extension struct {
		Calendar struct {
			Days    []string          `json:"days"`
			Sources map[string]string `json:"source_files"`
		} `json:"calendar"`
	}
	if e = json.Unmarshal(extRaw, &extension); e != nil {
		return e
	}
	bound := false
	for _, s := range extension.Calendar.Sources {
		bound = bound || s == in.CalendarSHA
	}
	if !bound {
		return fmt.Errorf("extension did not bind base")
	}
	last := days.Days[len(days.Days)-1]
	for _, text := range extension.Calendar.Days {
		n, err := strconv.Atoi(text)
		if err != nil {
			return err
		}
		d := tf.TradingDay(n)
		if d > last {
			days.Days = append(days.Days, d)
		}
	}
	cal, e := embedded.New(days.Days, embedded.NoNightAfter(days.NoNight...))
	if e != nil {
		return e
	}
	groups := [][]catalog.Entry{}
	for _, r := range in.References {
		raw, e := os.ReadFile(r.Path)
		if e != nil {
			return e
		}
		var entries []catalog.Entry
		if r.Format == "shfe-base" {
			entries, e = catalog.ParseSHFEBase(raw, r.Evidence, r.Day)
		} else {
			entries, e = catalog.ParseCZCEReference(raw, r.Evidence, r.Day)
		}
		if e != nil {
			return e
		}
		groups = append(groups, entries)
	}
	entries, e := catalog.Merge(groups...)
	if e != nil {
		return e
	}
	cat, e := catalog.New(entries)
	if e != nil {
		return e
	}
	var jobs []batch.Job
	origins := map[string]inputFile{}
	for _, f := range in.Documents {
		origins[f.Evidence.ID] = f
		jobs = append(jobs, batch.Job{ID: f.Evidence.ID, URL: f.Evidence.URL, Format: f.Format, Day: int32(f.Day), Local: f.Path, ExpectedSHA: f.Evidence.SHA256, RetrievedAt: f.Evidence.RetrievedAt, Version: f.Evidence.Version})
	}
	validator := func(j batch.Job, raw []byte, r evidence.Ref) error {
		doc := officialdaily.Document{Format: j.Format, Day: tf.TradingDay(j.Day), Evidence: r, Raw: raw}
		if f := origins[j.ID]; f.OriginalPath != "" {
			doc.Origin = f.Origin
			original, e := os.ReadFile(f.OriginalPath)
			if e != nil {
				return e
			}
			doc.Original = original
		}
		_, e := officialdaily.Parse(doc)
		return e
	}
	receipts, e := batch.Run(context.Background(), filepath.Join(out, "archive"), jobs, nil, 16<<20, validator)
	if e != nil {
		return e
	}
	documents := make([]officialdaily.Document, 0, len(jobs))
	for i, r := range receipts {
		if r.Receipt.State != "received" {
			return fmt.Errorf("archive state %s", r.Receipt.State)
		}
		raw, e := os.ReadFile(filepath.Join(out, "archive", r.ID+".body"))
		if e != nil {
			return e
		}
		doc := officialdaily.Document{Format: jobs[i].Format, Day: tf.TradingDay(jobs[i].Day), Evidence: r.Receipt.Evidence, Raw: raw}
		if f := origins[r.ID]; f.OriginalPath != "" {
			doc.Origin = f.Origin
			original, e := os.ReadFile(f.OriginalPath)
			if e != nil {
				return e
			}
			doc.Original = original
		}
		documents = append(documents, doc)
	}
	src, e := officialdaily.New(cal, documents, cat, in.Now.UnixMilli())
	if e != nil {
		return e
	}
	var all []sample
	for _, text := range in.Symbols {
		sym, e := tf.ParseSymbol(text)
		if e != nil {
			return e
		}
		result := sample{Symbol: text, Records: src.Records(sym)}
		dir := filepath.Join(out, "stores", fmt.Sprintf("%s-%s%04d", sym.Exchange, sym.Product, sym.YearMon))
		for round := 0; round < 2; round++ {
			store, tail, e := segfile.Open(dir, tf.Daily)
			if e != nil {
				return e
			}
			if tail != 0 {
				store.Close()
				return fmt.Errorf("unexpected tail")
			}
			proof, e := batch.SyncFrozen(context.Background(), tf.SyncerConfig{Calendar: cal, Store: store, NewSource: func(*http.Client) tf.Source { return src }, Pacer: pacing.NoPacing(), Timeout: time.Second}, tf.SyncRequest{Symbol: sym, Period: tf.Daily, From: in.From, To: in.To}, in.Now.UnixMilli())
			report := proof.Report
			if e != nil || !report.Complete() {
				store.Close()
				return fmt.Errorf("sync incomplete %s: %v %+v", sym, e, report)
			}
			if round == 1 && report.Bars != 0 {
				store.Close()
				return fmt.Errorf("resume wrote new bars")
			}
			result.Rounds = append(result.Rounds, report)
			feed, e := tf.NewFeed(store, tf.FeedConfig{Key: sym.ProductKey(), Calendar: cal, Base: tf.Daily, Rule: tf.AggTradingAxis, From: in.From, To: in.To})
			if e != nil {
				store.Close()
				return e
			}
			count := 0
			for feed.Next() {
				count++
				if round == 0 {
					result.Bars = append(result.Bars, clean(feed.View().Bar()))
				}
			}
			e = feed.Err()
			feed.Close()
			closeErr := store.Close()
			if e != nil {
				return e
			}
			if closeErr != nil {
				return closeErr
			}
			want := 0
			err := cal.Walk(sym.ProductKey(), in.From, in.To, func(tf.Day) bool { want++; return true })
			if err != nil {
				return err
			}
			if count != want {
				return fmt.Errorf("unexpected feed count %d", count)
			}
		}
		all = append(all, result)
	}
	resumed, e := batch.Run(context.Background(), filepath.Join(out, "archive"), jobs, nil, 16<<20, validator)
	if e != nil {
		return e
	}
	for _, r := range resumed {
		if !r.Resumed {
			return fmt.Errorf("archive resume was not reused")
		}
	}
	var productStatus []map[string]any
	for _, k := range in.Products {
		from, to, ok := cal.Covers(k)
		_, err := cal.Template(k, in.To)
		productStatus = append(productStatus, map[string]any{"product": k.String(), "embedded_latest_covered": ok && err == nil, "from": from, "to": to, "historical_sessions_certified": false})
	}
	report := map[string]any{"status": "offline_real_samples_passed", "scope": "bounded native contract-days; not full history or historical session certification", "network_requests": 0, "input_sha256": func() string { b, _ := os.ReadFile(inPath); return hash(b) }(), "calendar_sha256": in.CalendarSHA, "extension_sha256": in.ExtensionSHA, "product_statuses": productStatus, "catalog_entries": len(entries), "catalog": entries, "archive_receipts": resumed, "samples": all}
	body, e := json.MarshalIndent(report, "", "  ")
	if e != nil {
		return e
	}
	return os.WriteFile(filepath.Join(out, "report.json"), append(body, '\n'), 0600)
}
func main() {
	in := flag.String("input", "", "冻结探针输入JSON")
	out := flag.String("out", "", "独立输出目录")
	flag.Parse()
	if *in == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "必须给-input和-out")
		os.Exit(2)
	}
	if e := run(*in, *out); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
	fmt.Println("离线真实原件链路通过；报告:", filepath.Join(*out, "report.json"))
}
