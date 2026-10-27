"""测试收尾等待实际进程退出；终态快照不代表解释器已释放文件句柄。"""
import io
import os
import sys


def wait_for_exit(pid, timeout=10):
    """只等待，不终止进程；超时或权限错误必须暴露给测试。"""
    if type(pid) is not int or pid <= 0 or not 0 < timeout < float("inf"):
        raise ValueError("PID和超时必须为有限正数")
    if os.name == "nt":
        import ctypes
        from ctypes import wintypes
        kernel = ctypes.WinDLL("kernel32", use_last_error=True)
        kernel.OpenProcess.argtypes = [wintypes.DWORD, wintypes.BOOL, wintypes.DWORD]
        kernel.OpenProcess.restype = wintypes.HANDLE
        kernel.WaitForSingleObject.argtypes = [wintypes.HANDLE, wintypes.DWORD]
        kernel.WaitForSingleObject.restype = wintypes.DWORD
        kernel.CloseHandle.argtypes = [wintypes.HANDLE]
        kernel.CloseHandle.restype = wintypes.BOOL
        handle = kernel.OpenProcess(0x00100000, False, pid)
        if not handle:
            error = ctypes.get_last_error()
            if error == 87:  # 已退出的PID不再有可打开的进程对象。
                return
            raise ctypes.WinError(error)
        try:
            result = kernel.WaitForSingleObject(handle, max(1, int(timeout*1000)))
            if result == 258:
                raise TimeoutError("测试进程尚未实际退出")
            if result != 0:
                raise ctypes.WinError(ctypes.get_last_error())
        finally:
            kernel.CloseHandle(handle)
    else:
        import select
        try:
            descriptor = os.pidfd_open(pid)
        except ProcessLookupError:
            return
        try:
            ready, _, _ = select.select([descriptor], [], [], timeout)
            if not ready:
                raise TimeoutError("测试进程尚未实际退出")
        finally:
            os.close(descriptor)


if __name__ == "__main__":
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
