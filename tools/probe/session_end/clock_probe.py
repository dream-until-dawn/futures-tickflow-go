"""只读采集 NTP 偏差区间；不设置系统时间，不将网络延迟当成时钟真值。"""
import argparse
import datetime as dt
import io
import json
import math
import queue
from pathlib import Path
import socket
import struct
import sys
import threading
import time

SERVERS = ("ntp.aliyun.com", "time.windows.com")
EPOCH = 2208988800
CYCLE_BUDGET = 20
MAX_ATTEMPTS = 3
MIN_SOURCE_GAP = 2


def encode_stamp(value):
    seconds = value + EPOCH
    whole = int(seconds)
    return struct.pack("!II", whole, int((seconds - whole) * 2**32))


def decode_stamp(data):
    seconds, fraction = struct.unpack("!II", data)
    return seconds - EPOCH + fraction / 2**32


def parse_response(data, request_stamp, sent, received, elapsed):
    """NTP 服务端 T2/T3 与本机 T1/T4 给出偏差的保守区间。"""
    if len(data) < 48 or data[0] & 7 != 4 or (data[0] >> 3) & 7 not in (3, 4):
        raise ValueError("NTP响应长度、模式或版本错误")
    if data[0] >> 6 == 3 or not 1 <= data[1] <= 15 or data[24:32] != request_stamp:
        raise ValueError("NTP未同步、层级或请求回显无效")
    if not all(math.isfinite(v) for v in (sent, received, elapsed)):
        raise ValueError("时刻非有限值")
    if not 0 <= received - sent <= 0.5 or abs(received - sent - elapsed) > 0.05:
        raise ValueError("往返过长或采样期间本机时钟跳变")
    t2, t3 = decode_stamp(data[32:40]), decode_stamp(data[40:48])
    dispersion = struct.unpack("!I", data[8:12])[0] / 65536
    root_delay = struct.unpack("!i", data[4:8])[0] / 65536
    radius = dispersion + max(0, root_delay) / 2 + 0.001
    if not 0 < t2 <= t3 or t3 - t2 > received - sent + 0.001 or radius > 0.1:
        raise ValueError("NTP服务端时间或根误差不可信")
    low, high = t3 - received - radius, t2 - sent + radius
    if low > high or max(abs(low), abs(high)) > 5:
        raise ValueError("偏差区间无效或超过五秒")
    return dict(offset_low_seconds=low, offset_high_seconds=high,
                round_trip_seconds=received - sent, root_radius_seconds=radius)


def resolve(host, timeout):
    """DNS等待有界；后台解析不阻止进程退出，不修改系统解析配置。"""
    result = queue.Queue(maxsize=1)

    def lookup():
        try:
            result.put(socket.getaddrinfo(host, 123, socket.AF_INET, socket.SOCK_DGRAM))
        except OSError as error:
            result.put(error)

    threading.Thread(target=lookup, daemon=True).start()
    try:
        answer = result.get(timeout=timeout)
    except queue.Empty as error:
        raise TimeoutError("DNS解析超时") from error
    if isinstance(answer, OSError):
        raise answer
    addresses = sorted({item[4][0] for item in answer})
    if not addresses:
        raise OSError("DNS没有IPv4地址")
    return addresses


def sample(host, timeout=3):
    # DNS和UDP共享单调时钟预算；连接数值IP约束响应来源。
    deadline = time.monotonic() + timeout
    context = dict(stage="dns")
    try:
        addresses = resolve(host, min(2, timeout))
    except OSError as error:
        error.sample_context = context
        raise
    context.update(stage="udp", address=addresses[0], resolved_addresses=addresses)
    with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as connection:
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            raise TimeoutError("DNS耗尽请求预算")
        connection.settimeout(remaining)
        connection.connect((addresses[0], 123))
        sent, monotonic = time.time(), time.monotonic()
        stamp = encode_stamp(sent)
        try:
            connection.send(bytes((0x23, 0, 4, 0)) + 36 * b"\0" + stamp)
            data = connection.recv(512)
        except OSError as error:
            error.sample_context = dict(context, sent_at=sent, request_stamp_hex=stamp.hex())
            raise
        received, elapsed = time.time(), time.monotonic() - monotonic
    context.update(stage="quality", sent_at=sent, received_at=received, elapsed_seconds=elapsed,
                   request_stamp_hex=stamp.hex(), response_hex=data.hex())
    try:
        parsed = parse_response(data, stamp, sent, received, elapsed)
    except ValueError as error:
        error.sample_context = context
        raise
    return dict(server=host, **context, **parsed)


