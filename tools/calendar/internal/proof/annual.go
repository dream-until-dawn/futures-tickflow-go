package proof

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"reflect"
	"time"

	tf "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/verified"
	"github.com/dream-until-dawn/futures-tickflow-go/refdata/evidence"
)

// annualSpec 是独立核阅原文后写入的审核表，不读取编译器的节假日配置。
// 新年份不能仅靠提交一个自带SHA的JSON获得资格。
type annualSpec struct {
	year         int
	factsSHA     string
	holiday      [][2]tf.TradingDay
	dayOnly      map[tf.TradingDay]bool
	products     map[string]annualProduct
	openDays     int
	events       []annualEvent
	ordinalDates []tf.TradingDay
}
type annualEvent struct {
	from, to tf.TradingDay
	role     string
}
type annualVersion struct {
	from, to tf.TradingDay
	roles    []string
}
type annualProduct struct {
	from, to           tf.TradingDay
	listed             tf.TradingDay
	nightEnd, openDays int
	versions           []annualVersion
}

func spec2025() annualSpec {
	s := annualSpec{year: 2025, factsSHA: "830ed4263db18d942cca9c8cb2c44070b8c752a3d47ca39a5cbbfac21ca66181", openDays: 243,
		holiday: [][2]tf.TradingDay{{20250101, 20250101}, {20250128, 20250204}, {20250404, 20250406}, {20250501, 20250505}, {20250531, 20250602}, {20251001, 20251008}},
		dayOnly: map[tf.TradingDay]bool{20250102: true, 20250205: true, 20250407: true, 20250506: true, 20250603: true, 20251009: true}, products: map[string]annualProduct{}}
	for _, p := range []string{"ao", "br", "ad", "op"} {
		x := annualProduct{from: 20250101, to: 20251231, nightEnd: 1380, openDays: 243}
		if p == "ao" || p == "ad" {
			x.nightEnd = 1500
		}
		oldBusiness := "shfe-ao-business-doc"
		if p == "br" {
			oldBusiness = "shfe-br-business-docx"
		}
		newBusiness := p + "-business-2025-doc"
		if p == "ad" {
			x.from = 20250610
			x.listed = 20250610
			x.openDays = 141
			oldBusiness = "ad-business-first-docx"
			newBusiness = "ad-business-202508-docx"
		}
		x.versions = []annualVersion{{20241023, 20250807, []string{"shfe-management-docx", "shfe-rules-2024", oldBusiness}}, {20250808, 20251231, []string{"management-2025-doc", "rules-202507", newBusiness}}}
		if p == "ad" {
			x.versions[0].from = 20250610
			x.versions[0].roles = append(x.versions[0].roles, "ad-rules")
		}
		if p == "op" {
			x.from = 20250910
			x.listed = 20250910
			x.openDays = 75
			x.versions = []annualVersion{{20250818, 20251231, []string{"management-2025-doc", "rules-202507", "op-rules", "op-business-first-docx"}}}
		}
		s.products[p] = x
	}
	s.events = []annualEvent{{20250101, 20250102, "shfe-boundary-2025"}, {20250127, 20250205, "spring-2025"}, {20250403, 20250407, "qingming-2025"}, {20250430, 20250506, "labor-2025"}, {20250530, 20250603, "dragon-2025"}, {20250930, 20251009, "national-2025"}, {20251231, 20251231, "boundary-2026"}}
	return s
}

func (s annualSpec) closed(day tf.TradingDay) bool {
	w := natural(day).Weekday()
	if w == time.Saturday || w == time.Sunday {
		return true
	}
	for _, h := range s.holiday {
		if day >= h[0] && day <= h[1] {
			return true
		}
	}
	return false
}

func (s annualSpec) version(p annualProduct, day tf.TradingDay) (annualVersion, error) {
	for _, v := range p.versions {
		if day >= v.from && day <= v.to {
			return v, nil
		}
	}
	return annualVersion{}, fmt.Errorf("annual_version_range")
}

