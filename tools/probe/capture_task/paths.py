"""统一文件系统命名空间后比较边界；不以去掉前缀的方式放宽设备路径。"""
import io
import os
from pathlib import Path, PureWindowsPath
import re
import sys


def windows_key(value):
    """把普通盘符/UNC转入扩展文件命名空间；未知设备命名空间拒绝。"""
    text = str(value).replace('/', '\\')
    if '..' in PureWindowsPath(text).parts:
        raise ValueError('比较路径仍含未消解的父目录')
    if text.startswith('\\\\?\\'):
        if not re.match(r'^\\\\\?\\(?:[A-Za-z]:\\|UNC\\[^\\]+\\[^\\]+(?:\\|$))', text, re.I):
            raise ValueError('不支持的Windows设备路径')
        return PureWindowsPath(text)
    if text.startswith('\\\\.\\'):
        raise ValueError('不支持的Windows设备路径')
    if re.match(r'^[A-Za-z]:\\', text):
        return PureWindowsPath('\\\\?\\' + text)
    if re.match(r'^\\\\[^\\]+\\[^\\]+(?:\\|$)', text):
        return PureWindowsPath('\\\\?\\UNC\\' + text[2:])
    raise ValueError('Windows文件路径必须是绝对盘符或UNC路径')


def resolved_within(candidate, root, *, strict_child=False):
    """resolve仍负责解析符号链接；只统一其Windows返回值的合法文件命名空间。"""
    target, base = candidate.resolve(), root.resolve()
    if os.name == 'nt':
        target, base = windows_key(target), windows_key(base)
    return target.is_relative_to(base) and (not strict_child or target != base)


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
