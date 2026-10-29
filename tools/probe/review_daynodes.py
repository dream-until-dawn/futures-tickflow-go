"""独立重放L9原始取证；不导入Go解析器、不选择225/345告警规则。"""
import argparse
import base64
import datetime as dt
import hashlib
import io
import json
import math
from pathlib import Path
import sys

L9_SYMBOLS = {"KQ.m@SHFE.rb", "KQ.m@GFEX.si", "KQ.m@SHFE.ag", "SHFE.rb1605", "KQ.m@CFFEX.IF"}


def require(condition, reason):
    if not condition:
        raise ValueError(reason)


def integer(value, limit=9007199254740991):
    require(type(value) in (int, float) and math.isfinite(value) and value == int(value) and abs(value) <= limit, "id或标签不是可解释整数")
    return int(value)


def unique_object(items):
    result = {}
    for key, value in items:
        require(key not in result, "JSON重复键")
        result[key] = value
    return result


def decode(data):
    def bad_constant(value): raise ValueError("JSON非有限值："+value)
    return json.loads(data, object_pairs_hook=unique_object, parse_constant=bad_constant)


def merge(target, patch):
    require(isinstance(patch, dict), "DIFF须为对象")
    for key, value in patch.items():
        if value is None:
            target.pop(key, None)
        elif isinstance(value, dict):
            if not isinstance(target.get(key), dict): target[key] = {}
            merge(target[key], value)
        else:
            target[key] = value


def timestamp(row):
    require(isinstance(row, dict), "缺少首末根")
    value = integer(row.get("datetime"), 9223372036854775807)
    require(value > 0 and value % 60000000000 == 0, "分钟标签无效")
    return value


