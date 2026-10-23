"""独立复核日盘长缺口封存资料；只使用 Python 标准库。"""
import argparse
import datetime as dt
import hashlib
import io
import json
import math
from pathlib import Path
import sys

FIELDS = (("o", "Open", "open"), ("h", "High", "high"), ("l", "Low", "low"),
          ("c", "Close", "close"), ("v", "Volume", "volume"), ("oi", "CloseOI", "close_oi"))


def require(condition, message):
    if not condition:
        raise ValueError(message)


def millis(value):
    parsed = dt.datetime.fromisoformat(value)
    require(parsed.utcoffset() == dt.timedelta(hours=8), "执行单须明确东八区")
    return int(parsed.timestamp() * 1000)


def verify(env, events, raw, history, live):
    start, end = millis(env["START"]), millis(env["END"])
    begin, drop, resume = (millis(env[k]) for k in ("BEGIN", "DROP", "RESUME"))
    require(all(value % 60000 == 0 for value in (start, end, begin, drop, resume)), "执行单未按分钟对齐")
    require(start <= begin < drop < resume < end, "执行单时序错误")
    require(env["START"][:10] == env["END"][:10], "此复核器仅支持同日连续日盘")
    expected = list(range(start, end, 60000))
    day = int(env["END"][:10].replace("-", ""))
    require(str(day) in env["DAYS"].split(","), "执行单日历没有目标日")
    require(len(history) == len(live) == len(expected) > 0, "历史或 Live 数量不足")
    controls = {}
    for event in events:
        name = event["event"]
        require(name not in controls, "重复控制事件")
        controls[name] = event["when_ms"]
    closed, restored, stopped = (controls[k] for k in ("socket_closed", "resume_next", "capture_end"))
    require(drop <= closed <= drop + 5000 and resume <= restored <= resume + 5000, "控制事件偏离执行单")
    require(stopped >= end + 9000, "末根观察提前结束")
    archived, seen_in_minute = {}, set()
    previous_recv = 0
    for record in raw:
        recv = record["recv_ms"]
        require(record["conn"] == 1 and recv >= previous_recv and recv >= begin, "参照重连或接收时间倒退")
        require(recv <= stopped, "参照帧晚于录制结束")
        previous_recv = recv
        payload = record["raw"]
        require(isinstance(payload, dict), "原始帧载荷无效")
        if payload.get("aid") != "rtn_data":
            continue
        for diff in payload.get("data", []):
            data = diff.get("klines", {}).get(env["INS"], {}).get("60000000000", {}).get("data", {})
            for key, fields in data.items():
                if fields is None:
                    continue  # 窗口滚动移除不抹掉已观测的历史值。
                item = archived.setdefault(int(key), {})
                if "datetime" in item and "datetime" in fields:
                    require(item["datetime"] == fields["datetime"], "同一参照编号的时间标签改变")
                item.update(fields)
                if "datetime" in item:
                    stamp = item["datetime"] // 1000000
                    if stamp <= recv < stamp + 60000:
                        seen_in_minute.add(stamp)
    whole_gap = []
    for i, (hist, output) in enumerate(zip(history, live)):
        ts = expected[i]
        require(hist["Datetime"] == ts * 1000000 and output["ts"] == ts, "时间标签缺失、重复或乱序")
        require(output["day"] == day, "Live 交易日错误")
        require(i == 0 or hist["ID"] == history[i - 1]["ID"] + 1, "历史编号不连续")
        reference = archived.get(hist["ID"], {})
        require(reference.get("datetime") == hist["Datetime"], "参照缺根或编号不一致")
        for live_key, hist_key, raw_key in FIELDS:
            values = (output[live_key], hist[hist_key], reference.get(raw_key))
            require(all(type(v) in (float, int) and math.isfinite(v) for v in values), "价格或数量字段缺失/非有限")
            require(values[0] == values[1] == values[2], "O/H/L/C/V/OI 通道不一致")
        require(not closed < output["when_ms"] < restored, "暂停期仍交出数据")
        if ts >= closed and ts + 60000 <= restored:
            require(ts in seen_in_minute, "缺口根未在参照盘中实际出现")
            require(output["when_ms"] >= restored, "缺口根早于恢复交出")
            whole_gap.append(ts)
    require(len(whole_gap) >= 31, "实际暂停未覆盖 31 根完整交易分钟")
    require(any(o["when_ms"] < closed for o in live) and any(o["when_ms"] > restored for o in live), "缺少暂停前或恢复后输出")
    return {"rows": len(expected), "complete_gap_rows": len(whole_gap),
            "closed_ms": closed, "restored_ms": restored, "reference_frames": len(raw)}


def reject_constant(value):
    raise ValueError("JSON 中包含非标准常量：" + value)


def read_lines(data):
    return [json.loads(line, parse_constant=reject_constant)
            for line in data.decode("utf-8-sig").splitlines() if line.strip()]


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("run_root", type=Path)
    args = parser.parse_args(argv)
    root = args.run_root
    names = ["reference.jsonl", "history.jsonl", "live.jsonl", "events.jsonl"]
    contents = {name: (root / "capture" / name).read_bytes() for name in names}
    before = {name: hashlib.sha256(data).hexdigest() for name, data in contents.items()}
    manifest = json.loads((root / "manifest.json").read_text(encoding="utf-8-sig"), parse_constant=reject_constant)
    log_bytes = (root / "run.log").read_bytes()
    log = log_bytes.decode("utf-8-sig")
    require("--- PASS: TestLiveLongGapReal" in log and "--- FAIL:" not in log,
            "运行器尚未成功结束，不能封存通过结论")
    events = read_lines(contents["events.jsonl"])
    require(events and manifest["contract"] == events[0]["INS"], "执行单与采集配置不一致")
    for key in ("begin", "drop", "resume", "end"):
        require(manifest[key] == events[0][key.upper()], "执行单与采集时刻不一致")
    require(isinstance(manifest["sha"], str) and len(manifest["sha"]) == 40
            and all(c in "0123456789abcdef" for c in manifest["sha"]), "代码 SHA 无效")
    result = verify(events[0], events[1:], read_lines(contents["reference.jsonl"]),
                    read_lines(contents["history.jsonl"]), read_lines(contents["live.jsonl"]))
    after = {name: hashlib.sha256((root / "capture" / name).read_bytes()).hexdigest() for name in names}
    require(before == after, "分析期间原始文件改变")
    require(log_bytes == (root / "run.log").read_bytes(), "分析期间运行日志改变")
    result.update(source_sha=manifest["sha"], sha256_before=before, sha256_after=after)
    print(json.dumps(result, ensure_ascii=False, indent=2))
    return 0


if __name__ == "__main__":
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    raise SystemExit(main())
