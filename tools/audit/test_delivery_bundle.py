"""交接包校验器正反控制：必须命中具体原因，不能被其他守卫代替。"""
import json
import io
from pathlib import Path
import shutil
import tempfile
import sys
import unittest

from delivery_bundle import digest, inventory, verify


class DeliveryBundleTest(unittest.TestCase):
    def fixture(self, root):
        root.mkdir()
        (root / "payload").write_bytes(b"frozen-source")
        scope = dict(schema="staged-delivery-scope-v1", source_sha="a" * 40,
                     full_release=False, all_market_originals_included=False,
                     financial_admission_extended=False, live_l9_verified=False, tag_created=False)
        (root / "scope.json").write_text(json.dumps(scope), encoding="utf-8")
        return self.seal(root)

    def seal(self, root):
        (root / "manifest.json").write_text(json.dumps(dict(schema="staged-delivery-files-v1", files=inventory(root))), encoding="utf-8")
        return digest(root / "manifest.json")

    def test_positive_migration_and_exact_rejection_matrix(self):
        with tempfile.TemporaryDirectory() as temp:
            base = Path(temp) / "original"
            sha = self.fixture(base)
            self.assertEqual(verify(base, sha)["files"], 2)
            migrated = Path(temp) / "migrated"
            shutil.copytree(base, migrated)
            self.assertEqual(verify(base, sha), verify(migrated, sha))
            for mode, reason in [("mutate", "文件字节/SHA不符：payload"), ("delete", "文件集合不符"), ("extra", "文件集合不符"), ("manifest", "包外manifest SHA不符")]:
                with self.subTest(mode=mode):
                    bad = Path(temp) / mode
                    shutil.copytree(base, bad)
                    if mode == "mutate":
                        (bad / "payload").write_bytes(b"changed")
                    elif mode == "delete":
                        (bad / "payload").unlink()
                    elif mode == "extra":
                        (bad / "extra").write_bytes(b"unlisted")
                    else:
                        (bad / "manifest.json").write_bytes(b"{}")
                    with self.assertRaisesRegex(ValueError, reason):
                        verify(bad, sha)

    def test_scope_escalation_even_with_new_correct_manifest(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp) / "bundle"
            self.fixture(root)
            path = root / "scope.json"
            original = json.loads(path.read_text())
            for key in ("full_release", "all_market_originals_included", "financial_admission_extended", "live_l9_verified", "tag_created"):
                scope = dict(original)
                scope[key] = True
                path.write_text(json.dumps(scope), encoding="utf-8")
                with self.assertRaisesRegex(ValueError, "阶段范围不能提升资格：" + key):
                    verify(root, self.seal(root))

    def test_duplicate_manifest_key_and_untrusted_digest(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp) / "bundle"
            sha = self.fixture(root)
            with self.assertRaisesRegex(ValueError, "包外manifest SHA不符"):
                verify(root, "0" * 64)
            path = root / "manifest.json"
            path.write_text('{"schema":"staged-delivery-files-v1","files":{},"files":{}}', encoding="utf-8")
            with self.assertRaisesRegex(ValueError, "重复JSON键"):
                verify(root, digest(path))

    def test_noncanonical_parent_path_matches_resolved_root(self):
        """复现Windows临时目录别名的同类路径问题，不依赖创建链接的特权。"""
        with tempfile.TemporaryDirectory() as temp:
            parent = Path(temp) / "physical"
            (parent / "holder").mkdir(parents=True)
            alias = parent / "holder" / ".." / "bundle"
            sha = self.fixture(alias)
            self.assertEqual(verify(alias, sha), verify(alias.resolve(), sha))
            self.assertEqual(inventory(alias), inventory(alias.resolve()))


if __name__ == "__main__":
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
