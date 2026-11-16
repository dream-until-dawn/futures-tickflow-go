"""原件身份、显式年份及独立字段复核的正反控制。"""
import copy
import hashlib
import unittest
from reference_inventory import compare


class ReferenceTest(unittest.TestCase):
    def test_snapshot_positive_and_targeted_failures(self):
        raw = '<Contracts><Contract><PrdTp>期货</PrdTp><PrdCd>RM</PrdCd><YrCd>2027</YrCd><MnthCd>11</MnthCd><CtrCd>RM711</CtrCd><FrstTrdDt>2026-11-16</FrstTrdDt><LstTrdDt>2027-11-12</LstTrdDt></Contract></Contracts>'.encode()
        review = dict(body_sha256=hashlib.sha256(raw).hexdigest(), financial_admission_added=0,
                      entries=[dict(canonical='CZCE.RM2711', native='RM711', listed_declared=20261116, last_trading_declared=20271112)])
        self.assertEqual(compare(raw, review)['CZCE.RM2711'], ('RM711', 20261116, 20271112))
        with self.assertRaisesRegex(ValueError, 'reference_raw_sha'):
            compare(raw+b' ', review)
        changed=copy.deepcopy(review);changed['entries'][0]['last_trading_declared']=20271111
        with self.assertRaisesRegex(ValueError, 'reference_independent_mismatch'):
            compare(raw, changed)
        wrong=raw.replace(b'RM711', b'RM611');changed=copy.deepcopy(review)
        changed['body_sha256']=hashlib.sha256(wrong).hexdigest()
        with self.assertRaisesRegex(ValueError, 'reference_native_year'):
            compare(wrong, changed)


if __name__ == '__main__': unittest.main()
