"""官方观察日期不能被宣传为上市日，年度错配和重复直接拒绝。"""
import io
import sys
import unittest

from official_inventory import accumulate


class OfficialInventoryTest(unittest.TestCase):
    def test_positive_contiguous_years_do_not_create_lifecycle(self):
        contracts = {}
        for year,day in [(2021,20211231),(2022,20220104)]:
            result=accumulate(contracts,dict(year=year,records=[dict(exchange='SHFE',native='rb2205',day=day)]))
            self.assertEqual(result['rows'],1)
        self.assertEqual(contracts['SHFE.rb2205']['years'],[2021,2022])
        self.assertEqual(contracts['SHFE.rb2205']['first_observed_day'],20211231)
        self.assertFalse(contracts['SHFE.rb2205']['lifecycle_certified'])

    def test_duplicate_and_wrong_year_are_rejected(self):
        row=dict(exchange='SHFE',native='rb2205',day=20211231)
        for dataset in [dict(year=2021,records=[row,row]),dict(year=2022,records=[row])]:
            with self.assertRaisesRegex(ValueError,'^official_year_or_duplicate$'):
                accumulate({},dataset)


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
