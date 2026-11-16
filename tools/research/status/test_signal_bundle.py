"""信号只要求有效结算；数值规范化不补造OHLC，不接受非有限数或伪结算。"""
import io
import sys
import unittest
from signal_bundle import fields


class SignalBundleTest(unittest.TestCase):
    def test_settlement_without_ohlc_is_supported_and_zero_stays_zero(self):
        row=fields(dict(settle='2,400.00',volume='0',open='0'))
        self.assertEqual(row['settle'],'2400.00')
        self.assertEqual(row['open'],'0')
        self.assertIsNone(row['high'])
        self.assertEqual(row['volume'],'0')
        self.assertIsNone(row['turnover'])

    def test_no_close_as_settlement_or_nonfinite(self):
        for value in (None,'0','-1'):
            with self.assertRaisesRegex(ValueError,'signal_invalid_settlement'):
                fields(dict(close='100',settle=value))
        for value in ('NaN','Infinity','-Infinity'):
            with self.assertRaisesRegex(ValueError,'signal_nonfinite_numeric'):
                fields(dict(settle='100',open=value))
        with self.assertRaisesRegex(ValueError,'signal_invalid_numeric'):
            fields(dict(settle='wrong'))


if __name__ == "__main__":
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
