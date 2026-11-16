"""百分数单位、日期、空值和原生合约身份必须分别拒错。"""
import io
import sys
import unittest
from clear_parameters import parse


class ClearParametersTest(unittest.TestCase):
    def body(self, row='RM701|2,400.00|10.00', header='合约代码|当日结算价|交易保证金率(%)'):
        return ('郑州商品交易所期货结算参数表(2026-11-16)\n'+header+'\n'+row+'\n').encode()

    def test_percentage_is_ratio_but_no_effective_day_or_minimum_is_invented(self):
        row = parse(self.body(), 20261116)[0]
        self.assertEqual(row['margin_ratio'], '0.10')
        self.assertEqual(row['settlement'], '2400.00')
        self.assertEqual(row['source_native'], 'RM701')
        self.assertIsNone(row['as_of_trade_day'])
        self.assertIsNone(row['effective_min_open_lots'])

    def test_wrong_day_html_and_empty(self):
        with self.assertRaisesRegex(ValueError, '^clear_title_or_day$'):
            parse(self.body(), 20261117)
        with self.assertRaisesRegex(ValueError, '^clear_title_or_day$'):
            parse(b'<html>200</html>', 20261116)
        with self.assertRaisesRegex(ValueError, '^clear_empty$'):
            parse(self.body(row=''), 20261116)

    def test_duplicate_contract_columns_and_nonfinite(self):
        for body, reason in ((self.body(row='RM701|2400|10\nRM701|2400|10'), 'identity'),
                             (self.body(header='合约代码|合约代码|交易保证金率(%)'), 'headers'),
                             (self.body(row='RM2701|2400|10'), 'identity'),
                             (self.body(row='RM701|2400'), 'columns'),
                             (self.body(row='RM701|NaN|10'), 'invalid_numeric'),
                             (self.body(row='RM701|2400|1000'), 'invalid_numeric')):
            with self.assertRaisesRegex(ValueError, 'clear_.*'+reason):
                parse(body, 20261116)


if __name__ == "__main__":
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
