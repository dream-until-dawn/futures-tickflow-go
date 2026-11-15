"""字段差异须如实报告，结构错和重复须拒绝。"""
import unittest

from daily_research import compare_vendors


class DailyResearchTest(unittest.TestCase):
    def fixture(self):
        edb = b'datetime_nano,open,high,low,close,volume,open_oi,close_oi\n1609689600000000000,1,3,1,2,4,5,6\n'
        sina = b'''/*<script>location.href='//sina.com';</script>*/
var _V21052021_4_12=([{"d":"2021-01-04","o":"1.0","h":"3","l":"1","c":"2","v":"4","p":"6","s":"0"}]);'''
        return edb, sina

    def test_positive_numeric_not_lexical_equality(self):
        result = compare_vendors(*self.fixture())
        self.assertEqual(result['matched_values'], 6)
        self.assertEqual(result['differences'], [])
        self.assertFalse(result['official_reconciled'])

    def test_difference_is_not_hidden_or_official_failure(self):
        edb, sina = self.fixture()
        result = compare_vendors(edb.replace(b',4,5,6', b',7,5,6'), sina)
        self.assertEqual(result['matched_values'], 5)
        self.assertEqual(result['differences'][0]['field'], 'volume')

    def test_duplicate_and_anchor_rejected(self):
        edb, sina = self.fixture()
        with self.assertRaisesRegex(ValueError, '^edb_day_order$'):
            compare_vendors(edb + edb.splitlines(keepends=True)[1], sina)
        with self.assertRaisesRegex(ValueError, '^daily_observed_anchor$'):
            compare_vendors(edb.replace(b'1609689600000000000', b'1609693200000000000'), sina)

    def test_executable_suffix_rejected(self):
        edb, sina = self.fixture()
        with self.assertRaisesRegex(ValueError, '^sina_envelope$'):
            compare_vendors(edb, sina + b'fetch("elsewhere");')


if __name__ == '__main__':
    unittest.main()
