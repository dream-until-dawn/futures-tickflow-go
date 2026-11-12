// Package proof 独立复核已审核的历史日历及本地Source/Store/Feed；不联网。
package proof

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	tf "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/verified"
	"github.com/dream-until-dawn/futures-tickflow-go/refdata/evidence"
	"github.com/dream-until-dawn/futures-tickflow-go/source/officialdaily"
	"github.com/dream-until-dawn/futures-tickflow-go/source/pacing"
	"github.com/dream-until-dawn/futures-tickflow-go/store/segfile"
)

var zone = time.FixedZone("CST", 8*3600)

type calendarFile struct {
	Schema   string            `json:"schema"`
	Series   []verified.Series `json:"series"`
	Year     int               `json:"year"`
	FactsSHA string            `json:"facts_sha256"`
}
type fileRef struct {
	File string       `json:"file"`
	Ref  evidence.Ref `json:"ref"`
}
type sourceLock struct {
	Sources map[string]struct {
		File  string       `json:"file"`
		Bytes int          `json:"bytes"`
		Ref   evidence.Ref `json:"ref"`
	} `json:"sources"`
}
type expected struct {
	from           tf.TradingDay
	days, nightEnd int
	dayOnly        map[tf.TradingDay]bool
}

var profiles = map[tf.ProductKey]expected{
	{Exchange: tf.INE, Product: "ec"}:  {20230818, 90, 0, nil},
	{Exchange: tf.SHFE, Product: "ao"}: {20230619, 132, 1500, map[tf.TradingDay]bool{20230619: true, 20230626: true, 20231009: true}},
	{Exchange: tf.SHFE, Product: "br"}: {20230728, 105, 1380, map[tf.TradingDay]bool{20230728: true, 20231009: true}},
}

