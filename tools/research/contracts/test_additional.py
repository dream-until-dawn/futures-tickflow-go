"""三位代码映射必须唯一，业务日期不靠获取时间补造。"""
import io
import sys
import unittest
import zipfile
import csv

from additional import FIELD_COLUMNS,decode_czce,decode_cffex


class AdditionalMarketTest(unittest.TestCase):
    def fixture(self):
        header=['品种月份']+list(FIELD_COLUMNS.values())
        row=['AP101']+['1']*len(FIELD_COLUMNS)
        return ('郑州商品交易所期货每日行情表(2021-01-04)\n'+'|'.join(header)+'\n'+'|'.join(row)+'\n').encode()

    def test_unique_mapping_preserves_original_and_exact_day(self):
        rows,unknown=decode_czce(self.fixture(),{'AP101':[('CZCE.AP2101','AP')]})
        self.assertEqual(unknown,[])
        self.assertEqual(rows[0]['source_native'],'AP101')
        self.assertEqual(rows[0]['native'],'AP2101')
        self.assertEqual(rows[0]['day'],20210104)

    def test_ambiguous_and_missing_mapping_remain_unresolved(self):
        for known in [{},{'AP101':[('CZCE.AP2101','AP'),('CZCE.AP3101','AP')]}]:
            rows,unknown=decode_czce(self.fixture(),known)
            self.assertEqual(rows,[])
            self.assertEqual(len(unknown),1)
        with self.assertRaisesRegex(ValueError,'^czce_date_heading$'):
            decode_czce(self.fixture().replace(b'2021-01-04',b'2021-01-05'),{})

    def test_cffex_mixed_options_are_counted_not_futures(self):
        columns={**FIELD_COLUMNS,'pre_settle':'前结算','volume':'成交量','turnover':'成交金额'}
        s=io.StringIO();writer=csv.writer(s);writer.writerow(['合约代码']+list(columns.values()))
        for code in ['IF2101','IO2101-C-4200']:writer.writerow([code]+['1']*len(columns))
        b=io.BytesIO()
        with zipfile.ZipFile(b,'w') as z:z.writestr('20210104_1.csv',s.getvalue().encode('gb18030'))
        stats={};records,unknown=decode_cffex(b.getvalue(),{'IF2101':[('CFFEX.IF2101','IF')]},stats)
        self.assertEqual(len(records),1)
        self.assertEqual(unknown,[])
        self.assertEqual(stats['excluded_options'],1)

    def test_cffex_explicit_month_rejects_other_month_and_invalid_day(self):
        columns={**FIELD_COLUMNS,'pre_settle':'前结算','volume':'成交量','turnover':'成交金额'}
        s=io.StringIO();writer=csv.writer(s);writer.writerow(['合约代码']+list(columns.values()))
        writer.writerow(['IF2102']+['1']*len(columns))
        def package(name):
            b=io.BytesIO()
            with zipfile.ZipFile(b,'w') as z:z.writestr(name,s.getvalue().encode('gb18030'))
            return b.getvalue()
        known={'IF2102':[('CFFEX.IF2102','IF')]}
        rows,unknown=decode_cffex(package('20210201_1.csv'),known,month='202102')
        self.assertEqual(rows[0]['day'],20210201)
        self.assertEqual(unknown,[])
        with self.assertRaisesRegex(ValueError,'^cffex_unknown_member$'):
            decode_cffex(package('20210104_1.csv'),known,month='202102')
        with self.assertRaises(ValueError):
            decode_cffex(package('20210230_1.csv'),known,month='202102')


if __name__=='__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
