"""离线预检的正反向测试，凭证仅用合成值。"""
import contextlib
import io
from pathlib import Path
import tempfile
import sys
import unittest
from unittest import mock
import datetime as dt
import nonight_preflight as pre


class PreflightTest(unittest.TestCase):
    def cli(self, args, now):
        output = io.StringIO()
        with contextlib.redirect_stdout(output), contextlib.redirect_stderr(output):
            try:
                result = pre.main(args, now=now)
            except SystemExit as e:
                result = e.code
        return result, output.getvalue()

    def test_cli_credential_source_and_expired_windows(self):
        with tempfile.TemporaryDirectory() as tmp:
            custom = Path(tmp) / "custom.env"
            custom.write_text("\n".join(k + "=synthetic-secret" for k in pre.KEYS), encoding="utf-8")
            args = ["--phase", "evening", "--ins", "SHFE.rb2701", "--out", str(Path(tmp) / "out")]
            before = dt.datetime.fromisoformat("2026-09-30T20:58:00+08:00")
            with mock.patch.dict(pre.os.environ, dict.fromkeys(pre.KEYS, "fake"), clear=True):
                rc, output = self.cli(args + ["--env-file", str(custom)], before)
                self.assertNotEqual(rc, 0)
                self.assertNotIn("PASS", output)
                self.assertNotIn("go test", output)
                self.assertNotIn("synthetic-secret", output)
                rc, output = self.cli(args, before)
                self.assertEqual(rc, 0, output)
                self.assertIn("TestNoNightRealEvening", output)
                for phase in pre.WINDOWS:
                    rc, output = self.cli(["--phase", phase, "--ins", "SHFE.rb2701", "--out", str(Path(tmp) / "out")], dt.datetime.fromisoformat("2026-10-23T09:00:00+08:00"))
                    self.assertEqual(rc, 1, output)
                    self.assertNotIn("go test", output)

    def test_environment_takes_precedence(self):
        with tempfile.TemporaryDirectory() as tmp:
            env = Path(tmp) / "empty.env"
            env.write_text("\n".join(k + "=" for k in pre.KEYS), encoding="utf-8")
            _, issues = pre.check("SHFE.rb2701", str(Path(tmp) / "out"), env, dict.fromkeys(pre.KEYS, "fake"))
            self.assertEqual(issues, [])

    def test_missing_credentials_emits_no_command(self):
        with tempfile.TemporaryDirectory() as tmp:
            # 隔离真实仓库的凭证文件，确保缺凭证反例不会受本机配置影响。
            with mock.patch.object(pre, "ROOT", Path(tmp) / "repo"), mock.patch.dict(pre.os.environ, {}, clear=True):
                rc, output = self.cli(
                    ["--phase", "evening", "--ins", "SHFE.rb2701", "--out", str(Path(tmp) / "out")],
                    dt.datetime.fromisoformat("2026-09-30T20:58:00+08:00"),
                )
                self.assertEqual(rc, 1, output)
                self.assertNotIn("go test", output)
                self.assertFalse(any(line.startswith("PASS") for line in output.splitlines()))

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
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
