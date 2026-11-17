"""公司来源、标示日和列语义正反控制；标题不能替代真实字段。"""
import hashlib
import io
import json
import sys
import unittest

from broker_reference import URL, parse


class BrokerReferenceTest(unittest.TestCase):
    def fixture(self, header='投机'):
        grid=[['交易所','合约','保证金比例','保证金比例','备注'],
              ['交易所','合约',header,'套保','备注'], ['上期所','rb2612','16%','15%','临近交割期']]
        html='<table>'+''.join('<tr>'+''.join('<td>'+x+'</td>' for x in row)+'</tr>' for row in grid)+'</table>'
        raw=('<title>2026年11月16日交易结算信息表</title><script>F.content='+json.dumps(html,ensure_ascii=False)+';</script>').encode()
        return raw,dict(url=URL,state='received_scope_unreviewed',sha256=hashlib.sha256(raw).hexdigest(),http=dict(status=200,complete=True))

    def test_ratios_are_not_sides_or_prices_and_effective_day_stays_unknown(self):
        raw,receipt=self.fixture();row,=parse(raw,receipt,20261116)
        self.assertEqual(row['company_spec_ratio'],'0.16')
        self.assertEqual(row['company_hedge_ratio'],'0.15')
        self.assertIsNone(row['effective_trade_day'])
        self.assertNotIn('settle',row)
        self.assertNotIn('long',row)
        with self.assertRaisesRegex(ValueError,'title_day'):parse(raw,receipt,20261117)
        with self.assertRaisesRegex(ValueError,'source_identity'):parse(raw+b' ',receipt,20261116)
        with self.assertRaisesRegex(ValueError,'source_identity'):parse(raw,dict(receipt,http=dict(status=200,complete=False)),20261116)

    def test_different_columns_cannot_silently_be_read_as_speculation(self):
        raw,receipt=self.fixture(header='结算价')
        with self.assertRaisesRegex(ValueError,'reference_header'):parse(raw,receipt,20261116)


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
