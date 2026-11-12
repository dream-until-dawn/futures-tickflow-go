package proof

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"time"

	tf "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/verified"
	"github.com/dream-until-dawn/futures-tickflow-go/refdata/evidence"
)

// 仅放行独立审核过的事实集；修改配置和其自带审计文件不能自行扩大Go验收范围。
const facts2024SHA = "8fedb85d56425e320ae26120002c540813345bd412e3bb5aa95beaca5ebfcf59"

func bind2024(input calendarFile, lock sourceLock, facts, dir string) error {
	raw, e := os.ReadFile(facts)
	if e != nil {
		return e
	}
	sum := sha256.Sum256(raw)
	if input.Year != 2024 || input.FactsSHA != facts2024SHA || hex.EncodeToString(sum[:]) != facts2024SHA {
		return fmt.Errorf("annual_facts_pin")
	}
	allowed, e := sourceAllowlist(lock, dir)
	if e != nil {
		return e
	}
	for _, s := range input.Series {
		for _, r := range s.Records {
			seen := map[evidence.Ref]bool{}
			for _, ref := range r.Evidence {
				if !allowed[ref] {
					return fmt.Errorf("annual_evidence_binding")
				}
				seen[ref] = true
			}
			roles := []string{s.Product.Product + "-listing", "shfe-year-2024", "shfe-trade-docx", "shfe-night-docx", "shfe-rules-202304"}
			if r.Day >= 20241023 {
				business := "shfe-br-business-docx"
				if s.Product.Product == "ao" {
					business = "shfe-ao-business-doc"
				}
				roles = []string{s.Product.Product + "-listing", "shfe-year-2024", "shfe-management-docx", "shfe-rules-2024", business}
			}
			for _, role := range roles {
				if !seen[lock.Sources[role].Ref] {
					return fmt.Errorf("annual_required_evidence: %s %s", r.Day, role)
				}
			}
		}
	}
	return nil
}

func closed2024(day tf.TradingDay) bool {
	w := natural(day).Weekday()
	if w == time.Saturday || w == time.Sunday {
		return true
	}
	// 日期从公告独立复核，不读取Python事件列表；补班周末依然休市。
	for _, r := range [][2]tf.TradingDay{{20240101, 20240101}, {20240209, 20240217}, {20240404, 20240406}, {20240501, 20240505}, {20240608, 20240610}, {20240915, 20240917}, {20241001, 20241007}} {
		if day >= r[0] && day <= r[1] {
			return true
		}
	}
	return false
}

func profile2024(input calendarFile) (*verified.Calendar, error) {
	if input.Schema != "annual-calendar-v1" || input.Year != 2024 || len(input.Series) != 2 {
		return nil, fmt.Errorf("annual_profile_identity")
	}
	cal, e := verified.New(input.Series)
	if e != nil {
		return nil, fmt.Errorf("annual_structure: %w", e)
	}
	dayOnly := map[tf.TradingDay]bool{20240102: true, 20240219: true, 20240408: true, 20240506: true, 20240611: true, 20240918: true, 20241008: true}
	seen := map[string]bool{}
	for _, s := range input.Series {
		if s.Product.Exchange != tf.SHFE || (s.Product.Product != "ao" && s.Product.Product != "br") || seen[s.Product.Product] {
			return nil, fmt.Errorf("annual_product")
		}
		seen[s.Product.Product] = true
		if len(s.Records) != 366 {
			return nil, fmt.Errorf("annual_natural_coverage")
		}
		nightEnd := 1380
		if s.Product.Product == "ao" {
			nightEnd = 1500
		}
		count := 0
		for i, r := range s.Records {
			if r.Day != ymd(natural(20240101).AddDate(0, 0, i)) {
				return nil, fmt.Errorf("annual_natural_coverage")
			}
			from, to := tf.TradingDay(20230526), tf.TradingDay(20241022)
			if r.Day >= 20241023 {
				from, to = 20241023, 20241231
			}
			template := tf.SessionTemplate{From: from, To: to, Day: []tf.Session{{Start: 540 * 60000, End: 615 * 60000}, {Start: 630 * 60000, End: 690 * 60000}, {Start: 810 * 60000, End: 900 * 60000}}, Night: []tf.Session{{Start: 1260 * 60000, End: int64(nightEnd) * 60000}}}
			if !reflect.DeepEqual(template, r.Template) {
				return nil, fmt.Errorf("annual_template_version: %s", r.Day)
			}
			d, err := cal.DayOf(s.Product, r.Day)
			if closed2024(r.Day) {
				if !errors.Is(err, tf.ErrNotTradingDay) {
					return nil, fmt.Errorf("annual_holiday_open")
				}
				continue
			}
			if err != nil {
				return nil, err
			}
			count++
			var sessions []tf.Session
			if !dayOnly[r.Day] {
				previous := natural(r.Day).AddDate(0, 0, -1)
				for closed2024(ymd(previous)) {
					previous = previous.AddDate(0, 0, -1)
				}
				sessions = append(sessions, tf.Session{Start: instant(ymd(previous), 1260), End: instant(ymd(previous), nightEnd)})
			}
			for _, m := range [][2]int{{540, 615}, {630, 690}, {810, 900}} {
				sessions = append(sessions, tf.Session{Start: instant(r.Day, m[0]), End: instant(r.Day, m[1])})
			}
			if !reflect.DeepEqual(sessions, d.Sessions) {
				return nil, fmt.Errorf("annual_absolute_session: %s", r.Day)
			}
		}
		if count != 242 {
			return nil, fmt.Errorf("annual_open_count")
		}
		for _, day := range []tf.TradingDay{20231231, 20250101} {
			if _, e := cal.DayOf(s.Product, day); !errors.Is(e, tf.ErrUncovered) {
				return nil, fmt.Errorf("annual_scope_extension")
			}
		}
		for _, p := range [][2]int{{20240208, 1260}, {20240913, 1260}, {20240930, 1260}, {20241023, 615}, {20241023, 690}, {20241023, 900}} {
			if _, e := cal.DayAt(s.Product, instant(tf.TradingDay(p[0]), p[1])); !errors.Is(e, tf.ErrClosed) {
				return nil, fmt.Errorf("annual_closed_instant: %s %v: %v", s.Product, p, e)
			}
		}
		// DayAt按首末实际时段界定绝对时刻覆盖；末日收盘后的查询不能冒充已覆盖休市。
		if _, e := cal.DayAt(s.Product, instant(20241231, 1260)); !errors.Is(e, tf.ErrUncovered) {
			return nil, fmt.Errorf("annual_last_instant_scope")
		}
		// 闰日和规则切换前夜的实际归属分别检查，不能靠全年计数通过。
		for _, p := range []struct {
			date   tf.TradingDay
			minute int
			want   tf.TradingDay
		}{{20240229, 540, 20240229}, {20241022, 1260, 20241023}, {20241025, 1260, 20241028}} {
			d, e := cal.DayAt(s.Product, instant(p.date, p.minute))
			if e != nil || d.Num != p.want {
				return nil, fmt.Errorf("annual_day_at")
			}
		}
	}
	return cal, nil
}

