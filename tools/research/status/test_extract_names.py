"""名称流解析须保留数字名称，并拒绝截断/重复键和错误身份。"""
import io
import sys
import unittest
from extract_names import objects, names


class DisplayNamesTest(unittest.TestCase):
    def test_stream_and_suffix_preserve_index_digits(self):
        raw='{"CFFEX.IF2101":{"class":"FUTURE","exchange_id":"CFFEX","product_id":"IF","ins_name":"沪深300股指2101"}}'
        d=names(objects(io.StringIO(raw)))
        self.assertEqual(d['CFFEX.IF']['name'],'沪深300股指')

    def test_truncated_duplicate_and_trailing_reject(self):
        for text,reason in [('{"a":{','name_json_truncated'),('{"a":{},"a":{}}','name_duplicate_key'),('{}x','name_json_trailing')]:
            with self.subTest(text=text), self.assertRaisesRegex(ValueError,'^'+reason+'$'):
                list(objects(io.StringIO(text)))

    def test_czce_short_key_does_not_leave_a_year_digit_in_name(self):
        row={'class':'FUTURE','exchange_id':'CZCE','product_id':'AP','ins_name':'苹果2101'}
        self.assertEqual(names([('CZCE.AP101',row)],{'CZCE.AP101':'CZCE.AP2101'})['CZCE.AP']['name'],'苹果')

    def test_future_identity_and_empty_names_are_not_guessed(self):
        row=dict(class_='FUTURE',exchange_id='DCE',product_id='m',ins_name='')
        row['class']=row.pop('class_')
        self.assertEqual(names([('DCE.m2105',row)]),{})
        with self.assertRaisesRegex(ValueError,'^name_future_identity$'):names([('DCE.c2105',row)])
        self.assertEqual(names([('DCE.c2105',row)],{'DCE.m2105'}),{})


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
