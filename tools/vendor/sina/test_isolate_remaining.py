"""只用假执行器验证新工具跨批失败停止，不发市场请求。"""
import io
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch
import isolate_remaining


class IsolationCoordinatorTest(unittest.TestCase):
    def exercise(self, fail):
        with tempfile.TemporaryDirectory() as directory:
            folder = Path(directory)
            binary = folder / "fake.exe"
            binary.write_bytes(b"not-executable")
            profile = Path(__file__).resolve().parents[3] / "docs/evidence/sina-null-isolation-20261114/profile.json"
            output = folder / "execution"
            calls = []

            def run_batch(binary, output, profile, plan):
                calls.append(plan.name)
                count = len(json.loads(plan.read_text(encoding="utf-8")))
                return dict(state="blocked-with-diagnostics" if fail else "completed-with-quarantined-unavailable", success=not fail, requests=2 if fail else count, bytes=100, received=1 if fail else count - 1, quarantined=0 if fail else 1, returncode=int(fail))

            argv = ["isolate_remaining.py", str(profile), str(binary), "f" * 40, str(output)]
            with patch.object(sys, "argv", argv), patch.object(isolate_remaining, "run_batch", run_batch), patch.object(isolate_remaining.subprocess, "check_output", return_value="vcs.revision=" + "f" * 40 + "\nvcs.modified=false"), patch.object(isolate_remaining.time, "sleep"):
                isolate_remaining.main()
                before = len(calls)
                with self.assertRaises(FileExistsError):
                    isolate_remaining.main()
                self.assertEqual(before, len(calls))
            report = json.loads((output / "state.json").read_text(encoding="utf-8"))
            self.assertEqual(1 if fail else 5, len(calls))
            self.assertEqual(2 if fail else 175, report["requests"])
            self.assertEqual("blocked-with-diagnostics" if fail else "completed-with-unavailable-contracts", report["state"])
            self.assertFalse(report["covered"])
            self.assertFalse(report["financial_eligible"])
            self.assertFalse(report["old_stop_modified"])

    def test_full_schedule_and_quarantine_are_not_coverage(self):
        self.exercise(False)

    def test_other_failure_stops_all_later_batches(self):
        self.exercise(True)


if __name__ == "__main__":
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
