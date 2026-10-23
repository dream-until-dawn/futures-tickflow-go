"""用独立临时测试目录校验自检门禁的退出码，不修改仓库载体。"""
import io
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

import python_sweep


class PythonSweepTest(unittest.TestCase):
    def test_directory_discovery_is_not_a_fixed_list(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            for name in ("audit", "probe", "new/nested", "node_modules/ignored"):
                directory = root / "tools" / name
                directory.mkdir(parents=True)
                (directory / "test_fixture.py").write_text("", encoding="utf-8")
            (root / "tools" / "old").mkdir()
            (root / "tools" / "old" / "rebuild_docs_test.py").write_text("", encoding="utf-8")
            with mock.patch.object(python_sweep, "ROOT", root):
                actual = [p.relative_to(root).as_posix() for p in python_sweep.directories()]
            self.assertEqual(actual, ["tools/audit", "tools/new/nested", "tools/probe"])

    def test_worker_exit_codes(self):
        cases = [
            ("pass", "import unittest\nclass T(unittest.TestCase):\n def test_ok(self): self.assertEqual(2+2,4)\n", 0),
            ("fail", "import unittest\nclass T(unittest.TestCase):\n def test_bad(self): self.assertEqual(2+2,5)\n", 1),
            ("empty", "# 没有测试\n", 1),
            ("skip", "import unittest\nclass T(unittest.TestCase):\n @unittest.skip('fixture')\n def test_skip(self): pass\n", 1),
        ]
        for name, source, expected in cases:
            with self.subTest(name=name), tempfile.TemporaryDirectory() as tmp:
                Path(tmp, "test_fixture.py").write_text(source, encoding="utf-8")
                p = subprocess.run([sys.executable, "-X", "utf8", str(Path(__file__).with_name("python_sweep.py")),
                                    "--directory", tmp], capture_output=True, timeout=30)
                self.assertEqual(p.returncode, expected, p.stdout + p.stderr)


if __name__ == "__main__":
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