def review(root, require_l9=True):
    root = Path(root)
    metadata = decode((root/"metadata.json").read_bytes())
    summary = decode((root/"summary.json").read_bytes())
    require(metadata["schema"] == summary["schema"] == "day-nodes-v1", "取证版本不符")
    require(metadata["classification"] == summary["classification"] == "node-collection-only", "取证分类不符")
    require(summary["state"] == "completed" and summary["reason"] == "complete_nodes", "失败或未完成采样不能通过")
    timeout = metadata["timeout_seconds"]
    require(type(timeout) in (int, float) and math.isfinite(timeout) and 0 < timeout <= 90, "采样期限无效")
    started = dt.datetime.fromisoformat(metadata["started_at"])
    ended = dt.datetime.fromisoformat(summary["ended_at"])
    require(started.utcoffset() is not None and ended.utcoffset() is not None, "起止缺时区")
    requests = metadata["requests"]
    require(isinstance(requests, list) and len(requests) > 0, "空请求")
    symbols = [r["symbol"] for r in requests]
    require(all(isinstance(s, str) and s for s in symbols) and len(set(symbols)) == len(symbols), "合约重复或无效")
    if require_l9: require(set(symbols) == L9_SYMBOLS, "L9五路对照不完整")
    for index, request in enumerate(requests):
        require(request["chart"] == "t"+str(index) and type(request["need_span"]) is bool and
                request["need_span"] == (request["symbol"] != "SHFE.rb1605"), "图表或字段要求被篡改")
    raw = (root/"frames.jsonl").read_bytes()
    require(hashlib.sha256(raw).hexdigest() == summary["frames_sha256"], "原始帧哈希不符")
    frames = [decode(line) for line in raw.splitlines()]
    require(len(frames) > 0 and type(summary["frames"]) is int and len(frames) == summary["frames"], "帧数不符或为空")
    sent_raw = (root/"requests.jsonl").read_bytes()
    require(hashlib.sha256(sent_raw).hexdigest() == summary["requests_sha256"], "业务请求哈希不符")
    sent = [decode(line) for line in sent_raw.splitlines()]
    require(type(summary["requests_count"]) is int and len(sent) == summary["requests_count"] == len(requests)+len(frames), "业务请求数量不符")
    previous_sent = None
    for index, entry in enumerate(sent):
        wall, elapsed = integer(entry["sent_ms"]), integer(entry["elapsed_ns"], 9223372036854775807)
        require(elapsed >= 0 and abs(wall/1000-started.timestamp()-elapsed/1e9) <= .05, "请求时钟锚点不符")
        if previous_sent: require(wall >= previous_sent[0] and elapsed >= previous_sent[1], "业务请求时刻倒退")
        previous_sent = wall, elapsed
        if index < len(requests):
            target = requests[index]
            expected = dict(aid="set_chart", chart_id=target["chart"], ins_list=target["symbol"], duration=60000000000, view_width=500)
            require(entry["request"] == expected and elapsed <= frames[0]["elapsed_ns"], "实际订阅与元数据不符")
        else:
            frame_index = index-len(requests)
            require(entry["request"] == dict(aid="peek_message") and elapsed <= frames[frame_index]["elapsed_ns"], "实际读取请求不符")
            if frame_index: require(elapsed >= frames[frame_index-1]["elapsed_ns"], "读取请求早于前帧")
    snap, previous = {}, None
    for frame in frames:
        wall, elapsed = integer(frame["recv_ms"]), integer(frame["elapsed_ns"], 9223372036854775807)
        require(elapsed >= 0 and type(frame["message_type"]) is int and frame["message_type"] == 1, "非文本帧或单调时刻无效")
        require(abs(wall/1000-started.timestamp()-elapsed/1e9) <= .05, "帧墙钟与单调起点不符")
        if previous:
            require(wall >= previous[0] and elapsed >= previous[1], "接收时刻倒退")
            require(abs((wall-previous[0])/1000-(elapsed-previous[1])/1e9) <= .05, "帧间墙钟跳变")
        previous = wall, elapsed
        packet = decode(base64.b64decode(frame["payload_base64"], validate=True))
        require(isinstance(packet, dict), "行情响应不是对象")
        if packet.get("aid") == "rtn_data":
            require(isinstance(packet.get("data"), list), "DIFF数据缺失")
            for patch in packet["data"]: merge(snap, patch)
    require(integer(summary["elapsed_ns"], 9223372036854775807) >= frames[-1]["elapsed_ns"], "摘要早于末帧")
    require(0 <= summary["elapsed_ns"]/1e9 <= timeout+2 and
            abs(ended.timestamp()-started.timestamp()-summary["elapsed_ns"]/1e9) <= .05, "摘要结束或执行期限不符")
    output = []
    for request in requests:
        chart = snap.get("charts", {}).get(request["chart"], {})
        require(chart.get("ready") is True and chart.get("more_data", False) is False, "图表尚未就绪")
        serial = snap.get("klines", {}).get(request["symbol"], {}).get("60000000000", {})
        last = integer(serial.get("last_id"))
        require(last >= 0, "最后id是哨兵")
        start = None if serial.get("trading_day_start_id") is None else integer(serial["trading_day_start_id"])
        end = None if serial.get("trading_day_end_id") is None else integer(serial["trading_day_end_id"])
        have_span = start is not None and end is not None and start >= 0 and end >= 0
        require(not request["need_span"] or have_span, "必需跨度缺失或仍为哨兵")
        first = None
        if have_span:
            require(start <= last <= end, "节点id顺序错误")
            first = timestamp(serial.get("data", {}).get(str(start)))
        newest = timestamp(serial.get("data", {}).get(str(last)))
        if have_span: require(first <= newest and (last == start or first < newest), "首末根标签不自洽")
        output.append(dict(symbol=request["symbol"], last_id=last, start_id=start, end_id=end, first_ns=first, newest_ns=newest, have_span=have_span))
    require(json.dumps(summary["nodes"], sort_keys=True) == json.dumps(output, sort_keys=True), "摘要节点与独立重放不符")
    return dict(schema="day-nodes-review-v1", classification="node-collection-only", frames=len(frames), nodes=output,
                l9_control_set_complete=set(symbols) == L9_SYMBOLS, control_values_check="use_business_probe_separately",
                live_suspended_night_acceptance="not_established", l9_rule="not_selected")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("directory", type=Path)
    args = parser.parse_args()
    try:
        result = review(args.directory)
    except (OSError, ValueError, TypeError, KeyError) as error:
        raise SystemExit("FAIL L9独立复核："+str(error)) from error
    print(json.dumps(result, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    main()
