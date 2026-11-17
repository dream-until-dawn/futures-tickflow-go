"""明确开仓字段的正反控制，旧日、普通1手和缺失字段都不能混入当前通过范围。"""
import io
import sys
import copy
import gzip
import hashlib
import json
import unittest
from direct_minimum import extract


class DirectMinimumTest(unittest.TestCase):
    def fixture(self, row=None):
        row = row or dict(**{'class':'FUTURE'}, expired=False, exchange_id='GFEX',product_id='lc',
                          delivery_year=2027,delivery_month=1,instrument_id='GFEX.lc2701',
                          open_min_limit_order_volume=2,open_min_market_order_volume=2,min_limit_order_volume=1)
        raw = gzip.compress(json.dumps({'GFEX.lc2701':row}).encode())
        receipt = dict(status=200,body_complete=True,state='received_raw_unqualified',content_encoding='gzip',
                       sha256=hashlib.sha256(raw).hexdigest(),received_at='2026-11-17T05:00:00+00:00')
        return raw,receipt

    def test_actual_two_is_not_ordinary_one(self):
        raw,r = self.fixture()
        got=extract(raw,r,20261117,{'GFEX.lc2701'})['GFEX.lc2701']
        self.assertEqual(got['limit_min_open'],2)
        self.assertEqual(got['ordinary_limit_min'],1)
        self.assertEqual(extract(raw,r,20261117,{'SHFE.rb2701'}),{})

    def test_missing_bool_zero_expired_and_wrong_month_rejected(self):
        raw,r=self.fixture();base=json.loads(gzip.decompress(raw))['GFEX.lc2701']
        for changes in [{'open_min_limit_order_volume':None},{'open_min_limit_order_volume':True},
                        {'open_min_limit_order_volume':0},{'expired':True},{'delivery_month':2}]:
            row=copy.deepcopy(base);row.update(changes);raw,r=self.fixture(row)
            self.assertEqual(extract(raw,r,20261117,{'GFEX.lc2701'}),{})
        del base['open_min_limit_order_volume'];raw,r=self.fixture(base)
        self.assertEqual(extract(raw,r,20261117,{'GFEX.lc2701'}),{})

    def test_old_day_and_mutation_reject_for_specific_reason(self):
        raw,r=self.fixture()
        with self.assertRaisesRegex(ValueError,'stale_snapshot'):extract(raw,r,20261118,{'GFEX.lc2701'})
        with self.assertRaisesRegex(ValueError,'transport_or_identity'):extract(raw+b'x',r,20261117,{'GFEX.lc2701'})
        r['body_complete']=False
        with self.assertRaisesRegex(ValueError,'transport_or_identity'):extract(raw,r,20261117,{'GFEX.lc2701'})


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