func observations2024(cal *verified.Calendar, dir string) (int, error) {
	return observationsAnnual(cal, dir, annualSpec{year: 2024, openDays: 242, products: map[string]annualProduct{
		"ao": {from: 20240101, to: 20241231}, "br": {from: 20240101, to: 20241231},
	}})
}

// observationsAnnual 用另一品种的全年原始公布日期交叉核查；部分年只比较覆盖交集。
// 该集合证明日期相容，不代替时段公告、上市依据或官方交易日序号。
func observationsAnnual(cal *verified.Calendar, dir string, spec annualSpec) (int, error) {
	var input struct {
		Original fileRef `json:"original"`
		Artifact fileRef `json:"artifact"`
		Scanner  string  `json:"scanner_file"`
	}
	if e := read(filepath.Join(dir, "annual-observation-input.json"), &input); e != nil {
		return 0, e
	}
	if _, e := load(dir, input.Original); e != nil {
		return 0, e
	}
	raw, e := load(dir, input.Artifact)
	if e != nil {
		return 0, e
	}
	if filepath.Base(input.Scanner) != input.Scanner {
		return 0, fmt.Errorf("observation_scanner_path")
	}
	code, e := os.ReadFile(filepath.Join(dir, input.Scanner))
	if e != nil {
		return 0, e
	}
	sum := sha256.Sum256(code)
	scannerSHA := hex.EncodeToString(sum[:])
	var data struct {
		Schema     string                     `json:"schema"`
		Year       int                        `json:"year"`
		OriginSHA  string                     `json:"origin_sha256"`
		ScannerSHA string                     `json:"scanner_sha256"`
		Months     map[string][]tf.TradingDay `json:"months"`
	}
	if e = json.Unmarshal(raw, &data); e != nil {
		return 0, e
	}
	if data.Schema != "shfe-rb-observed-days-v1" || data.Year != spec.year || data.OriginSHA != input.Original.Ref.SHA256 || data.ScannerSHA != scannerSHA || input.Artifact.Ref.Version != scannerSHA || len(data.Months) != 12 {
		return 0, fmt.Errorf("observation_identity")
	}
	total, dates := 0, 0
	for month := 1; month <= 12; month++ {
		expected, ok := data.Months[fmt.Sprintf("%02d", month)]
		if !ok || len(expected) == 0 {
			return 0, fmt.Errorf("observation_month")
		}
		for i, d := range expected {
			y, m, _ := d.Split()
			if y != spec.year || m != month || (i > 0 && expected[i-1] >= d) {
				return 0, fmt.Errorf("observation_date")
			}
		}
		dates += len(expected)
		monthFrom := ymd(time.Date(spec.year, time.Month(month), 1, 0, 0, 0, 0, zone))
		monthTo := ymd(time.Date(spec.year, time.Month(month+1), 0, 0, 0, 0, 0, zone))
		for product, p := range spec.products {
			from, to := max(monthFrom, p.from), min(monthTo, p.to)
			if from > to {
				continue
			}
			want := []tf.TradingDay{}
			for _, d := range expected {
				if d >= from && d <= to {
					want = append(want, d)
				}
			}
			actual := []tf.TradingDay{}
			e = cal.Walk(tf.ProductKey{Exchange: tf.SHFE, Product: product}, from, to, func(d tf.Day) bool { actual = append(actual, d.Num); return true })
			if e != nil || !reflect.DeepEqual(actual, want) {
				return 0, fmt.Errorf("observation_date_set: %s month%d", product, month)
			}
			total++
		}
	}
	if dates != spec.openDays {
		return 0, fmt.Errorf("observation_total")
	}
	return total, nil
}
