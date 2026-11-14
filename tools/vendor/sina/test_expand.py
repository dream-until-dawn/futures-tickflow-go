"""跨批控制用假CLI标定；不发网络请求。"""
import io
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch
import expand


class ExpansionTest(unittest.TestCase):
    def exercise(self, fail):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            binary = root / "fake.exe"
            binary.write_bytes(b"fake-not-executable")
            output = root / "execution"
            proposal = Path(__file__).resolve().parents[3] / "docs/evidence/sina-vendor-next-20261114/proposal.json"
            calls = []

            def execute(binary, root, plan, count, static=False):
                calls.append((plan.name, static))
                blocked = fail and not static
                return dict(state="failed-stop-host-and-dependent-batches" if blocked else "completed", requests=1 if blocked else count, bytes=10, returncode=int(blocked), failures=["first"] if blocked else [])

            argv = ["expand.py", str(proposal), str(binary), "f" * 40, str(output)]
            with patch.object(sys, "argv", argv), patch.object(expand, "execute", execute), patch.object(expand.subprocess, "check_output", return_value="vcs.revision=" + "f" * 40 + "\nvcs.modified=false"), patch.object(expand.time, "sleep"):
                expand.main()
                before = len(calls)
                with self.assertRaises(FileExistsError):
                    expand.main()
                self.assertEqual(before, len(calls))
            state = json.loads((output / "state.json").read_text(encoding="utf-8"))
            self.assertEqual(2 if fail else 7, len(calls))
            self.assertEqual(1 if fail else 230, state["market_requests"])
            self.assertEqual(1, state["static_requests"])
            self.assertEqual(fail, state["future_market_batches_stopped"])
            self.assertEqual("blocked-with-diagnostics" if fail else "completed-raw-only", state["state"])

    def test_positive_six_batches_and_existing_directory_refused(self):
        self.exercise(False)

    def test_failure_stops_all_dependent_market_batches(self):
        self.exercise(True)


if __name__ == "__main__":
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