func read(path string, into any) error {
	raw, e := os.ReadFile(path)
	if e != nil {
		return e
	}
	return json.Unmarshal(raw, into)
}
func ymd(t time.Time) tf.TradingDay {
	y, m, d := t.Date()
	return tf.TradingDay(y*10000 + int(m)*100 + d)
}
func instant(d tf.TradingDay, minute int) int64 {
	y, m, n := d.Split()
	return time.Date(y, time.Month(m), n, minute/60, minute%60, 0, 0, zone).UnixMilli()
}
func natural(d tf.TradingDay) time.Time {
	y, m, n := d.Split()
	return time.Date(y, time.Month(m), n, 0, 0, 0, 0, zone)
}
func holiday(d tf.TradingDay) bool {
	w := natural(d).Weekday()
	return w == time.Saturday || w == time.Sunday || (d >= 20230622 && d <= 20230624) || (d >= 20230929 && d <= 20231006)
}
func load(dir string, file fileRef) ([]byte, error) {
	if filepath.Base(file.File) != file.File {
		return nil, fmt.Errorf("unsafe_source_path")
	}
	b, e := os.ReadFile(filepath.Join(dir, file.File))
	if e != nil {
		return nil, e
	}
	if e = file.Ref.Verify(b); e != nil {
		return nil, e
	}
	return b, nil
}
func sourceAllowlist(facts sourceLock, dir string) (map[evidence.Ref]bool, error) {
	allowed := map[evidence.Ref]bool{}
	for name, s := range facts.Sources {
		b, e := load(dir, fileRef{s.File, s.Ref})
		if e != nil {
			return nil, fmt.Errorf("source_hash %s: %w", name, e)
		}
		if len(b) != s.Bytes {
			return nil, fmt.Errorf("source_size %s", name)
		}
		allowed[s.Ref] = true
	}
	return allowed, nil
}
func bind(input calendarFile, facts sourceLock, dir string) error {
	allowed, e := sourceAllowlist(facts, dir)
	if e != nil {
		return e
	}
	for _, series := range input.Series {
		for _, r := range series.Records {
			seen := map[evidence.Ref]bool{}
			for _, ref := range r.Evidence {
				if !allowed[ref] {
					return fmt.Errorf("evidence_binding: %s", r.Day)
				}
				seen[ref] = true
			}
			roles := []string{"shfe-year-2023", "shfe-trade-docx", "shfe-night-docx", "shfe-rules-202304", series.Product.Product + "-listing"}
			if series.Product.Exchange == tf.INE {
				rule := "ine-trade-202308-doc"
				if r.Day >= 20231121 {
					rule = "ine-trade-202311-docx"
				}
				roles = []string{"ine-year-2023-repub", "ec-listing-repub", "ine-rules-202308-repub", rule}
			}
			for _, role := range roles {
				if !seen[facts.Sources[role].Ref] {
					return fmt.Errorf("required_evidence: %s %s", r.Day, role)
				}
			}
		}
	}
	return nil
}
func profile(input calendarFile) (*verified.Calendar, error) {
	if input.Schema != "listed2023-calendar-v1" || len(input.Series) != 3 {
		return nil, fmt.Errorf("profile_identity")
	}
	cal, e := verified.New(input.Series)
	if e != nil {
		return nil, fmt.Errorf("calendar_structure: %w", e)
	}
	seen := map[tf.ProductKey]bool{}
	for _, series := range input.Series {
		p, ok := profiles[series.Product]
		if !ok || seen[series.Product] {
			return nil, fmt.Errorf("unknown_product")
		}
		seen[series.Product] = true
		naturalCount := int(natural(20231231).Sub(natural(p.from)).Hours()/24) + 1
		if len(series.Records) != naturalCount {
			return nil, fmt.Errorf("natural_coverage")
		}
		count := 0
		for i, r := range series.Records {
			if r.Day != ymd(natural(p.from).AddDate(0, 0, i)) {
				return nil, fmt.Errorf("natural_coverage")
			}
			from, to := tf.TradingDay(20230526), tf.TradingDay(20231231)
			if series.Product.Exchange == tf.INE {
				from = 20230818
				if r.Day >= 20231121 {
					from = 20231121
				} else {
					to = 20231120
				}
			}
			want := tf.SessionTemplate{From: from, To: to, Day: []tf.Session{{Start: 540 * 60000, End: 615 * 60000}, {Start: 630 * 60000, End: 690 * 60000}, {Start: 810 * 60000, End: 900 * 60000}}}
			if p.nightEnd != 0 {
				want.Night = []tf.Session{{Start: 1260 * 60000, End: int64(p.nightEnd) * 60000}}
			}
			actual := r.Template
			// JSON空数组与nil在语义上都表示无夜盘；其余相位和版本必须逐项一致。
			if len(actual.Night) == 0 {
				actual.Night = nil
			}
			if !reflect.DeepEqual(actual, want) {
				return nil, fmt.Errorf("template_version_phase: %s %s", series.Product, r.Day)
			}
			d, err := cal.DayOf(series.Product, r.Day)
			if holiday(r.Day) {
				if !errors.Is(err, tf.ErrNotTradingDay) {
					return nil, fmt.Errorf("holiday_open: %s", r.Day)
				}
				continue
			}
			if err != nil {
				return nil, err
			}
			count++
			var sessions []tf.Session
			if p.nightEnd != 0 && !p.dayOnly[r.Day] {
				previous := natural(r.Day).AddDate(0, 0, -1)
				for holiday(ymd(previous)) {
					previous = previous.AddDate(0, 0, -1)
				}
				sessions = append(sessions, tf.Session{Start: instant(ymd(previous), 1260), End: instant(ymd(previous), p.nightEnd)})
			}
			for _, m := range [][2]int{{540, 615}, {630, 690}, {810, 900}} {
				sessions = append(sessions, tf.Session{Start: instant(r.Day, m[0]), End: instant(r.Day, m[1])})
			}
			if !reflect.DeepEqual(sessions, d.Sessions) {
				return nil, fmt.Errorf("absolute_session: %s %s", series.Product, r.Day)
			}
		}
		if count != p.days {
			return nil, fmt.Errorf("open_count: %s", series.Product)
		}
		before := ymd(natural(p.from).AddDate(0, 0, -1))
		for _, d := range []tf.TradingDay{before, 20240101} {
			if _, e := cal.DayOf(series.Product, d); !errors.Is(e, tf.ErrUncovered) {
				return nil, fmt.Errorf("scope_extension")
			}
		}
	}
	for _, tc := range []struct {
		product string
		date    tf.TradingDay
		minute  int
		want    tf.TradingDay
	}{
		{"ao", 20230630, 1260, 20230703}, {"ao", 20230701, 30, 20230703}, {"br", 20230728, 1260, 20230731},
		{"ao", 20231009, 540, 20231009}, {"ec", 20230818, 540, 20230818},
	} {
		exchange := tf.SHFE
		if tc.product == "ec" {
			exchange = tf.INE
		}
		d, e := cal.DayAt(tf.ProductKey{Exchange: exchange, Product: tc.product}, instant(tc.date, tc.minute))
		if e != nil || d.Num != tc.want {
			return nil, fmt.Errorf("day_at_mapping")
		}
	}
	for _, tc := range []struct {
		key    tf.ProductKey
		date   tf.TradingDay
		minute int
	}{
		{tf.ProductKey{Exchange: tf.SHFE, Product: "ao"}, 20230701, 60},
		{tf.ProductKey{Exchange: tf.SHFE, Product: "ao"}, 20230928, 1260},
		{tf.ProductKey{Exchange: tf.SHFE, Product: "br"}, 20230928, 1260},
		{tf.ProductKey{Exchange: tf.INE, Product: "ec"}, 20230818, 1260},
		{tf.ProductKey{Exchange: tf.INE, Product: "ec"}, 20230818, 615},
	} {
		if _, e := cal.DayAt(tc.key, instant(tc.date, tc.minute)); !errors.Is(e, tf.ErrClosed) {
			return nil, fmt.Errorf("closed_instant")
		}
	}
	return cal, nil
}
func ordinal(dir string, f fileRef) (tf.TradingDay, int, error) {
	b, e := load(dir, f)
	if e != nil {
		return 0, 0, e
	}
	var v struct {
		Date    string          `json:"report_date"`
		Ordinal json.RawMessage `json:"o_year_num"`
		Code    json.RawMessage `json:"o_code"`
	}
	if e = json.Unmarshal(b, &v); e != nil {
		return 0, 0, e
	}
	d, e := strconv.Atoi(v.Date)
	if e != nil {
		return 0, 0, e
	}
	n, e := strconv.Atoi(strings.Trim(string(v.Ordinal), "\""))
	if e != nil || strings.Trim(string(v.Code), "\"") != "0" {
		return 0, 0, fmt.Errorf("ordinal_status")
	}
	return tf.TradingDay(d), n, nil
}
func ordinals(cal *verified.Calendar, dir string) (int, error) {
	var inputs []struct {
		Profile string    `json:"profile"`
		Base    fileRef   `json:"base"`
		Ends    []fileRef `json:"ends"`
	}
	if e := read(filepath.Join(dir, "ordinal-input.json"), &inputs); e != nil {
		return 0, e
	}
	if len(inputs) != 3 {
		return 0, fmt.Errorf("ordinal_profiles")
	}
	seen := map[tf.ProductKey]bool{}
	total := 0
	for _, v := range inputs {
		parts := strings.Split(v.Profile, ".")
		if len(parts) != 2 {
			return 0, fmt.Errorf("ordinal_key")
		}
		key := tf.ProductKey{Exchange: parts[0], Product: parts[1]}
		p, ok := profiles[key]
		if !ok || seen[key] {
			return 0, fmt.Errorf("ordinal_key")
		}
		seen[key] = true
		base, n, e := ordinal(dir, v.Base)
		if e != nil {
			return 0, e
		}
		before := natural(p.from).AddDate(0, 0, -1)
		for holiday(ymd(before)) {
			before = before.AddDate(0, 0, -1)
		}
		if base != ymd(before) {
			return 0, fmt.Errorf("ordinal_base")
		}
		_, startMonth, _ := p.from.Split()
		if len(v.Ends) != 13-startMonth {
			return 0, fmt.Errorf("ordinal_months")
		}
		for i, f := range v.Ends {
			end, m, e := ordinal(dir, f)
			if e != nil {
				return 0, e
			}
			_, month, _ := end.Split()
			if month != startMonth+i || end < p.from || end > 20231231 {
				return 0, fmt.Errorf("ordinal_month")
			}
			count := 0
			e = cal.Walk(key, p.from, end, func(tf.Day) bool { count++; return true })
			if e != nil || count != m-n {
				return 0, fmt.Errorf("ordinal_mismatch: %s %s", key, end)
			}
			total++
		}
	}
	return total, nil
}

