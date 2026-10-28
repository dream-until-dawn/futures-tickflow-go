"""NTP 时间来源、格式、偏差区间与失败路径的离线标定。"""
import copy
import io
import struct
import sys
import threading
import unittest
from unittest.mock import patch

from clock_probe import SERVERS, combine, encode_stamp, measure, parse_response, resolve


class RetryClockTest(unittest.TestCase):
    """用可控时间验证恢复、耗尽和质量失败，不能只验证正常返回。"""
    def run_cycle(self, failure=None):
        self.now, self.calls, self.events = 1000.0, [], []

        def pause(seconds):
            self.now += seconds

        def sampler(host, timeout):
            self.calls.append((host, self.now, timeout))
            if failure:
                failure(len(self.calls), pause)
            return dict(server=host, offset_low_seconds=-0.1, offset_high_seconds=0.1)

        return measure(self.events.append, sampler=sampler, monotonic=lambda: self.now,
                       wall=lambda: self.now, pause=pause)

    def test_transient_network_failure_retained_and_six_valid_samples(self):
        def failure(number, pause):
            if number == 1:
                raise TimeoutError("模拟丢包")
        proof = self.run_cycle(failure)
        self.assertEqual((proof["attempts"], len(proof["samples"])), (7, 6))
        self.assertEqual(self.events[0]["state"], "error")
        self.assertEqual(self.events[-1]["state"], "cycle_pass")
        for host in SERVERS:
            times = [at for source, at, _ in self.calls if source == host]
            self.assertTrue(all(b-a >= 2 for a, b in zip(times, times[1:])))

    def test_network_failure_exhausts_without_source_substitution(self):
        def failure(number, pause):
            raise TimeoutError("持续断网")
        with self.assertRaises(TimeoutError): self.run_cycle(failure)
        self.assertEqual(len(self.calls), 3)
        self.assertEqual({item[0] for item in self.calls}, {SERVERS[0]})
        self.assertEqual(self.events[-1]["state"], "cycle_failed")

    def test_quality_failure_is_not_retried(self):
        def failure(number, pause):
            raise ValueError("未同步或回显错误")
        with self.assertRaises(ValueError): self.run_cycle(failure)
        self.assertEqual(len(self.calls), 1)

    def test_total_cycle_budget_does_not_reset_per_request(self):
        def failure(number, pause):
            pause(4)
        with self.assertRaises(TimeoutError): self.run_cycle(failure)
        self.assertEqual(len(self.calls), 5)
        self.assertEqual(self.now, 1020)

    def test_whole_cycle_wall_clock_jump_rejected(self):
        count = 0
        def wall():
            nonlocal count
            count += 1
            return 1000 + (1 if count > 7 else 0)
        with self.assertRaisesRegex(ValueError, "跳变"):
            measure(sampler=lambda host, timeout: dict(server=host, offset_low_seconds=0, offset_high_seconds=0.1),
                    monotonic=lambda: 1000, wall=wall, pause=lambda seconds: None)

    def test_dns_timeout_and_failure_are_bounded(self):
        release = threading.Event()
        entered = threading.Event()
        def lookup(*args):
            entered.set()
            release.wait(2)
            return []
        try:
            with patch("clock_probe.socket.getaddrinfo", side_effect=lookup):
                with self.assertRaises(TimeoutError): resolve("test.invalid", 0.02)
                self.assertTrue(entered.is_set())
        finally:
            release.set()
        with patch("clock_probe.socket.getaddrinfo", return_value=[]):
            with self.assertRaises(OSError): resolve("test.invalid", 1)


class ClockProofTest(unittest.TestCase):
    def packet(self):
        packet = bytearray(48)
        packet[0], packet[1] = 0x24, 2
        packet[24:32] = encode_stamp(1000)
        packet[32:40] = encode_stamp(999.1)
        packet[40:48] = encode_stamp(999.11)
        return packet

    def test_valid_offset_interval_is_not_a_point(self):
        proof = parse_response(self.packet(), encode_stamp(1000), 1000, 1000.2, 0.2)
        self.assertLess(proof["offset_low_seconds"], -1.09)
        self.assertGreater(proof["offset_high_seconds"], -0.90)

    def test_invalid_response_matrix(self):
        for kind in ("short", "mode", "leap", "stratum", "echo", "time_order", "dispersion", "root_delay"):
            packet = self.packet()
            if kind == "short": packet = packet[:30]
            elif kind == "mode": packet[0] = 0x23
            elif kind == "leap": packet[0] = 0xe4
            elif kind == "stratum": packet[1] = 16
            elif kind == "echo": packet[24:32] = encode_stamp(990)
            elif kind == "time_order": packet[40:48] = encode_stamp(998)
            elif kind == "dispersion": packet[8:12] = struct.pack("!I", 65536)
            else: packet[4:8] = struct.pack("!i", 65536)
            with self.subTest(kind=kind), self.assertRaises(ValueError):
                parse_response(packet, encode_stamp(1000), 1000, 1000.2, 0.2)

    def test_roundtrip_clock_jump_and_excessive_offset(self):
        for sent, received, elapsed in ((1000, 1001, 1), (1000, 1000.2, 0.1), (1000, 999, 0.1), (1010, 1010.2, 0.2)):
            with self.subTest(sent=sent, received=received), self.assertRaises(ValueError):
                parse_response(self.packet(), encode_stamp(1000), sent, received, elapsed)

    def test_both_sources_must_agree(self):
        samples = [dict(server=h, offset_low_seconds=-1.1, offset_high_seconds=-0.9) for _ in range(3) for h in SERVERS]
        self.assertEqual(combine(samples)["offset_low_seconds"], -1.1)
        for kind in ("missing", "one_source", "conflict", "nan", "reversed"):
            changed = copy.deepcopy(samples)
            if kind == "missing": changed.pop()
            elif kind == "one_source":
                for item in changed: item["server"] = SERVERS[0]
            elif kind == "conflict": changed[0].update(offset_low_seconds=1, offset_high_seconds=2)
            elif kind == "nan": changed[0]["offset_low_seconds"] = float("nan")
            else: changed[0].update(offset_low_seconds=2, offset_high_seconds=1)
            with self.subTest(kind=kind), self.assertRaises(ValueError): combine(changed)


if __name__ == "__main__":
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
