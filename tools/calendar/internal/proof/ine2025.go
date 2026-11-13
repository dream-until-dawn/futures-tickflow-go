package proof

import tf "github.com/dream-until-dawn/futures-tickflow-go"

// specINE2025 来自能源中心年度公告、逐节通知及规则原件；不借用SHFE规则切换日期。
// 后继规则明确2026-01-01生效，2025仍沿用2023-11-21交易细则。
func specINE2025() annualSpec {
	s := annualSpec{exchange: tf.INE, yearRole: "ine-year-2025", year: 2025,
		factsSHA: "96bd923b585d1a914943502f26abb88969a077b1087ccea0409720fbe2c11eec", openDays: 243,
		holiday:      [][2]tf.TradingDay{{20250101, 20250101}, {20250128, 20250204}, {20250404, 20250406}, {20250501, 20250505}, {20250531, 20250602}, {20251001, 20251008}},
		dayOnly:      map[tf.TradingDay]bool{20250102: true, 20250205: true, 20250407: true, 20250506: true, 20250603: true, 20251009: true},
		events:       []annualEvent{{20250101, 20250102, "ine-year-2025"}, {20250127, 20250205, "spring-2025"}, {20250403, 20250407, "qingming-2025"}, {20250430, 20250506, "labor-2025"}, {20250530, 20250603, "dragon-2025"}, {20250930, 20251009, "national-2025"}, {20251231, 20251231, "boundary-2026"}},
		ordinalDates: []tf.TradingDay{20250102, 20250127, 20250228, 20250331, 20250430, 20250530, 20250630, 20250731, 20250829, 20250930, 20251031, 20251128, 20251231},
		products:     map[string]annualProduct{},
	}
	versions := []annualVersion{{20231121, 20251231, []string{"ine-trade-202311-docx-text", "ine-rules-202311-repub", "ine-trade-202311-docx", "ine-next-rules-publication", "ine-next-rule-doc", "ine-next-rule-doc-text"}}}
	s.products["lu"] = annualProduct{from: 20250101, to: 20251231, listed: 20200622, nightEnd: 1380, openDays: 243, versions: versions}
	s.products["ec"] = annualProduct{from: 20250101, to: 20251231, listed: 20230818, nightEnd: 0, openDays: 243, versions: versions}
	return s
}
