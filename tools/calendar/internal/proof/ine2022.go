package proof

import tf "github.com/dream-until-dawn/futures-tickflow-go"

// specINE2022 使用年度62号直接明确的劳动/中秋安排，失败详细页不进入源锁。
func specINE2022() annualSpec {
	s := annualSpec{exchange: tf.INE, yearRole: "ine-year-2022", year: 2022,
		factsSHA: "16533cf07b1438986ac543380836f8c19f6793bc9759a0b7b806c4a18e4926bc", openDays: 242,
		holiday:      [][2]tf.TradingDay{{20220101, 20220103}, {20220131, 20220206}, {20220403, 20220405}, {20220430, 20220504}, {20220603, 20220605}, {20220910, 20220912}, {20221001, 20221007}},
		dayOnly:      map[tf.TradingDay]bool{20220104: true, 20220207: true, 20220406: true, 20220505: true, 20220606: true, 20220913: true, 20221010: true},
		events:       []annualEvent{{20220101, 20220104, "boundary-2022"}, {20220128, 20220207, "spring-2022"}, {20220401, 20220406, "qingming-2022"}, {20220429, 20220505, "ine-year-2022"}, {20220602, 20220606, "dragon-2022"}, {20220909, 20220913, "ine-year-2022"}, {20220930, 20221010, "national-2022"}, {20221230, 20221231, "boundary-2023"}},
		ordinalDates: []tf.TradingDay{20220104, 20220128, 20220228, 20220331, 20220429, 20220531, 20220630, 20220729, 20220831, 20220930, 20221031, 20221130, 20221230},
		products:     map[string]annualProduct{},
	}
	s.products["lu"] = annualProduct{from: 20220101, to: 20221231, listed: 20200622, nightEnd: 1380, openDays: 242,
		versions: []annualVersion{{20211126, 20221231, []string{"ine-trade-202111-text", "ine-rules-202111", "ine-trade-202111-doc", "ine-next-publication", "ine-next-docx", "ine-next-text"}}}}
	return s
}
