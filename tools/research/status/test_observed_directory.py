"""报价观察目录不得伪装生命周期，也不得用归档时间冒充原始取得。"""
import io
import sys
import unittest
from observed_directory import ObservedDirectory


class ObservedDirectoryTest(unittest.TestCase):
    def test_contract_observations_and_unknown_acquisition(self):
        d=ObservedDirectory('official-catalog','2026-11-16T00:00:00Z')
        def row(day,native):
            return dict(exchange='SHFE',native=native,day=day,evidence=dict(sha256='a'*64,retrieved_at='2026-11-15T00:00:00Z',version='first original HTTP UTC unresolved'))
        d.add(row(20210104,'rb2105'));d.add(row(20210105,'rb2105'));d.add(row(20210105,'rb2110'))
        r=d.finish()[0]
        self.assertEqual((r['contracts'],r['rows'],r['observed_days']),(2,3,2))
        self.assertIsNone(r['last_acquired_at']);self.assertIsNotNone(r['last_receipt_at'])
        self.assertIsNone(r['valid_rows']);self.assertIsNone(r['expected_contract_days'])
        self.assertNotIn('listing_day',r['contract_observations'][0])
        self.assertEqual(r['contract_observations'][0]['last'],'2021-01-05')
        with self.assertRaisesRegex(ValueError,'inventory_product_identity'):d.add(row(20210104,'rb'))

    def test_czce_native_year_requires_explicit_binding(self):
        row=dict(exchange='CZCE',native='AP101',day=20210104,evidence={})
        d=ObservedDirectory('a','2026-11-16T00:00:00Z');d.add(row)
        self.assertIsNone(d.finish()[0]['contract_observations'][0]['canonical'])
        d=ObservedDirectory('a','2026-11-16T00:00:00Z',{('CZCE','AP101'):{'CZCE.AP2101'}});d.add(row)
        self.assertEqual(d.finish()[0]['contract_observations'][0]['canonical'],'CZCE.AP2101')


if __name__=='__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