def combine(samples):
    """双来源每源三次齐全，全部区间须相交；缺失或冲突均失败。"""
    if len(samples) != 6 or any(sum(s["server"] == host for s in samples) != 3 for host in SERVERS):
        raise ValueError("缺少双来源六次完整采样")
    for item in samples:
        low, high = item["offset_low_seconds"], item["offset_high_seconds"]
        if not all(math.isfinite(v) for v in (low, high)) or not low <= high:
            raise ValueError("采样区间无效")
    low = max(s["offset_low_seconds"] for s in samples)
    high = min(s["offset_high_seconds"] for s in samples)
    if not low <= high or high - low > 0.5:
        raise ValueError("NTP来源区间冲突或不确定性过宽")
    return dict(measured_at=time.time(), offset_low_seconds=low, offset_high_seconds=high,
                samples=samples, assumption="NTP server accuracy and nonnegative path delay; not exchange latency")


def measure(trace=None, *, sampler=None, monotonic=None, wall=None, pause=None):
    """只重试网络错误；质量错误立即失败，成功周期仍须完整六样本。"""
    sampler, monotonic = sampler or sample, monotonic or time.monotonic
    wall, pause = wall or time.time, pause or time.sleep
    trace = trace or (lambda item: None)
    started, started_wall = monotonic(), wall()
    last, samples, attempts = {}, [], 0
    try:
        for slot in range(3):
            for host in SERVERS:
                for attempt in range(1, MAX_ATTEMPTS + 1):
                    delay = max(0, last.get(host, -math.inf) + MIN_SOURCE_GAP - monotonic())
                    if monotonic() + delay >= started + CYCLE_BUDGET:
                        raise TimeoutError("六采样周期预算耗尽")
                    pause(delay)
                    last[host] = monotonic()
                    attempts += 1
                    event = dict(server=host, slot=slot, attempt=attempt, at=wall())
                    try:
                        item = sampler(host, timeout=min(3, started + CYCLE_BUDGET - monotonic()))
                    except (OSError, ValueError) as error:
                        trace(dict(event, state="error", error_type=type(error).__name__, error=str(error),
                                   context=getattr(error, "sample_context", {})))
                        if isinstance(error, ValueError) or attempt == MAX_ATTEMPTS:
                            raise
                        continue
                    trace(dict(event, state="sample", sample=item))
                    samples.append(item)
                    break
        elapsed = monotonic() - started
        if elapsed > CYCLE_BUDGET or abs(wall() - started_wall - elapsed) > 0.05:
            raise ValueError("周期超时或本机时钟跳变")
        proof = combine(samples)
        proof.update(attempts=attempts, cycle_seconds=elapsed, cycle_started_at=started_wall)
        trace(dict(state="cycle_pass", attempts=attempts, elapsed_seconds=elapsed))
        return proof
    except (OSError, ValueError) as error:
        trace(dict(state="cycle_failed", attempts=attempts, error_type=type(error).__name__, error=str(error)))
        raise


def trace_writer(output):
    """逐条刷新诊断，失败周期同样保留，不覆盖旧记录。"""
    def write(item):
        output.write(json.dumps(item, ensure_ascii=False) + "\n")
        output.flush()
    return write


def record(path, until):
    """持续采样，记录墙钟/单调时钟变化；任一次失败立即退出。"""
    if until <= time.time():
        raise ValueError("校准截止已过期")
    previous = None
    with path.open("x", encoding="utf-8") as output, path.with_name(path.stem + "-attempts.jsonl").open("x", encoding="utf-8") as diagnostics:
        while True:
            wall, monotonic = time.time(), time.monotonic()
            if previous and abs((wall - previous[0]) - (monotonic - previous[1])) > 0.05:
                raise ValueError("记录期间本机时钟跳变")
            proof = measure(trace_writer(diagnostics))
            output.write(json.dumps(proof, ensure_ascii=False) + "\n")
            output.flush()
            previous = (wall, monotonic)
            if time.time() >= until:
                return
            time.sleep(min(30, max(0, until - time.time())))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("output", type=Path)
    parser.add_argument("--until", help="持续记录至显式带时区的RFC3339时刻")
    args = parser.parse_args()
    if args.until:
        end = dt.datetime.fromisoformat(args.until)
        if end.utcoffset() is None:
            raise ValueError("截止须带时区")
        record(args.output, end.timestamp())
    else:
        with args.output.with_name(args.output.stem + "-attempts.jsonl").open("x", encoding="utf-8") as diagnostics:
            proof = measure(trace_writer(diagnostics))
        with args.output.open("x", encoding="utf-8") as output:
            json.dump(proof, output, ensure_ascii=False, indent=2)
        print(json.dumps(proof, ensure_ascii=False))


if __name__ == "__main__":
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    main()
