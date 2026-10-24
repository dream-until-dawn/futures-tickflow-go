"""真实本地进程强杀与状态读取异常的演练，不操作其它进程。"""
import io
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import time
import unittest

from plan import validate
from worker import inspect


class CrashTest(unittest.TestCase):
    def test_missing_corrupt_and_future_heartbeat_are_unknown(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.assertEqual(inspect(root)["state"], "supervisor_unknown")
            for data in ("{", "[]", '{"state":"invented"}', '{"state":"running","heartbeat":true}',
                         json.dumps(dict(state="running", heartbeat=time.time() + 100))):
                (root / "status.json").write_text(data)
                self.assertEqual(inspect(root)["state"], "supervisor_unknown")

    def test_kill_owned_supervisor_and_child(self):
        for target in ("child", "supervisor"):
            with self.subTest(target=target), tempfile.TemporaryDirectory() as directory:
                root = Path(directory).resolve()
                code = "import pathlib,sys,time; p=pathlib.Path(sys.argv[1])/'raw.jsonl'; p.write_text('started'); time.sleep(30)"
                plan = validate(dict(command=[sys.executable, "-c", code, str(root)], cwd=str(root), watch=["raw.jsonl"],
                                     max_seconds=10, startup_seconds=2, idle_seconds=5, poll_seconds=0.03))
                (root / "plan.json").write_text(json.dumps(plan))
                child_pid = None
                with (root / "worker.log").open("wb") as output:
                    process = subprocess.Popen([sys.executable, str(Path(__file__).with_name("cli.py")), "worker", str(root)], stdout=output, stderr=output)
                    try:
                        deadline = time.monotonic() + 5
                        while time.monotonic() < deadline:
                            status = inspect(root)
                            child_pid = status.get("child_pid")
                            if child_pid and status.get("files"):
                                break
                            time.sleep(0.03)
                        self.assertIsNotNone(child_pid, status)
                        if target == "child":
                            os.kill(child_pid, signal.SIGTERM)
                            self.assertEqual(process.wait(timeout=5), 1)
                            self.assertEqual(inspect(root)["state"], "failed")
                        else:
                            process.kill()
                            process.wait(timeout=5)
                            time.sleep(0.2)
                            self.assertEqual(inspect(root, stale_seconds=0.1)["state"], "supervisor_unknown")
                    finally:
                        if process.poll() is None:
                            process.kill()
                            process.wait(timeout=5)
                        # 仅清理该夹具亲自创建且已记录的子进程，绝不按名称批量杀进程。
                        if child_pid and target == "supervisor":
                            try:
                                os.kill(child_pid, signal.SIGTERM)
                            except ProcessLookupError:
                                pass
                            time.sleep(0.1)


if __name__ == "__main__":
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
