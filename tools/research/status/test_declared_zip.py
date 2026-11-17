"""实际额外缓存目录故障的正反控制；源篡改和路径穿越分别按目标原因拒绝。"""
import io
import sys
import hashlib
import json
from pathlib import Path
import tempfile
import unittest
import zipfile
from declared_zip import build,verify,declared


class DeclaredZipTest(unittest.TestCase):
    def fixture(self,root):
        raw=b'original';(root/'input').write_bytes(raw)
        m=dict(files=[dict(file='input',bytes=len(raw),sha256=hashlib.sha256(raw).hexdigest())])
        (root/'manifest.json').write_text(json.dumps(m),encoding='utf-8')
        return m

    def test_cache_directory_is_not_packaged_but_injection_rejected(self):
        with tempfile.TemporaryDirectory() as d:
            root=Path(d);self.fixture(root);(root/'__pycache__').mkdir();(root/'unlisted').write_bytes(b'not-delivered')
            archive=root/'good.zip';build(root,archive);verify(archive)
            with zipfile.ZipFile(archive) as z:self.assertEqual(set(z.namelist()),{'manifest.json','input'})
            with zipfile.ZipFile(archive,'a') as z:z.writestr('__pycache__/',b'')
            with self.assertRaisesRegex(ValueError,'^declared_zip_member_set$'):verify(archive)

    def test_modified_source_refused_before_output_and_unsafe_names_refused(self):
        with tempfile.TemporaryDirectory() as d:
            root=Path(d);m=self.fixture(root);(root/'input').write_bytes(b'changed')
            with self.assertRaisesRegex(ValueError,'^declared_zip_source_identity:input$'):build(root,root/'bad.zip')
            self.assertFalse((root/'bad.zip').exists())
            m['files'][0]['file']='../input'
            with self.assertRaisesRegex(ValueError,'unsafe_name'):declared(m)


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
