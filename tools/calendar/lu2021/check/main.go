// check 独立核对LU 2021日历、月末官方交易日序号及可选真实行情链路；不联网。
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
var key = tf.ProductKey{Exchange: tf.INE, Product: "lu"}

type envelope struct {
	Schema string            `json:"schema"`
	Series []verified.Series `json:"series"`
}

func read(path string, into any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, into)
}
func day(t time.Time) tf.TradingDay {
	y, m, d := t.Date()
	return tf.TradingDay(y*10000 + int(m)*100 + d)
}
func at(d tf.TradingDay, minute int) int64 {
	y, m, n := d.Split()
	return time.Date(y, time.Month(m), n, minute/60, minute%60, 0, 0, zone).UnixMilli()
}
func closed(d tf.TradingDay) bool {
	y, m, n := d.Split()
	w := time.Date(y, time.Month(m), n, 0, 0, 0, 0, zone).Weekday()
	if w == time.Saturday || w == time.Sunday {
		return true
	}
	// 独立按官方年度公告复核的日期区间，不读取生成器的holiday配置。
	for _, r := range [][2]tf.TradingDay{{20210101, 20210103}, {20210211, 20210217}, {20210403, 20210405}, {20210501, 20210505}, {20210612, 20210614}, {20210919, 20210921}, {20211001, 20211007}} {
		if d >= r[0] && d <= r[1] {
			return true
		}
	}
	return false
}
func profile(in envelope) (*verified.Calendar, int, error) {
	if in.Schema != "lu2021-calendar-v1" || len(in.Series) != 1 || in.Series[0].Product != key || len(in.Series[0].Records) != 365 {
		return nil, 0, fmt.Errorf("profile_identity")
	}
	cal, err := verified.New(in.Series)
	if err != nil {
		return nil, 0, fmt.Errorf("calendar_structure: %w", err)
	}
	dayOnly := map[tf.TradingDay]bool{20210104: true, 20210218: true, 20210406: true, 20210506: true, 20210615: true, 20210922: true, 20211008: true}
	count := 0
	for i, r := range in.Series[0].Records {
		wantDay := day(time.Date(2021, 1, 1+i, 0, 0, 0, 0, zone))
		if r.Day != wantDay {
			return nil, 0, fmt.Errorf("natural_day_coverage")
		}
		actual, e := cal.DayOf(key, r.Day)
		if closed(r.Day) {
			if !errors.Is(e, tf.ErrNotTradingDay) {
				return nil, 0, fmt.Errorf("closed_day_opened: %s", r.Day)
			}
			continue
		}
		if e != nil {
			return nil, 0, e
		}
		count++
		var want []tf.Session
		if !dayOnly[r.Day] {
			previous := time.Date(2021, 1, 1+i, 0, 0, 0, 0, zone).AddDate(0, 0, -1)
			for closed(day(previous)) {
				previous = previous.AddDate(0, 0, -1)
			}
			want = append(want, tf.Session{Start: at(day(previous), 1260), End: at(day(previous), 1380)})
		}
		for _, m := range [][2]int{{540, 615}, {630, 690}, {810, 900}} {
			want = append(want, tf.Session{Start: at(r.Day, m[0]), End: at(r.Day, m[1])})
		}
		if len(actual.Sessions) != len(want) {
			return nil, 0, fmt.Errorf("night_presence: %s", r.Day)
		}
		for j, s := range want {
			if actual.Sessions[j] != s {
				return nil, 0, fmt.Errorf("absolute_session: %s", r.Day)
			}
		}
		t, e := cal.Template(key, r.Day)
		if e != nil || !reflect.DeepEqual(t.Day, []tf.Session{{Start: 540 * 60000, End: 615 * 60000}, {Start: 630 * 60000, End: 690 * 60000}, {Start: 810 * 60000, End: 900 * 60000}}) || !reflect.DeepEqual(t.Night, []tf.Session{{Start: 1260 * 60000, End: 1380 * 60000}}) {
			return nil, 0, fmt.Errorf("nominal_template")
		}
		from, to := tf.TradingDay(20210101), tf.TradingDay(20210610)
		if r.Day >= 20210611 {
			from, to = 20210611, 20211125
		}
		if r.Day >= 20211126 {
			from, to = 20211126, 20211231
		}
		if t.From != from || t.To != to {
			return nil, 0, fmt.Errorf("version_partition")
		}
	}
	if count != 243 {
		return nil, 0, fmt.Errorf("trading_day_count")
	}
	for _, p := range []struct {
		date   tf.TradingDay
		minute int
		want   tf.TradingDay
	}{{20210108, 1260, 20210111}, {20210728, 540, 20210728}, {20210728, 630, 20210728}, {20211008, 1260, 20211011}} {
		d, e := cal.DayAt(key, at(p.date, p.minute))
		if e != nil || d.Num != p.want {
			return nil, 0, fmt.Errorf("night_day_mapping")
		}
	}
	for _, p := range [][2]int{{20210402, 1260}, {20210611, 1260}, {20210917, 1260}, {20210930, 1260}, {20210728, 615}, {20210728, 690}, {20210728, 900}, {20210728, 1380}} {
		if _, e := cal.DayAt(key, at(tf.TradingDay(p[0]), p[1])); !errors.Is(e, tf.ErrClosed) {
			return nil, 0, fmt.Errorf("closed_instant")
		}
	}
	for _, d := range []tf.TradingDay{20201231, 20220101} {
		if _, e := cal.DayOf(key, d); !errors.Is(e, tf.ErrUncovered) {
			return nil, 0, fmt.Errorf("scope_extension")
		}
	}
	return cal, count, nil
}

