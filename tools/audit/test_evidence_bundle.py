"""合成证据封存、移动复核及篡改拒绝测试。"""
import hashlib
import io
import json
from pathlib import Path
import shutil
import sys
import tempfile
import unittest

from evidence_bundle import seal, verify
from test_review_long_gap import fixture


def write_fixture(root):
    (root / "capture").mkdir()
    env, events, raw, history, live = fixture()
    for name, rows in {"reference": raw, "history": history, "live": live, "events": [env] + events}.items():
        (root / "capture" / (name + ".jsonl")).write_text("\n".join(json.dumps(row) for row in rows))
    binary = b"unit-only"
    (root / "capture.test").write_bytes(binary)
    (root / "plan.json").write_text("{}")
    manifest = {k: env[k.upper()] for k in ("begin", "drop", "resume", "end")}
    manifest.update(contract=env["INS"], sha="a" * 40, provenance="synthetic", binary_file="capture.test",
                    binary_sha256=hashlib.sha256(binary).hexdigest(), plan_sha256=hashlib.sha256(b"{}").hexdigest())
    (root / "manifest.json").write_text(json.dumps(manifest))
    (root / "run.log").write_text("--- PASS: TestGapOfflineBundle\nPASS\n")
    (root / "status.json").write_text('{"state":"completed"}')


class EvidenceTest(unittest.TestCase):
    def test_copy_and_mutation_matrix(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory) / "source"
            root.mkdir()
            write_fixture(root)
            with self.assertRaises(ValueError):
                seal(root)
            seal(root, allow_synthetic=True)
            copied = Path(directory) / "moved"
            shutil.copytree(root, copied)
            self.assertEqual(verify(copied, True)["validation"], "passed")
            for mode in ("missing", "extra", "changed", "binary", "classification"):
                target = Path(directory) / mode
                shutil.copytree(root, target)
                if mode == "missing":
                    (target / "capture/live.jsonl").unlink()
                elif mode == "extra":
                    (target / "extra.json").write_text("extra")
                elif mode == "binary":
                    (target / "capture.test").write_bytes(b"different")
                elif mode == "classification":
                    path = target / "seal.json"
                    data = json.loads(path.read_text())
                    data["provenance"] = "real"
                    path.write_text(json.dumps(data))
                else:
                    (target / "capture/live.jsonl").write_text("changed")
                with self.subTest(mode=mode), self.assertRaises(ValueError):
                    verify(target, True)

    def test_active_and_incomplete_are_not_success(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            write_fixture(root)
            status = root / "status.json"
            status.write_text('{"state":"running"}')
            with self.assertRaises(ValueError):
                seal(root, True)
            status.write_text('{"state":"failed"}')
            with self.assertRaises(ValueError):
                seal(root, True)
            result = seal(root, True, incomplete=True)
            self.assertEqual(result["validation"], "incomplete")
            self.assertEqual(verify(root, True)["validation"], "incomplete")
            self.assertIsNone(result["review"])

    def test_plan_mismatch_rejected_before_sealing(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            write_fixture(root)
            (root / "plan.json").write_text('{"altered":true}')
            with self.assertRaisesRegex(ValueError, "执行计划"):
                seal(root, True)


if __name__ == "__main__":
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
