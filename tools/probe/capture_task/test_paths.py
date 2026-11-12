"""跨平台重放Windows路径前缀反例，保留越界及设备路径拒绝。"""
import io
from pathlib import Path
import sys
import tempfile
import unittest

from paths import windows_key, resolved_within


class PathBoundaryTest(unittest.TestCase):
    def test_recorded_drive_aliases_and_case(self):
        root = windows_key(r'C:\Temp\task-12')
        child = windows_key(r'\\?\C:\Temp\task-12\capture\raw.jsonl')
        self.assertTrue(child.is_relative_to(root))
        self.assertTrue(windows_key(r'c:\temp\TASK-12\capture\raw.jsonl').is_relative_to(root))
        self.assertEqual(root, windows_key(r'\\?\C:\Temp\task-12'))

    def test_unc_aliases(self):
        root = windows_key(r'\\server\share\task')
        child = windows_key(r'\\?\UNC\server\share\task\raw.jsonl')
        self.assertTrue(child.is_relative_to(root))
        self.assertEqual(root, windows_key(r'\\?\UNC\server\share\task'))

    def test_other_roots_are_not_children(self):
        root = windows_key(r'C:\Temp\task')
        for value in [r'\\?\C:\Temp\task-other\raw.jsonl', r'D:\Temp\task\raw.jsonl',
                      r'\\server\share\task\raw.jsonl']:
            with self.subTest(value=value):
                self.assertFalse(windows_key(value).is_relative_to(root))

    def test_unknown_namespaces_fail_closed(self):
        for value in [r'\\.\C:\Temp\task', r'\\?\GLOBALROOT\Device\HarddiskVolume1\task',
                      r'\\?\Volume{123}\task', r'C:task', r'\Temp\task', r'\\?\C:\task\..\outside']:
            with self.subTest(value=value), self.assertRaises(ValueError):
                windows_key(value)

    def test_resolved_boundary_and_strict_child(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            self.assertTrue(resolved_within(root / 'missing/raw.jsonl', root))
            self.assertTrue(resolved_within(root, root))
            self.assertFalse(resolved_within(root, root, strict_child=True))
            self.assertFalse(resolved_within(root.parent / 'outside', root))
            # 同名路径前缀不是目录边界。
            self.assertFalse(resolved_within(root.parent / (root.name + '-other/raw.jsonl'), root))


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
