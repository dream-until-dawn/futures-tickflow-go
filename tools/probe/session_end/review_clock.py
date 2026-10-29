"""离线独立复算v2时钟证明；不导入生产采样器，不为旧记录补造锚点。"""
import argparse
import datetime as dt
import io
import json
import math
from pathlib import Path
import struct
import sys
import uuid


def require(condition, reason):
    if not condition:
        raise ValueError(reason)


def number(value):
    require(type(value) in (int, float) and math.isfinite(value), "数值缺失或非有限")
    return value


def stamp(raw):
    seconds, fraction = struct.unpack("!II", raw)
    return seconds - 2208988800 + fraction / 2**32


def sample_interval(item):
    """用四时刻和原始包独立推导保守区间，拒绝伪造的派生读数。"""
    packet = bytes.fromhex(item["response_hex"])
    require(len(packet) >= 48, "NTP包截断")
    require(packet[0] & 7 == 4 and (packet[0] >> 3) & 7 in (3, 4), "NTP模式或版本错误")
    require(packet[0] >> 6 != 3 and 1 <= packet[1] <= 15, "NTP未同步或层级无效")
    request = bytes.fromhex(item["request_stamp_hex"])
    require(len(request) == 8 and packet[24:32] == request, "NTP回显错误")
    t1, t4 = number(item["sent_at"]), number(item["received_at"])
    elapsed = number(item["elapsed_seconds"])
    require(abs(stamp(request) - t1) <= 0.000001, "请求时刻不匹配")
    require(0 <= t4-t1 <= 0.5 and abs(t4-t1-elapsed) <= 0.05, "样本时钟跳变或RTT超限")
    t2, t3 = stamp(packet[32:40]), stamp(packet[40:48])
    radius = struct.unpack("!I", packet[8:12])[0]/65536 + max(0, struct.unpack("!i", packet[4:8])[0]/65536)/2 + .001
    require(0 < t2 <= t3 and t3-t2 <= t4-t1+.001 and radius <= .1, "服务端时刻或根误差无效")
    low, high = t3-t4-radius, t2-t1+radius
    require(low <= high and max(abs(low), abs(high)) <= 5, "偏差越界")
    for key, expected in (("offset_low_seconds", low), ("offset_high_seconds", high),
                          ("round_trip_seconds", t4-t1), ("root_radius_seconds", radius)):
        require(abs(number(item[key])-expected) <= 1e-9, "样本派生值不符："+key)
    return low, high


