package proof

import tf "github.com/dream-until-dawn/futures-tickflow-go"

// specINE2023 单独审核LU全年；EC上市首日例外不适用于已上市的LU。
func specINE2023() annualSpec {
	s := annualSpec{exchange: tf.INE, yearRole: "ine-year-2023", year: 2023, factsSHA: "e5f2d4db82213bef71eeca13ead2b285897aa0c1ee52811eba1598ee704464c5", openDays: 242,
		holiday:      [][2]tf.TradingDay{{20230101, 20230102}, {20230121, 20230127}, {20230405, 20230405}, {20230429, 20230503}, {20230622, 20230624}, {20230929, 20231006}},
		dayOnly:      map[tf.TradingDay]bool{20230103: true, 20230130: true, 20230406: true, 20230504: true, 20230626: true, 20231009: true},
		events:       []annualEvent{{20230101, 20230103, "ine-year-2023"}, {20230120, 20230130, "ine-year-2023"}, {20230404, 20230406, "ine-year-2023"}, {20230428, 20230504, "ine-year-2023"}, {20230621, 20230626, "ine-year-2023"}, {20230928, 20231009, "ine-year-2023"}, {20231229, 20231231, "boundary-2024"}},
		ordinalDates: []tf.TradingDay{20230103, 20230120, 20230228, 20230331, 20230428, 20230531, 20230630, 20230731, 20230831, 20230928, 20231031, 20231130, 20231229}, products: map[string]annualProduct{}}
	s.products["lu"] = annualProduct{from: 20230101, to: 20231231, listed: 20200622, nightEnd: 1380, openDays: 242, versions: []annualVersion{
		{20211126, 20230525, []string{"ine-trade-202111-text", "ine-rules-202111", "ine-trade-202111-doc", "ine-next-publication", "ine-next-docx", "ine-next-text"}},
		{20230526, 20230817, []string{"ine-next-text", "ine-next-publication", "ine-next-docx"}},
		{20230818, 20231120, []string{"ine-trade-202308-doc-text", "ine-rules-202308-repub", "ine-trade-202308-doc"}},
		{20231121, 20231231, []string{"ine-trade-202311-docx-text", "ine-rules-202311-repub", "ine-trade-202311-docx", "ine-next-rules-publication", "ine-next-rule-doc", "ine-next-rule-doc-text"}},
	}}
	return s
}
