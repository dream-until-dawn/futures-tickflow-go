package proof

import (
	"fmt"
	"path/filepath"

	tf "github.com/dream-until-dawn/futures-tickflow-go"
	"github.com/dream-until-dawn/futures-tickflow-go/calendar/verified"
)

// 2026只认证截至已核历史目标11月4日，不猜测2027元旦边界。
func spec2026() annualSpec {
	s := annualSpec{year: 2026, factsSHA: "9d59bae9cddb9f519f4da49811a125e488d7a8d28990d0d1f73047dff4fd8c55", openDays: 201,
		holiday: [][2]tf.TradingDay{{20260101, 20260104}, {20260214, 20260223}, {20260404, 20260406}, {20260501, 20260505}, {20260619, 20260621}, {20260925, 20260927}, {20261001, 20261007}},
		dayOnly: map[tf.TradingDay]bool{20260105: true, 20260224: true, 20260407: true, 20260506: true, 20260622: true, 20260928: true, 20261008: true}, products: map[string]annualProduct{},
		events:       []annualEvent{{20260101, 20260105, "boundary-2026"}, {20260213, 20260224, "spring-2026"}, {20260403, 20260407, "qingming-2026"}, {20260430, 20260506, "labor-2026"}, {20260618, 20260622, "dragon-2026"}, {20260924, 20260928, "autumn-national-2026"}, {20260930, 20261008, "autumn-national-2026"}},
		ordinalDates: []tf.TradingDay{20260130, 20260227, 20260331, 20260430, 20260529, 20260630, 20260731, 20260831, 20260930, 20261030, 20261104}}
	for _, product := range []string{"ao", "br", "ad", "op"} {
		p := annualProduct{from: 20260101, to: 20261104, nightEnd: 1380, openDays: 201}
		if product == "ao" || product == "ad" {
			p.nightEnd = 1500
		}
		p.versions = []annualVersion{{20260101, 20260705, []string{"management-2025-doc", "rules-202507", "next-hedging-rules", "business-next-2026-docx"}}, {20260706, 20260913, []string{"management-202607-doc", "manager-202606-release", "next-hedging-rules", "business-next-2026-docx"}}, {20260914, 20261104, []string{"management-202609-docx", "manager-202609-release", "next-hedging-rules", "business-next-2026-docx"}}}
		s.products[product] = p
	}
	return s
}

// annualOrdinals 从已验SHA的官方日原件读取年内序号，不以本地编译器的日期集合自证。
// 月末锚点精确固定；它是累计数量交叉检查，不认证中间每一天的价格或临时暂停。
func annualOrdinals(cal *verified.Calendar, dir string, spec annualSpec) (int, error) {
	var files []fileRef
	if e := read(filepath.Join(dir, "annual-ordinal-input.json"), &files); e != nil {
		return 0, e
	}
	if len(files) != len(spec.ordinalDates) || len(files) == 0 {
		return 0, fmt.Errorf("annual_ordinal_anchors")
	}
	base := 0
	if spec.ordinalBase != 0 {
		var f fileRef
		if e := read(filepath.Join(dir, "annual-base-input.json"), &f); e != nil {
			return 0, fmt.Errorf("annual_ordinal_base: %w", e)
		}
		day, n, e := ordinal(dir, f)
		if e != nil {
			return 0, e
		}
		// 基数本身也固定核过，不能把整串官方序号同时平移后仍通过。
		if day != spec.ordinalBase || n != spec.ordinalBaseCount || n <= 0 {
			return 0, fmt.Errorf("annual_ordinal_base_identity")
		}
		base = n
	} else if spec.ordinalBaseCount != 0 {
		return 0, fmt.Errorf("annual_ordinal_base_config")
	}
	total, previous := 0, base
	for i, f := range files {
		day, n, e := ordinal(dir, f)
		if e != nil {
			return 0, e
		}
		if day != spec.ordinalDates[i] || n <= previous {
			return 0, fmt.Errorf("annual_ordinal_identity")
		}
		previous = n
		for product, p := range spec.products {
			if day > p.to {
				return 0, fmt.Errorf("annual_ordinal_scope")
			}
			if spec.ordinalBase == 0 {
				if p.from != tf.TradingDay(spec.year*10000+101) {
					return 0, fmt.Errorf("annual_ordinal_scope")
				}
			} else {
				prior := natural(p.from).AddDate(0, 0, -1)
				for spec.closed(ymd(prior)) {
					prior = prior.AddDate(0, 0, -1)
				}
				if p.from != p.listed || spec.ordinalDates[0] != p.from || ymd(prior) != spec.ordinalBase {
					return 0, fmt.Errorf("annual_ordinal_scope")
				}
			}
			count := 0
			e := cal.Walk(tf.ProductKey{Exchange: spec.exchangeKey(), Product: product}, p.from, day, func(tf.Day) bool { count++; return true })
			if e != nil || count != n-base {
				return 0, fmt.Errorf("annual_ordinal_mismatch: %s %s", product, day)
			}
			total++
		}
	}
	if previous-base != spec.openDays {
		return 0, fmt.Errorf("annual_ordinal_total")
	}
	return total, nil
}
