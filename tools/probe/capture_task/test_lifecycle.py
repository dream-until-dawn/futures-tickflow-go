"""仅使用本地子进程的生命周期正反测试，不连接行情服务。"""
import json
import io
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import time
import unittest
from unittest import mock

from plan import validate
from worker import inspect, run, signatures, write_status
from cli import launch

CLI = Path(__file__).with_name("cli.py")
WRITE = "from pathlib import Path; import sys,time; p=Path(sys.argv[1])/'raw.jsonl'; "


class LifecycleTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.base = Path(self.temp.name).resolve()

    def plan(self, code, **overrides):
        value = dict(command=[sys.executable, "-c", code, "{task_dir}"], cwd=str(self.base),
                     watch=["raw.jsonl"], max_seconds=6, startup_seconds=2,
                     idle_seconds=0.3, poll_seconds=0.03,
                     process_scope=os.environ.get("TICKFLOW_TEST_PROCESS_SCOPE", "independent"))
        value.update(overrides)
        return validate(value)

    def execute(self, code, **overrides):
        root = self.base / "task"
        root.mkdir()
        rc = run(root, self.plan(code, **overrides))
        status = inspect(root)
        events = [json.loads(line)["state"] for line in (root / "events.jsonl").read_text(encoding="utf-8").splitlines()]
        return rc, status, events

    def test_write_then_success_and_nonzero(self):
        rc, status, _ = self.execute(WRITE + "p.write_text('real fixture'); time.sleep(0.12)")
        self.assertEqual(rc, 0)
        self.assertEqual(status["state"], "completed")
        self.assertEqual(status["returncode"], 0)
        self.assertIsNotNone(status["last_data_at"])

    def test_nonzero_preserves_log(self):
        rc, status, _ = self.execute(WRITE + "p.write_text('partial'); print('failure fixture'); sys.exit(7)")
        self.assertEqual(rc, 1)
        self.assertEqual(status["state"], "failed")
        self.assertEqual(status["returncode"], 7)
        self.assertIn("failure fixture", (self.base / "task" / "capture.log").read_text())

    def test_zero_without_data_is_failure(self):
        rc, status, _ = self.execute("pass")
        self.assertEqual(rc, 1)
        self.assertEqual(status["state"], "failed")
        self.assertEqual(status["returncode"], 0)

    def test_active_channel_does_not_hide_missing_channel(self):
        rc, status, events = self.execute(WRITE + "\nfor i in range(12):\n p.write_text(str(i)); time.sleep(0.06)\n",
                                         watch=["raw.jsonl", "missing.jsonl"], startup_seconds=0.1)
        self.assertEqual(rc, 1)
        self.assertIn("stalled", events)
        self.assertEqual(status["stale_files"], ["missing.jsonl"])
        self.assertEqual(status["state"], "failed")

    def test_status_replace_retries_sharing_conflict_but_is_bounded(self):
        import os
        real_replace = os.replace
        with mock.patch("worker.os.replace", side_effect=[PermissionError(), PermissionError(), None]) as replace:
            write_status(self.base, {"state": "running"})
            self.assertEqual(replace.call_count, 3)
        # 模拟持续被占用不能悄悄通过或无限等待。
        with mock.patch("worker.os.replace", side_effect=PermissionError()), mock.patch("worker.time.sleep"):
            with self.assertRaises(PermissionError):
                write_status(self.base, {"state": "running"})
        real_replace(self.base / "status.tmp", self.base / "status.json")

    def test_stall_and_recovery(self):
        rc, status, events = self.execute(WRITE + "p.write_text('first'); time.sleep(1); p.write_text('second'); time.sleep(0.15)")
        self.assertEqual(rc, 0)
        self.assertIn("stalled", events)
        index = events.index("stalled")
        self.assertIn("running", events[index + 1:])
        self.assertEqual(status["stalls"], 1)

    def test_no_output_stalls_then_times_out(self):
        rc, status, events = self.execute("import time; time.sleep(30)", max_seconds=0.8, startup_seconds=0.1)
        self.assertEqual(rc, 1)
        self.assertIn("stalled", events)
        self.assertEqual(status["state"], "timed_out")
        self.assertIsNotNone(status["returncode"])

    def test_spawn_failure_and_no_secret_echo(self):
        root = self.base / "task"
        root.mkdir()
        plan = self.plan("pass", command=[str(self.base / "missing-secret-executable")])
        self.assertEqual(run(root, plan), 1)
        status = inspect(root)
        self.assertEqual(status["state"], "failed")
        self.assertNotIn("secret", json.dumps(status))

    def test_stale_heartbeat_and_terminal_state(self):
        root = self.base / "task"
        root.mkdir()
        path = root / "status.json"
        for state in ("waiting", "running", "launching", "stalled"):
            path.write_text(json.dumps(dict(state=state, heartbeat=time.time() - 100)))
            self.assertEqual(inspect(root)["state"], "supervisor_unknown")
        path.write_text(json.dumps(dict(state="failed", heartbeat=0)))
        self.assertEqual(inspect(root)["state"], "failed")

    def test_rejects_bad_plan(self):
        for key, value in [("watch", ["../old.jsonl"]), ("watch", ["."]),
                           ("watch", ["status.tmp"]), ("watch", ["STATUS.JSON"]),
                           ("watch", [str(self.base / "outside")]),
                           ("command", "python"), ("cwd", "relative"),
                           ("idle_seconds", True), ("max_seconds", float("inf")),
                           ("poll_seconds", 4), ("startup_seconds", 0)]:
            with self.subTest(key=key, value=value), self.assertRaises(ValueError):
                self.plan("pass", **{key: value})

    def test_empty_file_and_escaping_link_are_not_activity(self):
        (self.base / "empty").touch()
        self.assertEqual(signatures(self.base, ["empty"]), {})
        with mock.patch.object(Path, "resolve", return_value=self.base.parent):
            with self.assertRaises(ValueError):
                signatures(self.base, ["escape"])

    def test_existing_directory_is_preserved(self):
        root = self.base / "task"
        root.mkdir()
        sentinel = root / "old.jsonl"
        sentinel.write_text("immutable")
        with self.assertRaises(FileExistsError):
            launch(root, self.plan("pass"))
        self.assertEqual(sentinel.read_text(), "immutable")
        self.assertEqual(list(root.iterdir()), [sentinel])

    def test_detached_worker_survives_launcher_exit(self):
        # launch 命令退出之后，监护和采集仍继续；不能仅断言 Popen 成功。
        root = self.base / "detached"
        plan = self.plan(WRITE + "\nfor i in range(16):\n p.write_text(str(i)); time.sleep(0.1)\n")
        spec = self.base / "input.json"
        spec.write_text(json.dumps(plan), encoding="utf-8")
        launcher = subprocess.run([sys.executable, str(CLI), "launch", str(root), "--plan", str(spec)],
                                  capture_output=True, timeout=5)
        self.assertEqual(launcher.returncode, 0, launcher.stderr)
        self.assertEqual(json.loads(launcher.stdout)["state"], "launched")
        self.assertEqual(json.loads(launcher.stdout)["process_scope"], plan["process_scope"])
        observed_after_exit = []
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline:
            status = inspect(root)
            data = root / "raw.jsonl"
            if data.exists():
                observed_after_exit.append(data.read_text())
            if status["state"] in {"completed", "failed", "timed_out"}:
                break
            time.sleep(0.06)
        self.assertEqual(status["state"], "completed", (status,
                         (root / "worker.log").read_text(errors="replace"),
                         (root / "capture.log").read_text(errors="replace")))
        self.assertGreaterEqual(len(set(observed_after_exit)), 2)
        self.assertEqual(data.read_text(), "15")
        self.assertTrue((root / "capture.log").exists())


if __name__ == "__main__":
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