def review(proofs, begin=None, end=None, diagnostics=None):
    """同一记录的连续序号、周期起止及周期间墙钟/单调增量须一致。"""
    require(bool(proofs), "空时钟记录")
    previous, identity, maximum_gap = None, None, 0
    lows, highs = [], []
    for index, proof in enumerate(proofs):
        require(proof.get("schema") == "ntp-cycle-v2", "旧证明缺少v2锚点，不能升级为独立周期间复核")
        current_id = proof["recording_id"]
        require(isinstance(current_id, str), "记录标识须为字符串")
        require(str(uuid.UUID(current_id)) == current_id, "记录标识无效")
        if identity is None:
            identity = current_id
        require(current_id == identity and type(proof["sequence"]) is int and proof["sequence"] == index, "记录拼接或周期缺失")
        anchor = proof["anchors"]
        ws, ms, we, me = [number(anchor[k]) for k in ("wall_start", "monotonic_start", "wall_end", "monotonic_end")]
        require(0 <= me-ms <= 20 and abs((we-ws)-(me-ms)) <= .05, "周期内锚点跳变")
        for key, expected in (("cycle_seconds", me-ms), ("cycle_started_at", ws), ("measured_at", we)):
            require(abs(number(proof[key])-expected) <= 1e-9, "周期派生值不符")
        if previous is not None:
            pws, pms, pwe, pme = previous
            gap = ms-pms
            require(0 < gap <= 65 and ms >= pme and ws >= pwe, "周期间单调倒退、重叠或间隔缺口")
            require(abs((ws-pws)-gap) <= .05, "周期间墙钟跳变")
            maximum_gap = max(maximum_gap, gap)
        previous = (ws, ms, we, me)
        samples = proof["samples"]
        require(len(samples) == 6 and all(sum(s["server"] == h for s in samples) == 3
                                        for h in ("ntp.aliyun.com", "time.windows.com")), "双来源六样本不齐")
        intervals = []
        for sample in samples:
            intervals.append(sample_interval(sample))
            require(ws-.05 <= sample["sent_at"] <= sample["received_at"] <= we+.05, "样本不在周期内")
        low, high = max(v[0] for v in intervals), min(v[1] for v in intervals)
        require(low <= high and high-low <= .5, "来源冲突或区间过宽")
        require(abs(number(proof["offset_low_seconds"])-low) <= 1e-9 and
                abs(number(proof["offset_high_seconds"])-high) <= 1e-9, "周期区间伪造")
        require(type(proof["attempts"]) is int and 6 <= proof["attempts"] <= 18, "尝试数越界")
        lows.append(low)
        highs.append(high)
    if begin is not None or end is not None:
        require(begin is not None and end is not None and begin < end, "须同时给有效覆盖起止")
        require(proofs[0]["anchors"]["wall_end"] <= begin and
                proofs[-1]["anchors"]["wall_start"] >= end, "缺少窗口前后校准覆盖")
    if diagnostics is not None:
        require(not any(e.get("state") in ("record_jump", "cycle_failed") for e in diagnostics), "诊断记录含失败或跳变")
        require([e["sample"] for e in diagnostics if e.get("state") == "sample"] ==
                [s for p in proofs for s in p["samples"]], "诊断与证明样本不符")
        require(sum(e.get("state") == "cycle_pass" for e in diagnostics) == len(proofs), "周期成功记录不符")
        anchors = [e for e in diagnostics if e.get("state") == "record_anchor"]
        require(len(anchors) == len(proofs), "记录起点缺失")
        for index, anchor in enumerate(anchors):
            require(anchor["recording_id"] == identity and anchor["sequence"] == index, "诊断锚点拼接或缺失")
            require(abs((number(anchor["wall"])-proofs[index]["anchors"]["wall_start"])-
                        (number(anchor["monotonic"])-proofs[index]["anchors"]["monotonic_start"])) <= .05, "诊断起点与周期锚点不符")
        endings = [e for e in diagnostics if e.get("state") == "record_end"]
        require(len(endings) == 1 and diagnostics[-1].get("state") == "record_end", "缺少唯一正常结束记录")
        ending = endings[0]
        require(ending["recording_id"] == identity and type(ending["cycles"]) is int and ending["cycles"] == len(proofs), "结束标识或周期数不符")
        require(number(ending["wall"]) >= number(ending["until"]) and
                number(ending["monotonic"]) >= proofs[-1]["anchors"]["monotonic_end"], "记录提前结束或单调时钟倒退")
        require(abs((ending["wall"]-proofs[-1]["anchors"]["wall_end"])-
                    (ending["monotonic"]-proofs[-1]["anchors"]["monotonic_end"])) <= .05, "结束锚点跳变")
    return dict(schema="ntp-review-v2", cycles=len(proofs), samples=6*len(proofs), recording_id=identity,
                inter_cycle_jump_check="passed" if len(proofs)>1 else "not_applicable_single_cycle",
                recording_end_check="passed" if diagnostics is not None else "not_checked_proof_only",
                max_start_gap_seconds=maximum_gap,
                offset_envelope_seconds=[min(lows), max(highs)],
                limitations="NTP accuracy and nonnegative delay assumed; no independent upstream or oscillator bound; not exchange latency")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("input", type=Path)
    parser.add_argument("--begin")
    parser.add_argument("--end")
    parser.add_argument("--diagnostics", type=Path, help="默认读取输入同名-attempts.jsonl，要求正常结束记录")
    args = parser.parse_args()
    def instant(value):
        if value is None:
            return None
        point = dt.datetime.fromisoformat(value)
        require(point.utcoffset() is not None, "覆盖时刻须显式时区")
        return point.timestamp()
    try:
        diagnostics = args.diagnostics or args.input.with_name(args.input.stem+"-attempts.jsonl")
        result = review([json.loads(line) for line in args.input.read_text(encoding="utf-8").splitlines()],
                        instant(args.begin), instant(args.end),
                        [json.loads(line) for line in diagnostics.read_text(encoding="utf-8").splitlines()])
    except (OSError, ValueError, KeyError, TypeError, struct.error) as error:
        raise SystemExit("FAIL 时钟独立复核："+str(error)) from error
    print(json.dumps(result, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    main()
