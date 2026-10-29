"""停夜盘执行单独立校验；合成示例不能产出真实执行命令。"""
import datetime as dt
import hashlib
import io
import json
from pathlib import Path
import re
import sys
from urllib.parse import urlsplit

FIELDS = {"schema", "symbol", "trading_days", "no_night_after", "resume_day", "evening_begin", "evening_end",
          "fetch_begin", "fetch_end", "official_url", "evidence_file", "evidence_sha256", "evidence_class"}


def validate(path, allow_synthetic=False):
    """日期和区间由同一计划派生；公告文件仅验证绑定，不宣称自动判读公告。"""
    path = Path(path).resolve()
    plan = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(plan, dict) or set(plan) != FIELDS or type(plan["schema"]) is not int or plan["schema"] != 1:
        raise ValueError("停夜盘计划字段或版本错误")
    if not isinstance(plan["symbol"], str) or not re.fullmatch(r"SHFE\.rb[0-9]{2}(0[1-9]|1[0-2])", plan["symbol"]):
        raise ValueError("须选具体螺纹钢合约")
    days = plan["trading_days"]
    def day(value):
        if type(value) is not int or not re.fullmatch(r"[0-9]{8}", str(value)):
            raise ValueError("交易日须八位整数")
        return dt.datetime.strptime(str(value), "%Y%m%d").date()
    if not isinstance(days, list) or len(days) != 4:
        raise ValueError("须给节前相邻两日及节后相邻两日")
    dates = [day(value) for value in days]
    if dates != sorted(set(dates)) or any(d.weekday() >= 5 for d in dates):
        raise ValueError("交易日须递增、唯一且非周末")
    if day(plan["no_night_after"]) != dates[1] or day(plan["resume_day"]) != dates[2]:
        raise ValueError("公告停夜日与恢复日须绑定日历中间两日")
    points = {}
    for key in ("evening_begin", "evening_end", "fetch_begin", "fetch_end"):
        if not isinstance(plan[key], str) or not plan[key].endswith("+08:00"):
            raise ValueError("窗口须显式+08:00")
        point = dt.datetime.fromisoformat(plan[key])
        if point.second or point.microsecond:
            raise ValueError("窗口须分钟对齐")
        points[key] = point
    eve = dt.datetime.combine(dates[1], dt.time(20, 59), dt.timezone(dt.timedelta(hours=8)))
    fetch = dt.datetime.combine(dates[2], dt.time(15, 10), dt.timezone(dt.timedelta(hours=8)))
    if points != dict(evening_begin=eve, evening_end=eve+dt.timedelta(minutes=11),
                      fetch_begin=fetch, fetch_end=fetch+dt.timedelta(minutes=5)):
        raise ValueError("窗口须为停夜日20:59—21:10、恢复日15:10—15:15")
    if plan["evidence_class"] not in ("official", "synthetic") or (plan["evidence_class"] == "synthetic" and not allow_synthetic):
        raise ValueError("合成公告不得用于正式执行")
    url = urlsplit(plan["official_url"])
    if url.scheme != "https" or url.hostname not in ("www.shfe.com.cn", "www.shfe.cn") or url.username or url.password or url.port:
        raise ValueError("公告须为上期所HTTPS链接")
    if not isinstance(plan["evidence_file"], str) or not plan["evidence_file"]:
        raise ValueError("缺少公告文件")
    evidence = path.parent/plan["evidence_file"]
    if not evidence.is_file() or not re.fullmatch(r"[0-9a-f]{64}", plan["evidence_sha256"]) or hashlib.sha256(evidence.read_bytes()).hexdigest() != plan["evidence_sha256"]:
        raise ValueError("公告文件哈希不符")
    return plan


if __name__ == "__main__":
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    raise SystemExit("请使用nonight_preflight.py校验执行单；本模块不启动任务")
