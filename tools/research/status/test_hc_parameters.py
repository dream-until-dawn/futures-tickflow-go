"""HC业务表日期、身份及比例反例；不凭RB计数放行。"""
import copy
import io
import sys
import unittest
from hc_parameters import select, extension_entries, EXTENSION_DAYS, KINDS
from native_handoff import encoded


class HCParametersTest(unittest.TestCase):
    def test_extension_requires_exact_23_tables_and_keeps_missing_settlement(self):
        entries=[dict(day=d,kind=k) for d in EXTENSION_DAYS for k in KINDS]
        entries += [dict(day='20261113',kind=k) for k in KINDS-{'Settlement'}]
        self.assertEqual(len(extension_entries(entries)),23)
        for changed in (entries[:-1], entries+[entries[0]], entries+[dict(day='20261113',kind='Settlement')]):
            with self.assertRaisesRegex(ValueError,'hc_parameters_extension_exact_scope'):
                extension_entries(changed)

    def value(self):
        return dict(report_date='2021-01-04', ContractDailyTradeArgument=[dict(
            INSTRUMENTID='hc2101', TRADINGDAY='20210104', SPEC_LONGMARGINRATIO='.15')])

    def test_native_rows_preserved_without_effective_time_or_minimum_invention(self):
        value = self.value()
        before = copy.deepcopy(value)
        rows = select(encoded(value), '20210104', 'ContractDailyTradeArgument')
        self.assertEqual(rows, value['ContractDailyTradeArgument'])
        self.assertNotIn('effective_at', rows[0])
        self.assertEqual(value, before)

    def test_wrong_table_day_row_day_and_duplicate_are_separate_failures(self):
        for edit, reason in [(lambda v:v.update(report_date='2021-01-05'), 'table_day'),
                             (lambda v:v['ContractDailyTradeArgument'][0].update(TRADINGDAY='20210105'), 'row_day'),
                             (lambda v:v['ContractDailyTradeArgument'].append(copy.deepcopy(v['ContractDailyTradeArgument'][0])), 'duplicate_contract')]:
            v=self.value();edit(v)
            with self.assertRaisesRegex(ValueError, 'hc_parameters_'+reason):
                select(encoded(v),'20210104','ContractDailyTradeArgument')

    def test_invalid_margin_or_only_rb_cannot_pass_hc(self):
        for literal in ('0','-.1','1.1','NaN'):
            v=self.value();v['ContractDailyTradeArgument'][0]['SPEC_LONGMARGINRATIO']=literal
            with self.subTest(literal=literal), self.assertRaisesRegex(ValueError,'hc_parameters_margin_ratio'):
                select(encoded(v),'20210104','ContractDailyTradeArgument')
        v=self.value();v['ContractDailyTradeArgument'][0]['INSTRUMENTID']='rb2101'
        with self.assertRaisesRegex(ValueError,'hc_parameters_empty_hc'):
            select(encoded(v),'20210104','ContractDailyTradeArgument')


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
