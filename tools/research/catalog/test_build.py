"""保持观察、空响应、未请求、制度缺证和金融准入的区别。"""
import copy
import datetime as dt
import io
import sys
import unittest
from build import attach_nonempty, candidates, daily_days, HEADER, ZONE
import hashlib


def csv_for(days):
    rows = [','.join(HEADER)]
    for day in days:
        instant = dt.datetime.strptime(str(day), '%Y%m%d').replace(tzinfo=ZONE)
        rows.append(str(int(instant.timestamp())*10**9)+',1,1,1,1,0,1,1')
    return ('\n'.join(rows)+'\n').encode()


class CatalogBoundaryTest(unittest.TestCase):
    def fixture(self):
        item = dict(canonical='CZCE.PF2105', source_key='CZCE.PF105', exchange='CZCE', product='PF', delivery_year=2021, delivery_month=5, expiry_seconds=1620956400)
        entry = candidates(dict(contracts=[item]))['CZCE.PF2105']
        raw = csv_for([20210104, 20210105]);url='https://edb.shinnytech.com/md/kline?period=86400&symbol=CZCE.PF105'
        receipt = dict(state='received',body_state='complete',bytes=len(raw),evidence=dict(url=url,sha256=hashlib.sha256(raw).hexdigest()),http=dict(status=200,complete=True,final_url=url))
        review = dict(rows=2, first_day=20210104, last_day=20210105, financial_eligible=False, official_lifecycle_certified=False, missing_official_days=[],official_days=1,overlapping_days=1,binary64_distinct_differences=1,calendar_unknown_actual_days=[20210104,20210105],calendar_product_profile_available=False)
        return item, entry, raw, receipt, review

    def test_actual_observation_does_not_promote_lifecycle_or_admission(self):
        _, entry, raw, receipt, review = self.fixture()
        attach_nonempty(entry,raw,receipt,review,'czce')
        self.assertEqual(entry['availability']['last_observed_day'],20210105)
        self.assertIsNone(entry['official_last_trading_day'])
        self.assertEqual(entry['reference_review']['unresolved_distinct_fields'],1)
        self.assertFalse(entry['financial_eligible'])

    def test_header_only_is_empty_but_bad_header_is_not(self):
        self.assertEqual(daily_days((','.join(HEADER)+'\n').encode()),[])
        with self.assertRaisesRegex(ValueError,'^daily_header$'):daily_days(b'date,close\n')

    def test_duplicate_days_and_nonmidnight_are_rejected(self):
        with self.assertRaisesRegex(ValueError,'^daily_anchor_or_order$'):daily_days(csv_for([20210104,20210104]))
        raw=csv_for([20210104]);old=raw.split(b'\n')[1].split(b',')[0]
        with self.assertRaisesRegex(ValueError,'^daily_anchor_or_order$'):daily_days(raw.replace(old,str(int(old)+1).encode()))

    def test_wrong_source_native_and_complete_state_are_rejected(self):
        _, entry, raw, receipt, review = self.fixture()
        receipt['evidence']['url']=receipt['evidence']['url'].replace('PF105','PF2105')
        with self.assertRaisesRegex(ValueError,'^receipt_symbol_or_period$'):attach_nonempty(entry,raw,receipt,review,'czce')
        _, entry, raw, receipt, review = self.fixture();receipt['http']['complete']=False
        with self.assertRaisesRegex(ValueError,'^receipt_complete_identity$'):attach_nonempty(entry,raw,receipt,review,'czce')

    def test_range_conflict_scope_promotion_and_repeat_do_not_pass(self):
        _, entry, raw, receipt, review = self.fixture();review['rows']=1
        with self.assertRaisesRegex(ValueError,'^daily_report_range$'):attach_nonempty(entry,raw,receipt,review,'czce')
        _, entry, raw, receipt, review = self.fixture();review['financial_eligible']=True
        with self.assertRaisesRegex(ValueError,'^review_scope_promotion$'):attach_nonempty(entry,raw,receipt,review,'czce')
        _, entry, raw, receipt, review = self.fixture();attach_nonempty(entry,raw,receipt,review,'czce')
        with self.assertRaisesRegex(ValueError,'^duplicate_contract_observation$'):attach_nonempty(entry,raw,receipt,review,'czce')

    def test_expiry_filter_is_not_an_official_lifecycle_and_duplicates_reject(self):
        item,*_=self.fixture();item['expiry_seconds']=1600000000
        self.assertEqual(candidates(dict(contracts=[item])),{})
        with self.assertRaisesRegex(ValueError,'^inventory_duplicate$'):candidates(dict(contracts=[item,copy.deepcopy(item)]))


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
