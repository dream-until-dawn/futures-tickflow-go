package proof

import tf "github.com/dream-until-dawn/futures-tickflow-go"

// specINE2024 的端午事实由完整年度74号直接明确，详细通知挑战页不作证据。
func specINE2024() annualSpec {
	s := annualSpec{exchange: tf.INE, yearRole: "ine-year-2024", year: 2024, factsSHA: "b6bf03d732522f83065cf0cd145f10871af5851c499b778b11eac0cf91f1da57", openDays: 242,
		holiday:      [][2]tf.TradingDay{{20240101, 20240101}, {20240209, 20240217}, {20240404, 20240406}, {20240501, 20240505}, {20240608, 20240610}, {20240915, 20240917}, {20241001, 20241007}},
		dayOnly:      map[tf.TradingDay]bool{20240102: true, 20240219: true, 20240408: true, 20240506: true, 20240611: true, 20240918: true, 20241008: true},
		events:       []annualEvent{{20240101, 20240102, "boundary-2024"}, {20240208, 20240219, "spring-2024"}, {20240403, 20240408, "qingming-2024"}, {20240430, 20240506, "labor-2024"}, {20240607, 20240611, "ine-year-2024"}, {20240913, 20240918, "autumn-2024"}, {20240930, 20241008, "national-2024"}, {20241231, 20241231, "boundary-2025"}},
		ordinalDates: []tf.TradingDay{20240102, 20240131, 20240229, 20240329, 20240430, 20240531, 20240628, 20240731, 20240830, 20240930, 20241031, 20241129, 20241231}, products: map[string]annualProduct{}}
	v := []annualVersion{{20231121, 20241231, []string{"ine-trade-202311-docx-text", "ine-rules-202311-repub", "ine-trade-202311-docx", "ine-next-rules-publication", "ine-next-rule-doc", "ine-next-rule-doc-text"}}}
	s.products["lu"] = annualProduct{from: 20240101, to: 20241231, listed: 20200622, nightEnd: 1380, openDays: 242, versions: v}
	s.products["ec"] = annualProduct{from: 20240101, to: 20241231, listed: 20230818, openDays: 242, versions: v}
	return s
}
