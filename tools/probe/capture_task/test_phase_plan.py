"""计划暂停、参数绑定与复核日志接入的离线正反验证。"""
import datetime as dt
import io
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import time
import unittest

from plan import validate, quiet_files, child_environment
from worker import run, inspect
from cli import launch


class PhasePlanTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.base = Path(self.temp.name).resolve()

    def plan(self, code="pass", **overrides):
        value = dict(command=[sys.executable, "-c", code, "{task_dir}"], cwd=str(self.base),
                     watch=["raw.jsonl"], max_seconds=4, startup_seconds=0.1,
                     idle_seconds=0.12, poll_seconds=0.02)
        value.update(overrides)
        return validate(value)

    def window(self, start, end):
        iso = lambda stamp: dt.datetime.fromtimestamp(stamp, dt.timezone.utc).isoformat()
        return {"start": iso(start), "end": iso(end)}

    def execute(self, plan):
        root = self.base / "task"
        root.mkdir()
        result = run(root, plan)
        events = [json.loads(line) for line in (root / "events.jsonl").read_text().splitlines()]
        return result, inspect(root), events

    def test_window_boundary_is_half_open(self):
        plan = self.plan(quiet_windows={"raw.jsonl": [self.window(100, 110)]})
        for moment, expected in [(99, []), (100, ["raw.jsonl"]), (109, ["raw.jsonl"]), (110, [])]:
            self.assertEqual(quiet_files(plan, moment), expected)

    def test_planned_pause_does_not_report_stall(self):
        now = time.time()
        code = "from pathlib import Path; import sys,time; p=Path(sys.argv[1])/'raw.jsonl'; p.write_text('first'); time.sleep(0.4); p.write_text('second')"
        rc, status, events = self.execute(self.plan(code, quiet_windows={"raw.jsonl": [self.window(now - 1, now + 3)]}))
        self.assertEqual(rc, 0)
        self.assertEqual(status["stalls"], 0)
        self.assertTrue(any(event["quiet_files"] == ["raw.jsonl"] for event in events))

    def test_other_channel_still_reports_missing(self):
        now = time.time()
        rc, status, events = self.execute(self.plan("import time; time.sleep(0.4)", watch=["raw.jsonl", "reference.jsonl"],
                                                   quiet_windows={"raw.jsonl": [self.window(now - 1, now + 3)]}))
        self.assertEqual(rc, 1)
        self.assertEqual(status["stale_files"], ["reference.jsonl"])
        self.assertTrue(any(event["state"] == "stalled" for event in events))

    def test_after_window_missing_data_stalls(self):
        now = time.time()
        rc, status, events = self.execute(self.plan("import time; time.sleep(0.7)",
                                                   quiet_windows={"raw.jsonl": [self.window(now - 1, now + 0.25)]}))
        self.assertEqual(rc, 1)
        self.assertGreater(status["stalls"], 0)
        self.assertTrue(any(event["stale_files"] == ["raw.jsonl"] and not event["quiet_files"] for event in events))

    def test_quiet_does_not_allow_missing_file_or_nonzero_exit(self):
        now = time.time()
        rc, status, _ = self.execute(self.plan("raise SystemExit(9)", quiet_windows={"raw.jsonl": [self.window(now - 1, now + 3)]}))
        self.assertEqual(rc, 1)
        self.assertEqual(status["state"], "failed")
        self.assertEqual(status["returncode"], 9)

    def test_rejects_invalid_windows_and_secret_parameters(self):
        invalid = [{"quiet_windows": {"not-watched": [self.window(1, 2)]}},
                   {"quiet_windows": {"raw.jsonl": [self.window(2, 1)]}},
                   {"quiet_windows": {"raw.jsonl": [self.window(1, 3), self.window(2, 4)]}},
                   {"quiet_windows": {"raw.jsonl": [{"start": "2026-10-24T10:00:00", "end": "2026-10-24T11:00:00"}]}},
                   {"env": {"SHINNY_PASS": "synthetic-secret"}}, {"env": {"PATH": "override"}},
                   {"env": {"TICKFLOW_GAP_REAL": 1}}, {"env": {"TICKFLOW_LIVE": ""}},
                   {"stdout_name": "../run.log"}, {"watch": ["run.log"]}]
        for item in invalid:
            with self.subTest(item=item), self.assertRaises(ValueError):
                self.plan(**item)

    def test_environment_is_frozen_without_copying_credentials(self):
        plan = self.plan(env={"TICKFLOW_GAP_INS": "SHFE.rb2701", "TICKFLOW_GAP_OUT": "{task_dir}/capture"})
        inherited = {"SHINNY_PASS": "synthetic-secret", "TICKFLOW_GAP_INS": "wrong-parent"}
        result = child_environment(plan, self.base, inherited)
        self.assertEqual(result["TICKFLOW_GAP_INS"], "SHFE.rb2701")
        self.assertEqual(result["SHINNY_PASS"], "synthetic-secret")
        self.assertEqual(Path(result["TICKFLOW_GAP_OUT"]), self.base / "capture")
        self.assertNotIn("synthetic-secret", json.dumps(plan))
        self.assertEqual(inherited["TICKFLOW_GAP_INS"], "wrong-parent")

    def test_outside_output_rejected_before_creating_directory(self):
        root = self.base / "task"
        plan = self.plan(env={"TICKFLOW_GAP_OUT": str(self.base / "old")})
        with self.assertRaises(ValueError):
            launch(root, plan)
        self.assertFalse(root.exists())
        with self.assertRaises(ValueError):
            child_environment(self.plan(), root, {"TICKFLOW_GAP_OUT": str(self.base / "old")})

    def test_detached_cli_uses_fixed_env_and_run_log(self):
        code = "from pathlib import Path; import os,sys; p=Path(os.environ['TICKFLOW_GAP_OUT']); p.mkdir(); (p/'raw.jsonl').write_text(os.environ['TICKFLOW_GAP_INS']); print('synthetic runner ended')"
        plan = self.plan(code, env={"TICKFLOW_GAP_INS": "SHFE.rb2701", "TICKFLOW_GAP_OUT": "{task_dir}/capture"},
                         watch=["capture/raw.jsonl"], stdout_name="run.log", startup_seconds=2)
        spec = self.base / "input.json"
        spec.write_text(json.dumps(plan), encoding="utf-8")
        root = self.base / "detached"
        cli = Path(__file__).with_name("cli.py")
        process = subprocess.run([sys.executable, str(cli), "launch", str(root), "--plan", str(spec)], capture_output=True, timeout=5)
        self.assertEqual(process.returncode, 0, process.stderr)
        deadline = time.monotonic() + 8
        while time.monotonic() < deadline:
            status = inspect(root)
            if status["state"] in {"completed", "failed", "timed_out"}:
                break
            time.sleep(0.04)
        self.assertEqual(status["state"], "completed", status)
        self.assertEqual((root / "capture/raw.jsonl").read_text(), "SHFE.rb2701")
        self.assertIn("synthetic runner ended", (root / "run.log").read_text())
        self.assertEqual(json.loads((root / "plan.json").read_text(encoding="utf-8"))["env"], plan["env"])


if __name__ == "__main__":
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
