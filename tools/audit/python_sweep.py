"""枚举并运行工具目录的 Python unittest，拒绝零测试和全部跳过。"""
import argparse
import io
import os
from pathlib import Path
import subprocess
import sys
import unittest

ROOT = Path(__file__).resolve().parents[2]
SKIP_DIRS = {".git", "__pycache__", "node_modules", ".venv", "venv"}


def directories():
    found = []
    for directory, dirs, files in os.walk(ROOT / "tools"):
        dirs[:] = [name for name in dirs if name not in SKIP_DIRS]
        if any(name.startswith("test_") and name.endswith(".py") for name in files):
            found.append(Path(directory))
    return sorted(found)


def worker(directory):
    # 每个目录在独立进程中加载，允许工具保留同目录 import。
    suite = unittest.defaultTestLoader.discover(str(directory), pattern="test_*.py")
    count = suite.countTestCases()
    if count == 0:
        print("FAIL：发现零项 Python 测试", file=sys.stderr)
        return 1
    result = unittest.TextTestRunner(verbosity=2).run(suite)
    if len(result.skipped) == count:
        print("FAIL：全部 Python 测试被跳过", file=sys.stderr)
        return 1
    return int(not result.wasSuccessful())


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--directory", type=Path, help="内部独立目录执行模式")
    args = parser.parse_args(argv)
    if args.directory:
        return worker(args.directory)
    paths = directories()
    if not paths:
        raise SystemExit("未枚举到 Python 测试目录")
    bad = False
    for directory in paths:
        print("Python 测试目录：" + directory.relative_to(ROOT).as_posix(), flush=True)
        p = subprocess.run([sys.executable, "-X", "utf8", str(Path(__file__).resolve()),
                            "--directory", str(directory)], cwd=ROOT, timeout=120)
        bad |= p.returncode != 0
    return int(bad)


if __name__ == "__main__":
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    raise SystemExit(main())
