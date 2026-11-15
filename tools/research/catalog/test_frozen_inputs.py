"""输入迁移保留外部可信清单；逐件身份与完整旧包范围分别检查。"""
import hashlib
import io
import json
from pathlib import Path
import sys
import tempfile
import unittest
from frozen_inputs import FrozenInputs


class FrozenInputTest(unittest.TestCase):
    def make(self, root):
        p=root/'data.json';p.write_bytes(b'{}')
        rows=[dict(path=p.name,bytes=2,sha256=hashlib.sha256(p.read_bytes()).hexdigest())]
        manifest=root/'manifest.json';manifest.write_text(json.dumps(dict(files=rows)),encoding='utf-8')
        return hashlib.sha256(manifest.read_bytes()).hexdigest()

    def test_projection_replays_and_extra_file_only_breaks_full_package_check(self):
        with tempfile.TemporaryDirectory() as d:
            root=Path(d)/'source';root.mkdir();trusted=self.make(root);p=FrozenInputs(root,trusted)
            self.assertEqual(p.verify_all(),1);self.assertEqual(p.data('data.json'),{})
            target=Path(d)/'projection';p.export_used(target)
            self.assertEqual(FrozenInputs(target,trusted).data('data.json'),{})
            (root/'extra').write_bytes(b'x')
            with self.assertRaisesRegex(ValueError,'^input_file_set$'):p.verify_all()

    def test_tamper_and_rewritten_manifest_have_different_target_failures(self):
        with tempfile.TemporaryDirectory() as d:
            root=Path(d);trusted=self.make(root);p=FrozenInputs(root,trusted);(root/'data.json').write_bytes(b'[]')
            with self.assertRaisesRegex(ValueError,'^input_file_sha:data.json$'):p.read('data.json')
            (root/'manifest.json').write_text('{}',encoding='utf-8')
            with self.assertRaisesRegex(ValueError,'^input_manifest_sha$'):FrozenInputs(root,trusted)

    def test_unsafe_duplicate_and_unlocked_paths_are_rejected(self):
        for paths,reason in [(['../escape'],'unsafe_input_path'),(['data.json','data.json'],'input_manifest_duplicate')]:
            with self.subTest(paths=paths), tempfile.TemporaryDirectory() as d:
                root=Path(d);rows=[dict(path=p,bytes=0,sha256='0'*64) for p in paths]
                m=root/'manifest.json';m.write_text(json.dumps(dict(files=rows)),encoding='utf-8');trusted=hashlib.sha256(m.read_bytes()).hexdigest()
                with self.assertRaisesRegex(ValueError,'^'+reason+'$'):FrozenInputs(root,trusted)
        with tempfile.TemporaryDirectory() as d:
            root=Path(d);p=FrozenInputs(root,self.make(root))
            with self.assertRaisesRegex(ValueError,'^input_not_locked:missing$'):p.read('missing')

    def test_locked_missing_file_reports_exact_path(self):
        with tempfile.TemporaryDirectory() as d:
            root=Path(d);p=FrozenInputs(root,self.make(root));(root/'data.json').unlink()
            with self.assertRaisesRegex(ValueError,'^input_file_missing:data.json$'):p.read('data.json')


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
