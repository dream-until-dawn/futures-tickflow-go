"""历史OHLC独立校验：时间日标签、价格顺序、零报价及量仓非整数。"""
import io
import sys
import unittest
from rb_backtest_bundle import normalize


class RBHistoryTest(unittest.TestCase):
    def row(self, **changes):
        row=dict(datetime_nano='1609689600000000000',open='100',high='102',low='99',close='101',volume='0',close_oi='5')
        return dict(row,**changes)

    def test_daily_label_and_unknown_settlement_are_preserved(self):
        day,values=normalize(self.row())
        self.assertEqual(day,20210104)
        self.assertEqual(values['volume'],'0')
        self.assertIsNone(values['settle'])
        self.assertIsNone(values['turnover'])

    def test_zero_or_invalid_ohlc_is_not_counted_as_price_coverage(self):
        self.assertIsNone(normalize(self.row(open='0',high='0',low='0',close='0')))
        self.assertIsNone(normalize(self.row(high='98')))
        with self.assertRaisesRegex(ValueError,'rb_history_nonfinite'):
            normalize(self.row(open='NaN'))
        for key,value in [('volume','-1'),('close_oi','1.5')]:
            with self.assertRaisesRegex(ValueError,'rb_history_invalid_quantity'):
                normalize(self.row(**{key:value}))


if __name__=='__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
