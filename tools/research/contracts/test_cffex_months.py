"""反例必须指向收据身份和原字节绑定，不能用解析失败代替。"""
import hashlib
import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from cffex_months import IDENTITY,verify_month


class CffexMonthReceiptTest(unittest.TestCase):
    def test_receipt_body_binding_and_unauthenticated_identity(self):
        with tempfile.TemporaryDirectory() as d:
            root=Path(d);raw=b'fixture';digest=hashlib.sha256(raw).hexdigest()
            job=dict(month='202102',url='http://www.cffex.com.cn/sj/historysj/202102/zip/202102.zip')
            receipt=dict(state='received',status=200,body_complete=True,url=job['url'],
                         identity=IDENTITY,publisher_authentication=False,financial_eligible=False,
                         bytes=len(raw),sha256=digest,evidence=dict(sha256=digest))
            (root/'month.body').write_bytes(raw)
            def put(r): (root/'receipt.json').write_text(json.dumps(r),encoding='utf-8')
            put(receipt)
            with patch('cffex_months.decode_cffex',return_value=([],[])) as decoder:
                _,_,summary=verify_month(root,job,{})
                self.assertEqual(summary['body_sha256'],digest)
                self.assertEqual(decoder.call_args.kwargs['month'],'202102')
            for change in [dict(publisher_authentication=True),dict(url=job['url'].replace('202102','202103')),
                           dict(body_complete=False),dict(financial_eligible=True),dict(state='failed')]:
                put({**receipt,**change})
                with self.assertRaisesRegex(ValueError,'^cffex_receipt_identity$'):verify_month(root,job,{})
            put(receipt);(root/'month.body').write_bytes(b'changed')
            with self.assertRaisesRegex(ValueError,'^cffex_body_sha$'):verify_month(root,job,{})


if __name__=='__main__':unittest.main()
