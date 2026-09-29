"""离线预检的正反向测试，凭证仅用合成值。"""
import contextlib
import io
from pathlib import Path
import tempfile
import unittest
import nonight_preflight as pre


class PreflightTest(unittest.TestCase):
    def test_valid_and_missing(self):
        with tempfile.TemporaryDirectory() as tmp:
            env = Path(tmp) / "fixture.env"
            env.write_text("\n".join(k + "=synthetic-secret" for k in pre.KEYS), encoding="utf-8")
            capture = io.StringIO()
            with contextlib.redirect_stdout(capture):
                out, issues = pre.check("SHFE.rb2701", str(Path(tmp) / "out"), env, {})
            self.assertEqual(issues, [])
            self.assertFalse(out.exists())
            self.assertNotIn("synthetic-secret", capture.getvalue())
            _, issues = pre.check("SHFE.rb2701", str(out), Path(tmp) / "absent", {})
            self.assertEqual(len(issues), 1)
            self.assertTrue(all(k in issues[0] for k in pre.KEYS))

    def test_rejects_contract_and_repo_path(self):
        _, issues = pre.check("KQ.m@SHFE.rb", str(pre.ROOT / "scratch"), Path("absent"), dict.fromkeys(pre.KEYS, "fake"))
        self.assertEqual(len(issues), 2)

    def test_preserves_existing_evidence(self):
        with tempfile.TemporaryDirectory() as tmp:
            evidence = Path(tmp) / "raw.jsonl"
            evidence.write_bytes(b"original")
            _, issues = pre.check("SHFE.rb2701", tmp, Path("absent"), dict.fromkeys(pre.KEYS, "fake"))
            self.assertEqual(len(issues), 1)
            self.assertEqual(evidence.read_bytes(), b"original")


if __name__ == "__main__":
    unittest.main()
