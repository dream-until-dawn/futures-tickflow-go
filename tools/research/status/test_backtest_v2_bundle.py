"""v2参数原件、缺年和迁移路径的定向反例；不靠普通一手通过。"""
import copy
import hashlib
import io
import json
from pathlib import Path
import sys
import tempfile
import unittest

from backtest_v2_bundle import checked_parameters, coverage, verified_file, valid_official_ohlc


class BacktestV2BundleTest(unittest.TestCase):
    def test_official_empty_quote_stays_empty_and_cannot_be_a_backtest_price(self):
        fields = dict(open='10', high='11', low='9', close='10', volume=None)
        self.assertTrue(valid_official_ohlc(fields))
        for value in (None, '0', 'NaN'):
            self.assertFalse(valid_official_ohlc(dict(fields, open=value)))
        self.assertIsNone(fields['volume'])
    def test_unknown_minimum_is_allowed_but_wrong_original_ratio_or_day_is_not(self):
        row = dict(symbol='SHFE.hc2701', source_native='hc2701', as_of_trade_day=20261117,
                   effective_min_open_lots=None, margin=dict(long='0.07', short='0.08', unit='ratio',
                       basis='exchange_spec', evidence_ids=['source']))
        source = dict(INSTRUMENTID='hc2701', TRADINGDAY=20261117,
                      SPEC_LONGMARGINRATIO='0.07', SPEC_SHORTMARGINRATIO='0.08')
        raw = json.dumps(dict(ContractDailyTradeArgument=[source])).encode()
        checked_parameters([row], raw, 20261117)
        for field, value, reason in [('TRADINGDAY', 20261116, 'day_or_scope'),
                                     ('SPEC_SHORTMARGINRATIO', '0.07', 'original_value')]:
            changed = dict(source, **{field: value})
            with self.assertRaisesRegex(ValueError, reason):
                checked_parameters([row], json.dumps(dict(ContractDailyTradeArgument=[changed])).encode(), 20261117)
        changed = copy.deepcopy(row)
        changed['margin']['unit'] = 'percent'
        with self.assertRaisesRegex(ValueError, 'day_or_scope'):
            checked_parameters([changed], raw, 20261117)

    def test_missing_year_tampered_bytes_and_outside_source_fail(self):
        rows = [dict(day=year * 10000 + 1101) for year in range(2021, 2026)] + [dict(day=20261116)]
        self.assertEqual(coverage(rows, 20261116)['valid_days_by_year']['2021'], 1)
        with self.assertRaisesRegex(ValueError, 'history_baseline'):
            coverage([r for r in rows if r['day'] // 10000 != 2024], 20261116)
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'raw').write_bytes(b'actual')
            entry = dict(path='raw', bytes=6, sha256=hashlib.sha256(b'actual').hexdigest())
            self.assertEqual(verified_file(root, entry), b'actual')
            (root / 'raw').write_bytes(b'changed')
            with self.assertRaisesRegex(ValueError, 'source_sha'):
                verified_file(root, entry)
            with self.assertRaisesRegex(ValueError, 'source_path'):
                verified_file(root, dict(entry, path='../outside'))


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
