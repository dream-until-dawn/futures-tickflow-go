package proof

import tf "github.com/dream-until-dawn/futures-tickflow-go"

// specINE2026 只核已发生至11月4日；规则实施日取各自正式公告，不从栏目日期推断。
func specINE2026() annualSpec {
	s := annualSpec{exchange: tf.INE, yearRole: "ine-year-2026", year: 2026,
		factsSHA: "0d24ae1bfcfadb2b7b3c6c387ea91eed8d3a5b5b58f4fe1c7319b1e973587842", openDays: 201,
		holiday:      [][2]tf.TradingDay{{20260101, 20260104}, {20260214, 20260223}, {20260404, 20260406}, {20260501, 20260505}, {20260619, 20260621}, {20260925, 20260927}, {20261001, 20261007}},
		dayOnly:      map[tf.TradingDay]bool{20260105: true, 20260224: true, 20260407: true, 20260506: true, 20260622: true, 20260928: true, 20261008: true},
		events:       []annualEvent{{20260101, 20260105, "boundary-2026"}, {20260213, 20260224, "spring-2026"}, {20260403, 20260407, "qingming-2026"}, {20260430, 20260506, "labor-2026"}, {20260618, 20260622, "dragon-2026"}, {20260924, 20260928, "autumn-national-2026"}, {20260930, 20261008, "autumn-national-2026"}},
		ordinalDates: []tf.TradingDay{20260130, 20260227, 20260331, 20260430, 20260529, 20260630, 20260731, 20260831, 20260930, 20261030, 20261104},
		products:     map[string]annualProduct{},
	}
	versions := []annualVersion{
		{20260101, 20260705, []string{"ine-next-rule-doc-text", "ine-next-rules-publication", "ine-next-rule-doc", "ine-sept-rule-page"}},
		{20260706, 20260913, []string{"ine-july-text", "ine-july-publication", "ine-july-docx", "ine-sept-rule-page"}},
		{20260914, 20261104, []string{"ine-sept-text", "ine-sept-publication", "ine-sept-docx", "ine-sept-rule-page"}},
	}
	s.products["lu"] = annualProduct{from: 20260101, to: 20261104, listed: 20200622, nightEnd: 1380, openDays: 201, versions: versions}
	s.products["ec"] = annualProduct{from: 20260101, to: 20261104, listed: 20230818, nightEnd: 0, openDays: 201, versions: versions}
	return s
}
