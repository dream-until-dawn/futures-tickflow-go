package proof

import tf "github.com/dream-until-dawn/futures-tickflow-go"

// specINE2020 仅从LU上市日起核验；上市前INE市场序号是基数，不是LU挂牌或价格证据。
func specINE2020() annualSpec {
	s := annualSpec{exchange: tf.INE, yearRole: "ine-year-2020", year: 2020, factsSHA: "e7b2778fe96a23b79bdc697f43f7548218a945e9e1abc96872ade34cd4e4ac11", openDays: 131,
		holiday: [][2]tf.TradingDay{{20200625, 20200627}, {20201001, 20201008}}, dayOnly: map[tf.TradingDay]bool{20200629: true, 20201009: true},
		events:      []annualEvent{{20200624, 20200629, "dragon-2020"}, {20200930, 20201009, "autumn-2020"}, {20201231, 20201231, "boundary-2021"}},
		ordinalBase: 20200619, ordinalBaseCount: 112,
		ordinalDates: []tf.TradingDay{20200622, 20200630, 20200731, 20200831, 20200930, 20201030, 20201130, 20201231}, products: map[string]annualProduct{}}
	s.products["lu"] = annualProduct{from: 20200622, to: 20201231, listed: 20200622, nightEnd: 1380, openDays: 131, versions: []annualVersion{
		{20200610, 20201110, []string{"ine-first-trade-text", "ine-first-publication", "ine-first-trade-doc", "ine-rules-202011", "ine-trade-202011-doc", "ine-trade-202011-text"}},
		{20201111, 20201231, []string{"ine-trade-202011-text", "ine-rules-202011", "ine-trade-202011-doc", "ine-next-202106-publication", "ine-next-202106-doc", "ine-next-202106-text"}},
	}}
	return s
}
