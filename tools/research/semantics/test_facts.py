"""正式合约、首次上市与后生效交易细则不能混为全年资格。"""
import io
import sys
import unittest
from facts import facts


class OfficialFactBoundaryTest(unittest.TestCase):
    def fixture(self):
        contract=['〔2021〕8号 2021年1月25日 自2021年2月1日起施行','5吨/手 2元/吨 1、3、4、10、11、12月 第10个交易日']
        rules=['2021年1月28日 2021年2月22日 自2021年2月22日起施行 9:00-11:30 10:15-10:30为休息时间 13:30-15:00 单边数量 每个交易日结束后发布']
        journal=['']*42+['〔2021〕9号 2021年2月1日 PK2110 PK2111 PK2112 PK2201 暂不开展夜盘交易']
        return contract,rules,journal

    def test_partial_rule_and_initial_night_are_not_annual_calendar(self):
        r=facts(*self.fixture())
        self.assertEqual(r['rule_effective_day'],20210222)
        self.assertEqual(r['missing_pk_initial_rule_interval'],[20210201,20210221])
        self.assertEqual(r['derived_open_day_segments'][1],['10:30','11:30'])
        for k in ['general_rule_applies_to_sample_20210104','full_year_no_night_proven','verified_calendar_generated','financial_eligible']:
            self.assertFalse(r[k])

    def test_wrong_effective_date_or_missing_break_rejected(self):
        for old,new in [('2021年2月22日起施行','2021年1月4日起施行'),('10:15-10:30为休息时间','')]:
            contract,rules,journal=self.fixture();rules=[rules[0].replace(old,new)]
            with self.assertRaisesRegex(ValueError,'^rule_marker:'):facts(contract,rules,journal)

    def test_first_contract_identity_and_multiplier_mutations_rejected(self):
        contract,rules,journal=self.fixture();journal[42]=journal[42].replace('PK2110','PK2113')
        with self.assertRaisesRegex(ValueError,'^pk_listing_marker:'):facts(contract,rules,journal)
        contract,rules,journal=self.fixture();contract[1]=contract[1].replace('5吨/手','10吨/手')
        with self.assertRaisesRegex(ValueError,'^pk_contract_marker:'):facts(contract,rules,journal)

    def test_conflicting_title_and_footer_effective_dates_rejected(self):
        contract,rules,journal=self.fixture()
        rules.append('本细则自2021年1月4日起施行')
        with self.assertRaisesRegex(ValueError,'^rule_effective_conflict$'):facts(contract,rules,journal)


if __name__=='__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