type marketInput struct {
	Documents []struct {
		File     string        `json:"file"`
		Format   string        `json:"format"`
		Day      tf.TradingDay `json:"day"`
		Ref      evidence.Ref  `json:"ref"`
		Original *fileRef      `json:"original,omitempty"`
	} `json:"documents"`
	Samples []struct {
		Symbol        string        `json:"symbol"`
		Day           tf.TradingDay `json:"day"`
		StartDay      tf.TradingDay `json:"start_day"`
		StartMinute   int           `json:"start_minute"`
		Close, Settle float64
	} `json:"samples"`
}

func market(cal *verified.Calendar, dir, out string) (int, error) {
	var input marketInput
	if e := read(filepath.Join(dir, "market-input.json"), &input); e != nil {
		return 0, e
	}
	now := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC).UnixMilli()
	docs := []officialdaily.Document{}
	for _, v := range input.Documents {
		raw, e := load(dir, fileRef{v.File, v.Ref})
		if e != nil {
			return 0, e
		}
		d := officialdaily.Document{Format: v.Format, Day: v.Day, Evidence: v.Ref, Raw: raw}
		if v.Original != nil {
			d.Original, e = load(dir, *v.Original)
			if e != nil {
				return 0, e
			}
			d.Origin = v.Original.Ref
		}
		docs = append(docs, d)
	}
	src, e := officialdaily.New(cal, docs, nil, now)
	if e != nil {
		return 0, e
	}
	for i, s := range input.Samples {
		sym, e := tf.ParseSymbol(s.Symbol)
		if e != nil {
			return 0, e
		}
		for round := 0; round < 2; round++ {
			store, tail, e := segfile.Open(filepath.Join(out, fmt.Sprintf("sample-%d", i)), tf.Daily)
			if e != nil {
				return 0, e
			}
			if tail != 0 {
				store.Close()
				return 0, fmt.Errorf("store_truncated")
			}
			sy, e := tf.NewSyncer(tf.SyncerConfig{Calendar: cal, Store: store, NewSource: func(*http.Client) tf.Source { return src }, Pacer: pacing.NoPacing(), Timeout: time.Second})
			if e != nil {
				store.Close()
				return 0, e
			}
			r, e := sy.Sync(context.Background(), tf.SyncRequest{Symbol: sym, Period: tf.Daily, From: s.Day, To: s.Day}, now)
			wantBars := 1 - round
			if e != nil || !r.Complete() || r.Bars != wantBars {
				store.Close()
				return 0, fmt.Errorf("sync_report: %s %s %+v %v", s.Symbol, s.Day, r, e)
			}
			feed, e := tf.NewFeed(store, tf.FeedConfig{Key: sym.ProductKey(), Calendar: cal, Base: tf.Daily, Rule: tf.AggTradingAxis, From: s.Day, To: s.Day})
			if e != nil {
				store.Close()
				return 0, e
			}
			count := 0
			for feed.Next() {
				count++
				b := feed.View().Bar()
				if b.Close != s.Close || b.Settle != s.Settle || b.Ts != instant(s.StartDay, s.StartMinute) || b.TsEnd != instant(s.Day, 900) {
					feed.Close()
					store.Close()
					return 0, fmt.Errorf("feed_fields: %s %s", s.Symbol, s.Day)
				}
			}
			e = feed.Err()
			feed.Close()
			closeErr := store.Close()
			if e != nil {
				return 0, e
			}
			if closeErr != nil {
				return 0, closeErr
			}
			if count != 1 {
				return 0, fmt.Errorf("feed_count")
			}
		}
	}
	if len(input.Samples) != 8 {
		return 0, fmt.Errorf("market_samples")
	}
	return len(input.Samples), nil
}
func run(calendar, facts, dir, out string, withMarket bool) error {
	if e := os.Mkdir(out, 0700); e != nil {
		return fmt.Errorf("新输出目录: %w", e)
	}
	var input calendarFile
	var lock sourceLock
	if e := read(calendar, &input); e != nil {
		return e
	}
	if e := read(facts, &lock); e != nil {
		return e
	}
	var cal *verified.Calendar
	var n int
	var e error
	if input.Schema == "annual-calendar-v1" {
		if e = bind2024(input, lock, facts, dir); e != nil {
			return e
		}
		cal, e = profile2024(input)
		if e == nil {
			n, e = observations2024(cal, dir)
		}
	} else {
		if e = bind(input, lock, dir); e != nil {
			return e
		}
		cal, e = profile(input)
		if e == nil {
			n, e = ordinals(cal, dir)
		}
	}
	if e != nil {
		return e
	}
	samples := 0
	if withMarket {
		samples, e = market(cal, dir, out)
		if e != nil {
			return e
		}
	}
	report := map[string]any{"status": "passed", "products": len(input.Series), "market_contract_days": samples, "reopen_added_bars": 0, "scope": "published continuous schedules; no point-in-time availability or price certification"}
	if input.Schema == "annual-calendar-v1" {
		report["monthly_observation_checks"] = n
	} else {
		report["ordinal_checks"] = n
	}
	raw, e := json.MarshalIndent(report, "", "  ")
	if e != nil {
		return e
	}
	return os.WriteFile(filepath.Join(out, "report.json"), raw, 0600)
}

// Main 提供历史日历核查命令的共享入口；兼容旧命令路径。
func Main() {
	calendar := flag.String("calendar", "", "日历JSON")
	facts := flag.String("facts", "", "固定源锁")
	dir := flag.String("evidence-dir", "", "原件目录")
	out := flag.String("out", "", "新输出目录")
	withMarket := flag.Bool("market", false, "真实Source/Store/Feed")
	flag.Parse()
	if e := run(*calendar, *facts, *dir, *out, *withMarket); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
