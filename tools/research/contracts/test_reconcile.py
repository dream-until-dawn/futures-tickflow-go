"""正控、量值差异、真实缺日与未报价字段分别验证。"""
import io
import sys
import unittest

from reconcile import reconcile


class DirectoryDailyReconcileTest(unittest.TestCase):
    def fixture(self):
        raw = b'datetime_nano,open,high,low,close,volume,open_oi,close_oi\n1609689600000000000,1,3,1,2,4,5,6\n'
        official = {20210104: dict(open='1.0', high='3', low='1', close='2', volume='4', open_interest='6')}
        return raw, official

    def test_positive_numeric_and_scope(self):
        raw, official = self.fixture()
        result = reconcile(raw, official, {20210104: True}, {2021})
        self.assertEqual(result['matched_values'], 6)
        self.assertEqual(result['differences'], [])
        self.assertEqual(result['missing_official_days'], [])
        self.assertFalse(result['official_lifecycle_certified'])

    def test_changed_volume_is_reported(self):
        raw, official = self.fixture()
        result = reconcile(raw.replace(b',4,5,6',b',7,5,6'), official, {}, {2021})
        self.assertEqual(result['matched_values'],5)
        self.assertEqual(result['differences'],[dict(day=20210104,field='volume',edb='7',official='4',binary64_equal=False)])
        self.assertEqual(result['binary64_distinct_differences'],1)

    def test_binary64_representation_difference_still_preserves_raw_decimal(self):
        raw,official=self.fixture()
        official[20210104]['close']='2.0000000000000001'
        result=reconcile(raw,official,{}, {2021})
        self.assertEqual(result['matched_values'],5)
        self.assertEqual(result['binary64_equivalent_differences'],1)
        self.assertEqual(result['binary64_distinct_differences'],0)
        self.assertEqual(result['differences'][0]['official'],'2.0000000000000001')

    def test_missing_official_and_unknown_calendar_are_not_success(self):
        raw, official = self.fixture()
        official[20210105] = official[20210104]
        result = reconcile(raw,official,{}, {2021})
        self.assertEqual(result['missing_official_days'],[20210105])
        self.assertEqual(result['calendar_unknown_actual_days'],[20210104])
        official[20210104]['open'] = '-'
        result = reconcile(raw,official,{}, {2021})
        self.assertEqual(result['official_unpriced_fields'],[dict(day=20210104,field='open',official='-')])

    def test_duplicate_and_shifted_anchor_are_rejected(self):
        raw,official=self.fixture()
        with self.assertRaisesRegex(ValueError,'^daily_order_or_window$'):
            reconcile(raw+raw.splitlines(keepends=True)[1],official,{}, {2021})
        with self.assertRaisesRegex(ValueError,'^daily_anchor$'):
            reconcile(raw.replace(b'1609689600000000000',b'1609693200000000000'),official,{}, {2021})

    def test_partial_year_is_not_full_year_and_commas_are_numeric(self):
        raw,official=self.fixture();official[20210104]['volume']='1,234'
        result=reconcile(raw.replace(b',4,5,6',b',1234,5,6'),official,{},set())
        self.assertEqual(result['matched_values'],6)
        self.assertEqual(result['outside_official_years'],[])
        self.assertEqual(result['extra_in_official_years'],[])


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
