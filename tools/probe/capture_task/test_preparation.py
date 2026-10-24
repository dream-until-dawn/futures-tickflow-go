"""执行单、编译产物绑定与错误模式的离线测试。"""
import copy
import hashlib
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

from artifact import check, stage, preflight, synthetic_spec
from prepare import make_plan, prepare
from plan import validate
from cli import launch


class PreparationTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.base = Path(self.temp.name).resolve()
        self.binary = self.base / "capture.test"
        self.binary.write_bytes(b"unit-only-not-executable")
        self.plan = make_plan(self.binary, "a" * 40, "synthetic", synthetic_spec(), 1)

    def test_stage_binds_manifest_and_copies_bytes(self):
        root = self.base / "task"
        root.mkdir()
        data = check(self.plan)
        stage(root, self.plan, data)
        manifest = json.loads((root / "manifest.json").read_text(encoding="utf-8"))
        self.assertEqual(manifest["provenance"], "synthetic")
        self.assertEqual(manifest["sha"], "a" * 40)
        self.assertEqual(manifest["contract"], "SHFE.rb2610")
        self.assertEqual(manifest["binary_sha256"], hashlib.sha256(data).hexdigest())
        self.assertEqual((root / "capture.test").read_bytes(), data)

    def test_tampered_binary_rejected_before_task_creation(self):
        self.binary.write_bytes(b"tampered")
        root = self.base / "task"
        with self.assertRaisesRegex(ValueError, "哈希"):
            launch(root, self.plan)
        self.assertFalse(root.exists())

    def test_parameter_command_and_classification_tampering_rejected(self):
        for kind in ("env", "command", "classification", "sha", "spec"):
            plan = copy.deepcopy(self.plan)
            if kind == "env":
                plan["env"]["TICKFLOW_GAP_INS"] = "SHFE.rb2701"
            elif kind == "command":
                plan["command"][1] = "-test.run=NoSuchTest"
            elif kind == "classification":
                plan["artifact"]["mode"] = "unknown"
            elif kind == "sha":
                plan["artifact"]["source_sha"] = "not-a-sha"
            else:
                plan["artifact"]["spec"]["SHINNY_PASS"] = "synthetic-secret"
            with self.subTest(kind=kind), self.assertRaises(ValueError):
                validate(plan)

    def test_zero_or_skipped_preflight_does_not_pass(self):
        for output in (b"PASS\n[no tests to run]", b"--- SKIP: TestGapPlanOfflinePreflight\nPASS"):
            result = subprocess.CompletedProcess([], 0, output, b"")
            with mock.patch("artifact.subprocess.run", return_value=result), self.assertRaises(ValueError):
                preflight(self.binary, synthetic_spec())

    def test_expired_or_insufficient_deadline_rejected(self):
        for now, end in ((10**12, "2026-09-04T10:00:00+08:00"), (1, "2026-09-04T09:40:00+08:00")):
            spec = dict(synthetic_spec(), DEADLINE=end)
            with self.assertRaises(ValueError):
                make_plan(self.binary, "a" * 40, "real", spec, now)

    def test_dirty_source_rejected_without_output(self):
        root = self.base / "prepared"
        with mock.patch("prepare.git", side_effect=["a" * 40, " M dirty.go"]), self.assertRaises(ValueError):
            prepare(root, "synthetic", synthetic_spec())
        self.assertFalse(root.exists())

    def test_real_launch_calls_go_preflight_again(self):
        spec = synthetic_spec()
        plan = make_plan(self.binary, "a" * 40, "real", spec, 1)
        with mock.patch("artifact.preflight", side_effect=ValueError("过期")) as guard:
            root = self.base / "task"
            with self.assertRaisesRegex(ValueError, "过期"):
                launch(root, plan)
            guard.assert_called_once()
            self.assertFalse(root.exists())

    def test_process_scope_is_explicit_and_real_cannot_use_job(self):
        for scope in ("independent", "job"):
            plan = validate(dict(self.plan, process_scope=scope))
            root = self.base / scope
            with mock.patch("cli.subprocess.Popen", return_value=mock.Mock(pid=123)) as spawn:
                result = launch(root, plan)
            self.assertEqual(result["process_scope"], scope)
            options = spawn.call_args.kwargs
            if os.name == "nt":
                self.assertEqual(bool(options["creationflags"] & subprocess.CREATE_BREAKAWAY_FROM_JOB), scope == "independent")
            else:
                self.assertEqual(options["start_new_session"], scope == "independent")
            self.assertEqual(json.loads((root / "manifest.json").read_text())["process_scope"], scope)
        with self.assertRaises(ValueError):
            make_plan(self.binary, "a" * 40, "real", synthetic_spec(), 1, "job")

    def test_independent_spawn_denial_never_silently_falls_back(self):
        root = self.base / "denied"
        with mock.patch("cli.subprocess.Popen", side_effect=PermissionError()) as spawn:
            with self.assertRaises(PermissionError):
                launch(root, self.plan)
            self.assertEqual(spawn.call_count, 1)
        self.assertEqual(json.loads((root / "status.json").read_text())["state"], "failed")


if __name__ == "__main__":
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