func sourceHashes(facts, dir string, calendar envelope) error {
	var input struct {
		Sources map[string]struct {
			File  string       `json:"file"`
			Bytes int          `json:"bytes"`
			Ref   evidence.Ref `json:"ref"`
		} `json:"sources"`
	}
	if err := read(facts, &input); err != nil {
		return err
	}
	allowed := make(map[evidence.Ref]bool)
	for name, s := range input.Sources {
		if filepath.Base(s.File) != s.File {
			return fmt.Errorf("unsafe_source_path")
		}
		b, e := os.ReadFile(filepath.Join(dir, s.File))
		if e != nil {
			return e
		}
		if len(b) != s.Bytes {
			return fmt.Errorf("source_size: %s", name)
		}
		if e = s.Ref.Verify(b); e != nil {
			return fmt.Errorf("source_hash: %s: %w", name, e)
		}
		allowed[s.Ref] = true
	}
	// 仅有语法正确的引用不够，逐条绑定已经核字节的源锁。
	for _, series := range calendar.Series {
		for _, record := range series.Records {
			seen := make(map[evidence.Ref]bool)
			for _, ref := range record.Evidence {
				if !allowed[ref] {
					return fmt.Errorf("calendar_evidence_binding: %s", record.Day)
				}
				seen[ref] = true
			}
			version := "trade-rule-202011-doc"
			if record.Day >= 20210611 {
				version = "trade-rule-202106-doc"
			}
			if record.Day >= 20211126 {
				version = "trade-rule-202111-doc"
			}
			for _, role := range []string{"lu-listing-primary", "holiday-year-2021", version} {
				if !seen[input.Sources[role].Ref] {
					return fmt.Errorf("calendar_evidence_required: %s %s", record.Day, role)
				}
			}
		}
	}
	return nil
}
func ordinals(cal *verified.Calendar, dir string) error {
	var input []struct {
		Month int    `json:"month"`
		File  string `json:"file"`
		Ref   struct {
			SHA string `json:"sha256"`
		} `json:"ref"`
	}
	if err := read(filepath.Join(dir, "monthly-ordinal-input.json"), &input); err != nil {
		return err
	}
	if len(input) != 12 {
		return fmt.Errorf("ordinal_months")
	}
	for i, r := range input {
		if r.Month != i+1 || filepath.Base(r.File) != r.File {
			return fmt.Errorf("ordinal_identity")
		}
		b, e := os.ReadFile(filepath.Join(dir, r.File))
		if e != nil {
			return e
		}
		sum := sha256.Sum256(b)
		if hex.EncodeToString(sum[:]) != r.Ref.SHA {
			return fmt.Errorf("ordinal_hash")
		}
		var obj struct {
			Date    string          `json:"report_date"`
			Ordinal json.RawMessage `json:"o_year_num"`
			Code    json.RawMessage `json:"o_code"`
		}
		if e = json.Unmarshal(b, &obj); e != nil {
			return e
		}
		n, e := strconv.Atoi(strings.Trim(string(obj.Ordinal), "\""))
		if e != nil || strings.Trim(string(obj.Code), "\"") != "0" {
			return fmt.Errorf("ordinal_status")
		}
		d, e := strconv.Atoi(obj.Date)
		if e != nil || d/100 != 202100+r.Month {
			return fmt.Errorf("ordinal_report_day")
		}
		count := 0
		e = cal.Walk(key, 20210101, tf.TradingDay(d), func(tf.Day) bool { count++; return true })
		if e != nil || count != n {
			return fmt.Errorf("ordinal_mismatch: %d got%d want%d", r.Month, count, n)
		}
	}
	return nil
}
func market(cal *verified.Calendar, dir, out string) error {
	var input struct {
		File string       `json:"file"`
		Ref  evidence.Ref `json:"ref"`
	}
	if e := read(filepath.Join(dir, "market-input.json"), &input); e != nil {
		return e
	}
	if filepath.Base(input.File) != input.File {
		return fmt.Errorf("unsafe_market_path")
	}
	raw, e := os.ReadFile(filepath.Join(dir, input.File))
	if e != nil {
		return e
	}
	now := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC).UnixMilli()
	src, e := officialdaily.New(cal, []officialdaily.Document{{Format: "shfe-json", Day: 20210728, Evidence: input.Ref, Raw: raw}}, nil, now)
	if e != nil {
		return e
	}
	bad, _ := tf.ParseSymbol("INE.lu2108")
	good, _ := tf.ParseSymbol("INE.lu2109")
	if bars, e := src.Bars(context.Background(), tf.BarRequest{Symbol: bad, Period: tf.Daily, From: 20210728, To: 20210728}); e == nil || bars != nil || !strings.Contains(e.Error(), "OHLC隔离") {
		return fmt.Errorf("market_ohlc_guard")
	}
	request := tf.SyncRequest{Symbol: good, Period: tf.Daily, From: 20210728, To: 20210728}
	for round := 0; round < 2; round++ {
		store, tail, e := segfile.Open(filepath.Join(out, "lu2109"), tf.Daily)
		if e != nil {
			return e
		}
		if tail != 0 {
			store.Close()
			return fmt.Errorf("market_truncated")
		}
		syncer, e := tf.NewSyncer(tf.SyncerConfig{Calendar: cal, Store: store, NewSource: func(*http.Client) tf.Source { return src }, Pacer: pacing.NoPacing(), Timeout: time.Second})
		if e != nil {
			store.Close()
			return e
		}
		r, e := syncer.Sync(context.Background(), request, now)
		if e != nil || !r.Complete() || (round == 0 && r.Bars != 1) || (round == 1 && r.Bars != 0) {
			store.Close()
			return fmt.Errorf("market_sync: %+v %v", r, e)
		}
		feed, e := tf.NewFeed(store, tf.FeedConfig{Key: key, Calendar: cal, Base: tf.Daily, Rule: tf.AggTradingAxis, From: 20210728, To: 20210728})
		if e != nil {
			store.Close()
			return e
		}
		n := 0
		for feed.Next() {
			n++
			b := feed.View().Bar()
			if b.Close != 3476 || b.Settle != 3475 || b.Ts != at(20210727, 1260) || b.TsEnd != at(20210728, 900) {
				feed.Close()
				store.Close()
				return fmt.Errorf("market_feed_fields")
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
		if n != 1 {
			return fmt.Errorf("market_feed_count")
		}
	}
	return nil
}
func run(calendar, facts, dir, out string, withMarket bool) error {
	if err := os.Mkdir(out, 0700); err != nil {
		return fmt.Errorf("输出目录必须全新: %w", err)
	}
	var input envelope
	if e := read(calendar, &input); e != nil {
		return e
	}
	if e := sourceHashes(facts, dir, input); e != nil {
		return e
	}
	cal, n, e := profile(input)
	if e != nil {
		return e
	}
	if e = ordinals(cal, dir); e != nil {
		return e
	}
	if withMarket {
		if e = market(cal, dir, out); e != nil {
			return e
		}
	}
	report := map[string]any{"status": "passed", "natural_days": 365, "trading_days": n, "official_monthly_ordinal_checks": 12, "real_market_pipeline": withMarket, "scope": "LU2021 official published schedule; no price/full-universe/tick-completeness certification"}
	b, e := json.MarshalIndent(report, "", "  ")
	if e != nil {
		return e
	}
	return os.WriteFile(filepath.Join(out, "report.json"), b, 0600)
}
func main() {
	calendar := flag.String("calendar", "", "生成的日历JSON")
	facts := flag.String("facts", "", "固定事实与源锁JSON")
	dir := flag.String("evidence-dir", "", "冻结原件目录")
	out := flag.String("out", "", "全新核查输出目录")
	withMarket := flag.Bool("market", false, "核真实LU日原件Source/Store/Feed及OHLC拒绝")
	flag.Parse()
	if e := run(*calendar, *facts, *dir, *out, *withMarket); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
