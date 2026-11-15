"""业务反控直接改数值/时间，不依靠错误SHA遮蔽真正守卫。"""
import csv
import io
import sys
import unittest

import review


class EDBReviewTest(unittest.TestCase):
    def fixture(self):
        stamp = 1763427600000000000
        data = [[str(stamp), '1', '2', '1', '2', '2', '5', '6'],
                [str(stamp + 60000000000), '2', '3', '2', '2', '3', '6', '7']]
        sessions = [dict(Start=stamp // 1000000, End=stamp // 1000000 + 120000)]
        official = dict(open='1', high='3', low='1', close='2', volume='5', open_interest='7')
        return data, sessions, official

    def raw(self, data):
        out = io.StringIO(); writer = csv.writer(out); writer.writerow(review.HEADER); writer.writerows(data)
        return out.getvalue().encode()

    def test_positive_exact_grid_and_aggregation(self):
        data, sessions, official = self.fixture()
        result = review.review_minutes(self.raw(data), sessions, official)
        self.assertEqual(result['rows'], 2)
        self.assertEqual(result['aggregates']['volume'], '5')
        self.assertFalse(result['settlement_compared'])

    def test_shifted_time_is_rejected_by_grid(self):
        data, sessions, official = self.fixture()
        for row in data:
            row[0] = str(int(row[0]) + 3600000000000)
        with self.assertRaisesRegex(ValueError, '^minute_grid$'):
            review.review_minutes(self.raw(data), sessions, official)

    def test_missing_duplicate_and_field_changes(self):
        data, sessions, official = self.fixture()
        for changed in (data[1:], data + [data[-1]]):
            with self.assertRaisesRegex(ValueError, '^minute_grid$'):
                review.review_minutes(self.raw(changed), sessions, official)
        for column, value, field in [(5, '4', 'volume'), (4, '2.1', 'close'), (7, '8', 'open_interest')]:
            changed = [r[:] for r in data]; changed[-1][column] = value
            with self.assertRaisesRegex(ValueError, '^official_mismatch:' + field + '$'):
                review.review_minutes(self.raw(changed), sessions, official)

    def test_daily_midnight_is_observation_not_universal_boundary(self):
        stamp = 1763395200000000000
        row = [str(stamp), '1', '3', '1', '2', '5', '6', '7']
        expected = {20251118: dict(open='1', high='3', low='1', close='2', volume='5', open_interest='7')}
        result = review.review_daily(self.raw([row]), expected)
        self.assertFalse(result['query_boundary_inclusivity_certified'])
        row[0] = str(stamp + 3600000000000)
        with self.assertRaisesRegex(ValueError, '^daily_observed_anchor$'):
            review.review_daily(self.raw([row]), expected)


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
