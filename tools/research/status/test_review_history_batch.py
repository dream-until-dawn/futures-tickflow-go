"""独立批次读回不得把空/部分正文计成功，也不能重复或跳过原件身份。"""
import io
import json
from pathlib import Path
import sys
import tempfile
import unittest
from review_history_batch import collect_cases,digest,EMPTY


class BatchReviewTest(unittest.TestCase):
    def test_received_empty_notrun_and_raw_tamper(self):
        with tempfile.TemporaryDirectory() as temp:
            root=Path(temp);archive=root/'archive';archive.mkdir()
            jobs=[dict(id=str(i),url='https://edb.shinnytech.com/md/kline?symbol=CZCE.AP312',product='AP') for i in range(3)]
            for i,raw in enumerate([EMPTY+b'1763251200000000000,1,2,1,2,0,0,0\r\n',EMPTY]):
                (archive/(str(i)+'.body')).write_bytes(raw)
                meta=dict(state='received' if i==0 else 'failed',body_state='complete',bytes=len(raw),evidence=dict(url=jobs[i]['url'],sha256=digest(raw)),
                          http=dict(attempted=True,response_received=True,status=200,complete=True,final_url=jobs[i]['url']),error='' if i==0 else 'CSV错误或没有样本行: <nil>')
                (archive/(str(i)+'.json')).write_text(json.dumps(meta),encoding='utf8')
            cases,states,_,_=collect_cases([root],jobs,{'CZCE.AP312':'CZCE.AP2312'})
            self.assertEqual((len(cases),states['received'],states['unavailable_complete_empty'],states['not_run']),(1,1,1,1))
            (archive/'0.body').write_bytes(b'tampered')
            with self.assertRaisesRegex(ValueError,'acquisition_raw_identity'):collect_cases([root],jobs,{'CZCE.AP312':'CZCE.AP2312'})


if __name__=='__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
