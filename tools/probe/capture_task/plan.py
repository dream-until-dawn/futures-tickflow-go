"""验证离线执行计划，避免覆盖旧记录或监测目录外的文件。"""
import math
from pathlib import Path


def validate(value):
    """返回规范计划；所有时限必须有限且严格为正。"""
    if not isinstance(value, dict):
        raise ValueError("计划必须是对象")
    command = value.get("command")
    if not isinstance(command, list) or not command or any(
            not isinstance(arg, str) or not arg or "\x00" in arg for arg in command):
        raise ValueError("command 必须是非空参数数组")
    cwd = value.get("cwd")
    if not isinstance(cwd, str) or not Path(cwd).is_absolute() or not Path(cwd).is_dir():
        raise ValueError("cwd 必须是已存在的绝对目录")
    watch = value.get("watch")
    if not isinstance(watch, list) or not watch:
        raise ValueError("必须指定 watch 数据文件")
    reserved = {"plan.json", "status.json", "status.tmp", "events.jsonl", "worker.log", "capture.log"}
    for name in watch:
        if (not isinstance(name, str) or not name or not Path(name).parts or Path(name).drive or Path(name).is_absolute()
                or ".." in Path(name).parts or Path(name).parts[0].casefold() in reserved):
            raise ValueError("watch 必须是任务目录内非监护文件的相对路径")
    result = dict(command=command, cwd=cwd, watch=watch)
    for key in ("max_seconds", "startup_seconds", "idle_seconds", "poll_seconds"):
        number = value.get(key)
        if isinstance(number, bool) or not isinstance(number, (int, float)) or not math.isfinite(number) or number <= 0:
            raise ValueError(key + " 必须是有限正数")
        result[key] = number
    if result["poll_seconds"] > min(result["idle_seconds"], result["startup_seconds"]):
        raise ValueError("轮询间隔不得大于启动或空闲阈值")
    return result
