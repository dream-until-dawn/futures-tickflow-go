"""公司标准与交易所栏、投机与套保栏及跨年月份专项不能混用。"""
import io
import sys
import unittest
import gzip
import hashlib
import json
from broker_margin import expand,ratio,bind,special_symbols,build_current_facts


class BrokerMarginTest(unittest.TestCase):
    def test_company_columns_and_specials_do_not_overwrite_existing_minimum(self):
        header=['交易所','品种','品种代码']+['交易所保证金（%）']*2+['公司保证金（%）']*2+['涨跌幅','特殊合约']+['交易所保证金（%）']*2+['公司保证金（%）']*2+['涨跌幅']
        rows=[header,['投机']*14,['大商所','玉米','c','7','7','12','11','6','c2701','9','9','20','19','8']]
        raw=('<html>20261117期货保证金率及涨跌停板<table>'+''.join('<tr>'+''.join('<td>'+v+'</td>' for v in r)+'</tr>' for r in rows)+'</table></html>').encode()
        receipt=dict(url='https://www.minfutures.com/main/a/20261116/86319.shtml',http=dict(status=200,complete=True),sha256=hashlib.sha256(raw).hexdigest(),finished_at='2026-11-17T06:00:00Z')
        old=dict(**{'class':'FUTURE'},expired=False,expire_datetime=1900000000,exchange_id='DCE',product_id='c',delivery_year=2027,delivery_month=1,instrument_id='DCE.c2701')
        scope=gzip.compress(json.dumps({'DCE.c2701':old}).encode())
        existing=dict(product_key='SHFE.rb',symbol='SHFE.rb2701',source_native='rb2701',effective_min_open_lots=4)
        base=dict(current_trade_day=20261117,current_contracts=[existing],settlement_scopes={},evidence=[],source_limits=[])
        result,_=build_current_facts(base,raw,receipt,scope,{'SHFE.rb','DCE.c'})
        self.assertEqual(result['current_contracts'][0],existing)
        new=result['current_contracts'][1]
        self.assertEqual(new['margin']['long'],'0.2')
        self.assertEqual(new['margin']['basis'],'company')
        self.assertIsNone(new['effective_min_open_lots'])
        self.assertFalse(new['special_adjustments_reviewed'])
        with self.assertRaisesRegex(ValueError,'source_identity'):build_current_facts(base,raw+b'x',receipt,scope,{'DCE.c'})
    def test_rowspan_colspan_and_percent_units(self):
        rows=[[({'rowspan':'2'},'A'),({'colspan':'2'},'B')],[({},'C'),({},'D')]]
        self.assertEqual(expand(rows),[['A','B','B'],['A','C','D']])
        self.assertEqual(ratio('19'),'0.19')
        for value in ['0','-1','101','NaN','']: 
            with self.assertRaisesRegex(ValueError,'broker_margin_ratio'):ratio(value)

    def test_cross_year_special_overrides_base_only_in_scope(self):
        policy=dict(product='SHFE.cu',company_spec_ratio='0.17',special=[dict(symbols='cu2612-2702',company_spec_ratio='0.19')])
        result=bind(policy,['SHFE.cu2611','SHFE.cu2612','SHFE.cu2701','SHFE.cu2703'],{})
        self.assertEqual(result,{'SHFE.cu2611':'0.17','SHFE.cu2612':'0.19','SHFE.cu2701':'0.19','SHFE.cu2703':'0.17'})
        with self.assertRaisesRegex(ValueError,'special_month'):special_symbols('cu2613','SHFE.cu',{})
        with self.assertRaisesRegex(ValueError,'special_syntax'):special_symbols('其他合约','SHFE.cu',{})

    def test_three_digit_needs_explicit_year_and_conflicts_reject(self):
        with self.assertRaisesRegex(ValueError,'year_binding'):special_symbols('TA701','CZCE.TA',{})
        self.assertEqual(special_symbols('TA701','CZCE.TA',{'CZCE.TA701':'CZCE.TA2701'}),{'CZCE.TA2701'})
        policy=dict(product='SHFE.cu',company_spec_ratio='0.17',special=[dict(symbols='cu2701',company_spec_ratio='0.19'),dict(symbols='cu2701',company_spec_ratio='0.20')])
        with self.assertRaisesRegex(ValueError,'conflicting_override'):bind(policy,['SHFE.cu2701'],{})


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
