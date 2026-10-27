"""实际文件占用进程标定等待器，不能把终态标志当作退出。"""
import io
from pathlib import Path
import subprocess
import sys
import tempfile
import time
import unittest

from process_wait import wait_for_exit


class ProcessWaitTest(unittest.TestCase):
    def test_waits_for_file_owner_and_timeout_does_not_kill(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp)/"worker.log"
            release = Path(tmp)/"release"
            # 文件已出现相当于已发布终态，但子进程仍有活动句柄。
            process = subprocess.Popen([sys.executable, "-c",
                                       "from pathlib import Path; import sys,time; f=open(sys.argv[1],'w'); f.write('completed'); f.flush();\n"
                                       "while not Path(sys.argv[2]).exists(): time.sleep(0.005)\n"
                                       "f.close()",
                                       str(path), str(release)], stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            try:
                deadline = time.monotonic()+5
                while (not path.exists() or not path.stat().st_size) and time.monotonic() < deadline:
                    time.sleep(0.005)
                self.assertTrue(path.exists())
                with self.assertRaises(TimeoutError):
                    wait_for_exit(process.pid, 0.01)
                self.assertIsNone(process.poll())
                release.touch()
                wait_for_exit(process.pid, 5)
                process.wait(timeout=1)
                path.unlink()  # Windows仍占用时这里必然失败。
                wait_for_exit(process.pid, 1)  # 已退出的PID同样可收尾。
            finally:
                if process.poll() is None:
                    process.kill()
                process.wait(timeout=5)

    def test_invalid_identity_and_timeout_rejected(self):
        for pid, timeout in ((0, 1), (-1, 1), (True, 1), (1, 0), (1, float("inf"))):
            with self.subTest(pid=pid, timeout=timeout), self.assertRaises(ValueError):
                wait_for_exit(pid, timeout)


if __name__ == "__main__":
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
