"""到期筛选与官方缺席必须保留，不从首末行情推断上市日。"""
import io
import sys
import unittest

from review import summarize


class HistoricalDirectoryReviewTest(unittest.TestCase):
    def fixture(self):
        return dict(by_class={'FUTURE': 2, 'OPTION': 3}, by_market={'SHFE': 2}, contracts=[
            dict(canonical='SHFE.rb2012', exchange='SHFE', delivery_year=2020, expiry_seconds='1608000000'),
            dict(canonical='SHFE.rb2105', exchange='SHFE', delivery_year=2021, expiry_seconds='1620000000')])

    def test_expiry_filter_and_unresolved_missing_identity(self):
        result = summarize(self.fixture(), [dict(records=[dict(exchange='SHFE', native='rb2105'), dict(exchange='SHFE', native='rb2405')])])
        self.assertEqual(result['total_entries'], 5)
        self.assertEqual(result['six_market_futures'], 2)
        self.assertEqual(result['expiry_not_before_2021'], 1)
        self.assertEqual(result['official_absent_from_snapshot'], ['SHFE.rb2405'])
        self.assertFalse(result['complete_historical_listing_certified'])

    def test_duplicate_not_hidden_by_set(self):
        data = self.fixture()
        data['contracts'].append(data['contracts'][0])
        with self.assertRaisesRegex(ValueError, '^duplicate_canonical$'):
            summarize(data, [])


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
