"""验证离线执行计划，避免覆盖旧记录或监测目录外的文件。"""
import math
import datetime as dt
import io
import sys
from pathlib import Path

ENV_KEYS = {"TICKFLOW_GAP_" + key for key in
            ("INS", "DAYS", "START", "BEGIN", "DROP", "RESUME", "END", "OUT", "REAL")} | {"TICKFLOW_LIVE"}


def stamp(value):
    """暂停窗口必须有明确时区，避免按机器本地时区解释。"""
    if not isinstance(value, str):
        raise ValueError("暂停时刻必须是带时区的字符串")
    parsed = dt.datetime.fromisoformat(value)
    if parsed.utcoffset() is None:
        raise ValueError("暂停时刻缺少时区")
    return parsed.timestamp()


def quiet_files(plan, now):
    """只抑制指定文件在左闭右开窗口内的空闲告警。"""
    return [name for name, windows in plan.get("quiet_windows", {}).items()
            if any(stamp(item["start"]) <= now < stamp(item["end"]) for item in windows)]


def child_environment(plan, root, inherited):
    """凭证仅继承，不写入计划；OUT只能落在新任务的子目录。"""
    overrides = dict(plan.get("env", {}))
    environment = dict(inherited, **overrides)
    if "TICKFLOW_GAP_OUT" in environment:
        output = Path(environment["TICKFLOW_GAP_OUT"].replace("{task_dir}", str(root))).resolve()
        if output == root or not output.is_relative_to(root):
            raise ValueError("OUT必须是新任务目录的子目录")
        environment["TICKFLOW_GAP_OUT"] = str(output)
    return environment


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
    reserved = {"plan.json", "status.json", "status.tmp", "events.jsonl", "worker.log", "capture.log", "run.log"}
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
    environment = value.get("env", {})
    if not isinstance(environment, dict) or any(
            key not in ENV_KEYS or not isinstance(item, str) or not item or "\x00" in item
            for key, item in environment.items()):
        raise ValueError("env只接受白名单内非敏感字符串参数")
    result["env"] = dict(environment)
    log_name = value.get("stdout_name", "capture.log")
    if log_name not in ("capture.log", "run.log"):
        raise ValueError("stdout_name只允许capture.log或run.log")
    result["stdout_name"] = log_name
    windows = value.get("quiet_windows", {})
    if not isinstance(windows, dict):
        raise ValueError("quiet_windows必须按watch文件声明")
    result["quiet_windows"] = {}
    for name, entries in windows.items():
        if name not in watch or not isinstance(entries, list) or not entries:
            raise ValueError("暂停窗口必须属于watch通道且非空")
        previous_end = float("-inf")
        for item in entries:
            if not isinstance(item, dict) or set(item) != {"start", "end"}:
                raise ValueError("暂停窗口必须指定start和end")
            start, end = stamp(item["start"]), stamp(item["end"])
            if not math.isfinite(start) or not math.isfinite(end) or not previous_end <= start < end:
                raise ValueError("暂停窗口必须递增、不重叠且起止有效")
            previous_end = end
        result["quiet_windows"][name] = [dict(item) for item in entries]
    return result


if __name__ == "__main__":
    # 仅直接执行时配置异常输出；作为模块导入不改调用方的流。
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