func bindAnnual(input calendarFile, lock sourceLock, facts, dir string, spec annualSpec) error {
	raw, e := os.ReadFile(facts)
	if e != nil {
		return e
	}
	sum := sha256.Sum256(raw)
	if input.Year != spec.year || input.FactsSHA != spec.factsSHA || hex.EncodeToString(sum[:]) != spec.factsSHA {
		return fmt.Errorf("annual_facts_pin")
	}
	allowed, e := sourceAllowlist(lock, dir)
	if e != nil {
		return e
	}
	for _, s := range input.Series {
		p, ok := spec.products[s.Product.Product]
		if s.Product.Exchange != tf.SHFE || !ok {
			return fmt.Errorf("annual_product")
		}
		for _, r := range s.Records {
			seen := map[evidence.Ref]bool{}
			for _, ref := range r.Evidence {
				if !allowed[ref] {
					return fmt.Errorf("annual_evidence_binding")
				}
				seen[ref] = true
			}
			v, e := spec.version(p, r.Day)
			if e != nil {
				return e
			}
			roles := append([]string{s.Product.Product + "-listing", fmt.Sprintf("shfe-year-%d", spec.year)}, v.roles...)
			// 年度公告与逐节详细通知分别绑定，不能删掉停夜盘依据仍通过审核。
			for _, event := range spec.events {
				if r.Day >= event.from && r.Day <= event.to {
					roles = append(roles, event.role)
				}
			}
			for _, role := range roles {
				ref, ok := lock.Sources[role]
				if !ok || !seen[ref.Ref] {
					return fmt.Errorf("annual_required_evidence: %s %s", r.Day, role)
				}
			}
		}
	}
	return nil
}

func profileAnnual(input calendarFile, spec annualSpec) (*verified.Calendar, error) {
	if input.Schema != "annual-calendar-v1" || input.Year != spec.year || len(input.Series) != len(spec.products) {
		return nil, fmt.Errorf("annual_profile_identity")
	}
	cal, e := verified.New(input.Series)
	if e != nil {
		return nil, fmt.Errorf("annual_structure: %w", e)
	}
	seen := map[string]bool{}
	for _, s := range input.Series {
		p, ok := spec.products[s.Product.Product]
		if s.Product.Exchange != tf.SHFE || !ok || seen[s.Product.Product] {
			return nil, fmt.Errorf("annual_product")
		}
		seen[s.Product.Product] = true
		wantDays := int(natural(p.to).Sub(natural(p.from)).Hours()/24) + 1
		if len(s.Records) != wantDays {
			return nil, fmt.Errorf("annual_natural_coverage")
		}
		count := 0
		for i, r := range s.Records {
			if r.Day != ymd(natural(p.from).AddDate(0, 0, i)) {
				return nil, fmt.Errorf("annual_natural_coverage")
			}
			v, e := spec.version(p, r.Day)
			if e != nil {
				return nil, e
			}
			template := tf.SessionTemplate{From: v.from, To: v.to, Day: []tf.Session{{Start: 540 * 60000, End: 615 * 60000}, {Start: 630 * 60000, End: 690 * 60000}, {Start: 810 * 60000, End: 900 * 60000}}, Night: []tf.Session{{Start: 1260 * 60000, End: int64(p.nightEnd) * 60000}}}
			if !reflect.DeepEqual(template, r.Template) {
				return nil, fmt.Errorf("annual_template_version: %s", r.Day)
			}
			d, e := cal.DayOf(s.Product, r.Day)
			if spec.closed(r.Day) {
				if !errors.Is(e, tf.ErrNotTradingDay) {
					return nil, fmt.Errorf("annual_holiday_open")
				}
				continue
			}
			if e != nil {
				return nil, e
			}
			count++
			var sessions []tf.Session
			// 只有原文明确的上市首日才能省略前夜，部分年起点本身不等于上市。
			firstListing := r.Day == p.listed
			if !spec.dayOnly[r.Day] && !firstListing {
				previous := natural(r.Day).AddDate(0, 0, -1)
				for spec.closed(ymd(previous)) {
					previous = previous.AddDate(0, 0, -1)
				}
				sessions = append(sessions, tf.Session{Start: instant(ymd(previous), 1260), End: instant(ymd(previous), p.nightEnd)})
			}
			for _, m := range [][2]int{{540, 615}, {630, 690}, {810, 900}} {
				sessions = append(sessions, tf.Session{Start: instant(r.Day, m[0]), End: instant(r.Day, m[1])})
			}
			if !reflect.DeepEqual(sessions, d.Sessions) {
				return nil, fmt.Errorf("annual_absolute_session: %s", r.Day)
			}
		}
		if count != p.openDays {
			return nil, fmt.Errorf("annual_open_count")
		}
		for _, day := range []tf.TradingDay{ymd(natural(p.from).AddDate(0, 0, -1)), ymd(natural(p.to).AddDate(0, 0, 1))} {
			if _, e := cal.DayOf(s.Product, day); !errors.Is(e, tf.ErrUncovered) {
				return nil, fmt.Errorf("annual_scope_extension")
			}
		}
		if _, e := cal.DayAt(s.Product, instant(p.to, 1260)); !errors.Is(e, tf.ErrUncovered) {
			return nil, fmt.Errorf("annual_last_instant_scope")
		}
	}
	return cal, nil
}
