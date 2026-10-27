"""只读采集 NTP 偏差区间；不设置系统时间，不将网络延迟当成时钟真值。"""
import argparse
import datetime as dt
import io
import json
import math
from pathlib import Path
import socket
import struct
import sys
import time

SERVERS = ("ntp.aliyun.com", "time.windows.com")
EPOCH = 2208988800


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


def sample(host):
    # DNS和网络均有有限等待；连接UDP同时约束响应来源IP与端口。
    with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as connection:
        connection.settimeout(3)
        connection.connect((host, 123))
        sent, monotonic = time.time(), time.monotonic()
        stamp = encode_stamp(sent)
        connection.send(b"\x23" + 39 * b"\0" + stamp)
        data = connection.recv(512)
        received, elapsed = time.time(), time.monotonic() - monotonic
    return dict(server=host, received_at=received,
                **parse_response(data, stamp, sent, received, elapsed))


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


def measure():
    return combine([sample(host) for _ in range(3) for host in SERVERS])


def record(path, until):
    """持续采样，记录墙钟/单调时钟变化；任一次失败立即退出。"""
    if until <= time.time():
        raise ValueError("校准截止已过期")
    previous = None
    with path.open("x", encoding="utf-8") as output:
        while True:
            wall, monotonic = time.time(), time.monotonic()
            if previous and abs((wall - previous[0]) - (monotonic - previous[1])) > 0.05:
                raise ValueError("记录期间本机时钟跳变")
            proof = measure()
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
        proof = measure()
        with args.output.open("x", encoding="utf-8") as output:
            json.dump(proof, output, ensure_ascii=False, indent=2)
        print(json.dumps(proof, ensure_ascii=False))


if __name__ == "__main__":
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    main()
