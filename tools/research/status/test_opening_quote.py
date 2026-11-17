"""实际开仓表头、当日标称日期、显式年份及正反例；不能从评论和普通1手填空。"""
import io
import sys
import hashlib
import unittest
from opening_quote import parse


class OpeningQuoteTest(unittest.TestCase):
    def fixture(self,header='最小开仓(手)',day='2026-11-17',minimum='2'):
        rows=[['合约品种','现价','交易单位','涨/跌停板',header,'保证金%','手续费'],
              ['苹果(AP701)','100','10吨/手','110/90',minimum,'9%','9%','90元','5元','5元','5元','10','10元','0','']]
        raw=('<html><table>'+''.join('<tr>'+''.join('<td>'+v+'</td>' for v in row)+'</tr>' for row in rows)+'</table>依据交易所公告每日自动整理，更新至'+day+'。评论：最少1手</html>').encode()
        receipt=dict(state='received_scope_unreviewed',http=dict(status=200,complete=True),sha256=hashlib.sha256(raw).hexdigest(),finished_at='2026-11-17T08:00:00Z',url='https://example.invalid/quote')
        return raw,receipt

    def test_explicit_two_survives_comment_one_and_requires_binding(self):
        raw,r=self.fixture();result=parse(raw,r,'CZCE.AP',20261117,{'AP701':'CZCE.AP2701'})
        self.assertEqual(result['contracts']['CZCE.AP2701']['effective_min_open_lots'],2)
        with self.assertRaisesRegex(ValueError,'no_usable_rows'):parse(raw,r,'CZCE.AP',20261117,{})

    def test_old_or_future_day_wrong_header_and_missing_value_rejected(self):
        for options,reason in [({'day':'2026-11-16'},'reported_update_day'),({'day':'2026-11-18'},'reported_update_day'),
                               ({'header':'最小下单量'},'actual_open_header'),({'header':'最大开仓量'},'actual_open_header'),
                               ({'minimum':''},'no_usable_rows'),({'minimum':'0'},'no_usable_rows')]:
            raw,r=self.fixture(**options)
            with self.assertRaisesRegex(ValueError,reason):parse(raw,r,'CZCE.AP',20261117,{'AP701':'CZCE.AP2701'})
        raw,r=self.fixture()
        with self.assertRaisesRegex(ValueError,'transport_identity'):parse(raw+b'x',r,'CZCE.AP',20261117,{'AP701':'CZCE.AP2701'})


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
