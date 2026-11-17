"""HTML可见正文与原件身份复核；归一化不能吞掉挑战、源篡改或索引缺页。"""
import io
import sys
import hashlib
import json
from pathlib import Path
import tempfile
import unittest
from current_constraints import visible, index_entries, verify_original


class CurrentConstraintsTest(unittest.TestCase):
    def test_visible_date_can_span_tags_but_script_is_not_evidence(self):
        raw='<p>自2026年<span>9月14日</span>起实施</p><script>不存在的最小开仓</script>'.encode()
        self.assertIn('自2026年9月14日起实施',visible(raw))
        self.assertNotIn('最小开仓',visible(raw))

    def test_original_positive_and_targeted_mutation(self):
        with tempfile.TemporaryDirectory() as d:
            root=Path(d);raw='<html>发布日期：正文</html>'.encode();(root/'source').write_bytes(raw)
            sha=hashlib.sha256(raw).hexdigest()
            receipt=dict(http=dict(complete=True,status=200,final_url='https://www.shfe.com.cn/a'),sha256=sha)
            rb=json.dumps(receipt).encode();(root/'receipt').write_bytes(rb)
            row=dict(file='source',receipt_file='receipt',bytes=len(raw),sha256=sha,receipt_sha256=hashlib.sha256(rb).hexdigest(),url='https://www.shfe.com.cn/a',required_visible_text=['正文'])
            self.assertIn('正文',verify_original(row,root))
            (root/'source').write_bytes(raw+b'changed')
            with self.assertRaisesRegex(ValueError,'source_identity'):verify_original(row,root)
            (root/'source').write_bytes(raw);row['required_visible_text']=['不存在的最小开仓']
            with self.assertRaisesRegex(ValueError,'business_text'):verify_original(row,root)

    def test_index_rejects_missing_duplicate_and_disordered_rows(self):
        rows=[f'<div class="info_item_title"><a href="./202611/t202611{i:02}_a{i}.html" title="公告">公告</a></div><div class="info_item_date">2026-11-{i:02}</div>' for i in range(20,0,-1)]
        self.assertEqual(len(index_entries(''.join(rows).encode())),20)
        with self.assertRaisesRegex(ValueError,'count_or_duplicate'):index_entries(''.join(rows[:-1]).encode())
        with self.assertRaisesRegex(ValueError,'order'):index_entries(''.join(reversed(rows)).encode())
        with self.assertRaisesRegex(ValueError,'count_or_duplicate'):index_entries(''.join(rows[:-1]+rows[:1]).encode())


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
