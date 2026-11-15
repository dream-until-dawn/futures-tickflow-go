"""首次上市目录关联不做三位原码的最近十年猜测，原生代码集合必须完整。"""
import io
import sys
import unittest
from listing import links


class ListingLinkTest(unittest.TestCase):
    def fixture(self):
        cy='2017年8月18日 2017年8月14日 21:00—23:30 '+' '.join('CY180'+str(i) for i in range(1,9))
        pf='2020年10月12日 2020年9月28日 21:00至23:00 '+' '.join('PF10'+str(i) for i in range(5,10))
        items=[dict(exchange='CZCE',canonical='CZCE.CY180'+str(i),source_key='CZCE.CY80'+str(i)) for i in range(1,9)]
        items += [dict(exchange='CZCE',canonical='CZCE.PF210'+str(i),source_key='CZCE.PF10'+str(i)) for i in range(5,10)]
        return dict(contracts=items),cy,pf

    def test_three_digit_native_remains_candidates_even_when_unique(self):
        inventory,cy,pf=self.fixture();rows=links(inventory,cy,pf)
        self.assertEqual(len(rows),13)
        self.assertIsNone(rows[8]['official_canonical_symbol'])
        self.assertEqual(rows[8]['snapshot_candidates'],['CZCE.PF2105'])
        self.assertEqual(rows[0]['official_canonical_symbol'],'CZCE.CY1801')
        inventory['contracts'].append(dict(exchange='CZCE',canonical='CZCE.PF3105',source_key='CZCE.PF105'))
        self.assertEqual(links(inventory,cy,pf)[8]['snapshot_candidates'],['CZCE.PF2105','CZCE.PF3105'])

    def test_missing_official_contract_or_changed_historical_night_rejects(self):
        inventory,cy,pf=self.fixture()
        with self.assertRaisesRegex(ValueError,'^first_listing_contract_set$'):links(inventory,cy.replace('CY1808',''),pf)
        with self.assertRaisesRegex(ValueError,'^cy_listing_marker:'):links(inventory,cy.replace('23:30','23:00'),pf)

    def test_absent_snapshot_match_is_not_filled_from_delivery_month(self):
        inventory,cy,pf=self.fixture();inventory['contracts']=inventory['contracts'][1:]
        with self.assertRaisesRegex(ValueError,'^listing_snapshot_no_candidate$'):links(inventory,cy,pf)


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
