"""反例必须指向收据身份和原字节绑定，不能用解析失败代替。"""
import hashlib
import csv
import io
import json
import sys
import tempfile
import unittest
import zipfile
from pathlib import Path
from unittest.mock import patch

from cffex_months import IDENTITY,SDK_SHA,verify_month
from cffex_independent import audit


class CffexMonthReceiptTest(unittest.TestCase):
    def test_independent_fields_detect_mutated_decode(self):
        with tempfile.TemporaryDirectory() as d:
            root=Path(d);text=io.StringIO();writer=csv.writer(text)
            names={'open':'今开盘','high':'最高价','low':'最低价','close':'今收盘','settle':'今结算',
                   'pre_settle':'前结算','volume':'成交量','open_interest':'持仓量','turnover':'成交金额'}
            writer.writerow(['合约代码']+list(names.values()));writer.writerow(['IF2102']+['1']*9)
            b=io.BytesIO()
            with zipfile.ZipFile(b,'w') as z:z.writestr('20210201_1.csv',text.getvalue().encode('gb18030'))
            raw=b.getvalue();(root/'month.body').write_bytes(raw)
            (root/'receipt.json').write_text(json.dumps(dict(sha256=hashlib.sha256(raw).hexdigest())),encoding='utf-8')
            data=dict(records=[dict(native='IF2102',day=20210201,fields={k:'1' for k in names})],
                      summary=dict(excluded_options=0,excluded_aggregate_rows=0))
            output=root/'decoded.json';output.write_text(json.dumps(data),encoding='utf-8')
            self.assertEqual(audit(root,output,'202102')['fields'],9)
            data['records'][0]['fields']['close']='2'
            output.write_text(json.dumps(data),encoding='utf-8')
            with self.assertRaisesRegex(ValueError,'^audit_fields$'):audit(root,output,'202102')

    def test_receipt_body_binding_and_unauthenticated_identity(self):
        with tempfile.TemporaryDirectory() as d:
            root=Path(d);raw=b'fixture';digest=hashlib.sha256(raw).hexdigest()
            job=dict(month='202102',url='http://www.cffex.com.cn/sj/historysj/202102/zip/202102.zip')
            receipt=dict(state='received',status=200,body_complete=True,url=job['url'],
                         identity=IDENTITY,publisher_authentication=False,financial_eligible=False,
                         method='GET',attempted=True,response_received=True,source_sha256=SDK_SHA,
                         bytes=len(raw),sha256=digest,evidence=dict(sha256=digest,url=job['url']))
            (root/'month.body').write_bytes(raw)
            def put(r): (root/'receipt.json').write_text(json.dumps(r),encoding='utf-8')
            put(receipt)
            with patch('cffex_months.decode_cffex',return_value=([],[])) as decoder:
                _,_,summary=verify_month(root,job,{})
                self.assertEqual(summary['body_sha256'],digest)
                self.assertEqual(decoder.call_args.kwargs['month'],'202102')
            for change in [dict(publisher_authentication=True),dict(url=job['url'].replace('202102','202103')),
                           dict(body_complete=False),dict(financial_eligible=True),dict(state='failed'),
                           dict(attempted=False),dict(response_received=False),dict(method='POST'),dict(source_sha256='bad')]:
                put({**receipt,**change})
                with self.assertRaisesRegex(ValueError,'^cffex_receipt_identity$'):verify_month(root,job,{})
            put(receipt);(root/'month.body').write_bytes(b'changed')
            with self.assertRaisesRegex(ValueError,'^cffex_body_sha$'):verify_month(root,job,{})


if __name__=='__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
