"""用真实冻结输入作封存正控；资格提升与原件篡改副本必须被拒绝。"""
import io
import json
from pathlib import Path
import shutil
import sys
import tempfile
import unittest
from unittest.mock import patch
import seal_isolation

ROOT = Path(__file__).resolve().parents[3]


class IsolationSealTest(unittest.TestCase):
    def reconstruct(self, destination):
        evidence = ROOT / "docs/evidence/sina-null-results-20261114"
        destination.mkdir()
        for name in ("profile.json", "state.json", "build-info.txt"):
            shutil.copyfile(evidence / name, destination / name)
        for path in sorted(evidence.glob("batch-*.json")):
            shutil.copyfile(path, destination / path.name)
            batch = destination / path.stem
            batch.mkdir()
            for kind in ("raw", "started", "receipts"):
                (batch / kind).mkdir()
            shutil.copyfile(evidence / "reports" / path.name, batch / "report.json")
            shutil.copyfile(evidence / "reports" / path.name, destination / (path.stem + "-stdout.json"))
            for job in json.loads(path.read_text(encoding="utf-8")):
                for kind, suffix in (("raw", ".jsonp"), ("started", ".json"), ("receipts", ".json")):
                    name = job["id"] + suffix
                    shutil.copyfile(evidence / kind / name, batch / kind / name)

    def call(self, execution, prior, output):
        with patch.object(sys, "argv", ["seal_isolation.py", str(execution), str(prior), str(output)]):
            seal_isolation.main()

    def test_real_positive_and_state_promotion_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            folder = Path(directory)
            execution = folder / "execution"
            self.reconstruct(execution)
            prior = ROOT / "docs/evidence/sina-vendor-expanded-20261114"
            output = folder / "positive"
            self.call(execution, prior, output)
            scope = json.loads((output / "scope.json").read_text(encoding="utf-8"))
            self.assertEqual((175, 169, 6, 19346, 234, 0), tuple(scope[k] for k in ("actual_requests", "normal_arrays", "failed_quarantined", "normal_target_rows", "current_array_codes", "unattempted_current_codes")))
            self.assertFalse(scope["financial_eligible"])
            state = json.loads((execution / "state.json").read_text(encoding="utf-8"))
            state["covered"] = True
            (execution / "state.json").write_text(json.dumps(state), encoding="utf-8")
            with self.assertRaisesRegex(ValueError, "资格或原stop"):
                self.call(execution, prior, folder / "bad")
            self.assertFalse((folder / "bad").exists())

    def test_prior_raw_mutation_rejected_before_output(self):
        with tempfile.TemporaryDirectory() as directory:
            folder = Path(directory)
            execution = folder / "execution"
            self.reconstruct(execution)
            prior = folder / "prior"
            shutil.copytree(ROOT / "docs/evidence/sina-vendor-expanded-20261114", prior)
            raw = prior / "raw/daily-A2711.jsonp"
            raw.write_bytes(raw.read_bytes() + b"tampered")
            with self.assertRaisesRegex(ValueError, "原封存被改变"):
                self.call(execution, prior, folder / "bad")
            self.assertFalse((folder / "bad").exists())


if __name__ == "__main__":
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
