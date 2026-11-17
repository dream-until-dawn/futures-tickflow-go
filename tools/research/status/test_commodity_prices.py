"""真实交付的关键反例：不能以去重掩盖冲突，不能用跨度替代缺年。"""
import copy
import io
from pathlib import Path
import sys
import tempfile
import unittest
import hashlib
import json
import zipfile
import warnings
from unittest.mock import patch

from commodity_delivery import Payloads, policy, company_bundle
from commodity_prices import UniquePrices, canonical, coverage, normalize, official_fields
from official_prefix import czce_rows


class CommodityPricesTest(unittest.TestCase):
    def test_policy_keeps_actual_minimum_identity_without_readding_it_to_hard_items(self):
        value=policy(20261117,20261116)
        self.assertEqual(value['minimum_lots_kind'],'effective_min_open')
        self.assertEqual(value['backtest_hard_items'],['history_ohlc','current_margin'])
        self.assertEqual(value['daily_signal_hard_items'],['settlement','daily_update'])
    def record(self, day=20210104, close='2', source='a'):
        return dict(product_key='DCE.a', symbol='DCE.a2103', day=day, source_id=source,
                    fields=dict(open='1', high='2', low='1', close=close))

    def test_same_value_is_counted_but_conflict_isolated_without_picking_a_winner(self):
        rows=UniquePrices(); a=self.record(); rows.add(a)
        rows.add(self.record(source='b'))
        self.assertEqual(rows.duplicates,1);self.assertEqual(rows.product('DCE.a'),[a])
        rows.add(self.record(close='1',source='c'))
        self.assertEqual(rows.product('DCE.a'),[])
        rows.add(self.record(source='d'))
        self.assertEqual(len(rows.conflicts[('DCE.a2103',20210104)]),3)
        self.assertEqual(a['source_id'],'a')

    def test_missing_year_listing_day_and_future_are_targeted_rejections(self):
        rows=[self.record(y*10000+104) for y in range(2021,2027)]
        self.assertEqual(coverage(rows,None,20261116)['first'],20210104)
        with self.assertRaisesRegex(ValueError,'year_or_listing'):
            coverage([r for r in rows if r['day']//10000!=2024],None,20261116)
        listing=dict(day=20250722)
        rows=[self.record(20250722),self.record(20261113)]
        self.assertEqual(coverage(rows,listing,20261116)['listing_day'],20250722)
        for changed in [[self.record(20250723),self.record(20261113)],rows+[self.record(20261118)]]:
            with self.assertRaisesRegex(ValueError,'year_or_listing'):coverage(changed,listing,20261116)

    def test_raw_decimals_and_null_settlement_are_preserved(self):
        row=dict(datetime_nano='1609718400000000000',open='1.00',high='2.000',low='1.0',close='2.00',volume='0',close_oi='2')
        day,fields=normalize(row)
        self.assertEqual(day,20210104);self.assertEqual(fields['close'],'2.00')
        self.assertIsNone(fields['settle']);self.assertIsNone(fields['turnover'])
        self.assertIsNone(normalize(dict(row,low='0')))
        for change,reason in [(dict(volume='-1'),'quantity'),(dict(close='NaN'),'nonfinite'),
                              (dict(datetime_nano='1609718400000000001'),'timestamp_precision')]:
            with self.assertRaisesRegex(ValueError,reason):normalize(dict(row,**change))

    def test_explicit_year_and_safe_artifact_identity_are_required(self):
        self.assertEqual(canonical('CZCE.CF105',{'CZCE.CF105':'CZCE.CF2105'}),'CZCE.CF2105')
        for aliases in [{},{'CZCE.CF105':'CZCE.CF105'},{'CZCE.CF105':'CFFEX.IF2105'},
                        {'CZCE.CF105':'CZCE.SR2105'},{'CZCE.CF105':'CZCE.CF2205'}]:
            with self.assertRaisesRegex(ValueError,'explicit_contract'):canonical('CZCE.CF105',aliases)
        payload=Payloads();payload.add('a','source-original',b'raw')
        payload.add('a','source-original',b'raw')
        with self.assertRaisesRegex(ValueError,'collision'):payload.add('a','source-original',b'changed')
        for key in ['../outside','C:/outside','..','a\\b']:
            with self.assertRaisesRegex(ValueError,'artifact_path'):payload.add(key,'source-original',b'raw')

    def test_official_comma_fields_and_embedded_day_are_not_silently_reinterpreted(self):
        raw=('郑州商品交易所期货每日行情表(2022-12-30)\n'
             '合约代码|今开盘|最高价|最低价|今收盘|今结算|成交量(手)|持仓量|成交额(万元)\n'
             'CF301|14,135.00|14,280.00|14,120.00|14,225.00|14,215.00|9,313|57,180|66,200.72\n').encode()
        source=czce_rows(raw,20221230)['CF301']
        fields=official_fields(dict(open=source['今开盘'],high=source['最高价'],low=source['最低价'],
            close=source['今收盘'],volume=source['成交量(手)'],settle=source['今结算']))
        self.assertEqual(fields['close'],'14225.00');self.assertEqual(fields['settle'],'14215.00')
        self.assertIsNone(fields['open_interest'])
        with self.assertRaisesRegex(ValueError,'czce_day'):czce_rows(raw,20221229)
        self.assertIsNone(official_fields(dict(open='1',high='1',low='2',close='1')))

    def test_company_zip_duplicate_does_not_hide_behind_the_same_set_of_names(self):
        raw=b'original';manifest=json.dumps(dict(files=[dict(file='company-original.html',bytes=len(raw),
            sha256=hashlib.sha256(raw).hexdigest())])).encode()
        with tempfile.TemporaryDirectory() as directory:
            path=Path(directory)/'input.zip'
            with zipfile.ZipFile(path,'x') as z:
                z.writestr('manifest.json',manifest);z.writestr('company-original.html',raw)
            with patch('commodity_delivery.COMPANY_SHA',hashlib.sha256(manifest).hexdigest()):
                self.assertEqual(company_bundle(path)['company-original.html'],raw)
                with warnings.catch_warnings():
                    warnings.simplefilter('ignore',UserWarning)
                    with zipfile.ZipFile(path,'a') as z:z.writestr('company-original.html',raw)
                with self.assertRaisesRegex(ValueError,'company_members'):company_bundle(path)


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
