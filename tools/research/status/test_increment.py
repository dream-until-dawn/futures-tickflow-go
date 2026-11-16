"""原始表与派生记录的集合、日期、字面量必须分别通过。"""
import copy
import io
import json
import sys
import unittest
from review_increment import inspect,compare


class IncrementTest(unittest.TestCase):
    def test_valid_zero_volume_missing_price_and_literal_mutation(self):
        row=dict(PRODUCTID='rb_f',DELIVERYMONTH='2701',OPENPRICE='',HIGHESTPRICE='',LOWESTPRICE='',CLOSEPRICE='1.00',SETTLEMENTPRICE=2,PRESETTLEMENTPRICE=2,VOLUME=0,OPENINTEREST=3,TURNOVER=0)
        raw=json.dumps(dict(report_date='20261116',o_code='0000',o_curinstrument=[row])).encode()
        fields=inspect(raw,'shfe-json',20261116)[('SHFE','rb2701',20261116)]
        decoded=[dict(exchange='SHFE',native='rb2701',day=20261116,fields=fields)]
        self.assertEqual(compare(raw,'shfe-json',20261116,decoded)['fields'],9)
        self.assertEqual(fields['open'],'');self.assertEqual(fields['volume'],'0')
        bad=copy.deepcopy(decoded);bad[0]['fields']['close']='1.01'
        with self.assertRaisesRegex(ValueError,'^increment_fields_or_set$'):compare(raw,'shfe-json',20261116,bad)
        with self.assertRaisesRegex(ValueError,'^increment_fields_or_set$'):compare(raw,'shfe-json',20261116,decoded+decoded)
        with self.assertRaisesRegex(ValueError,'^increment_day_or_business$'):inspect(raw,'shfe-json',20261117)


if __name__=='__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
