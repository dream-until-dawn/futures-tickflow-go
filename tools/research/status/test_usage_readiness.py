"""有价格不等于完整回测，有快照不等于每日盘前稳定交付。"""
import io
import sys
import unittest
from usage_readiness import assess


class ReadinessTest(unittest.TestCase):
    def test_sample_only_and_current_original_do_not_promote_scope(self):
        row=dict(kind='ohlc',present_rows=4,route='edb-directory',valid_rows=3,rows=4,last='2021-01-05',source='EDB')
        r=assess(dict(datasets=[row]));self.assertEqual(r['research_backtest'],'partial_price_research');self.assertIsNone(r['latest_official'])
        row=dict(row,route='official-day-20261116',last='2026-11-16',last_acquired_at='2026-11-16T11:00:00Z')
        r=assess(dict(datasets=[row]));self.assertEqual(r['latest_official']['valid_rows'],3)
        self.assertFalse(r['stable_preopen_delivery_certified']);self.assertFalse(r['financial_admission_added'])
        self.assertEqual(r['full_backtest'],'not_certified_by_this_inventory')
        row['valid_rows']=0
        self.assertEqual(assess(dict(datasets=[row]))['research_backtest'],'no_verified_price_rows')
        row['valid_rows']=3;row['last_acquired_at']=None
        self.assertIsNone(assess(dict(datasets=[row]))['latest_official'])


if __name__=='__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
