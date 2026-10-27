"""NTP 时间来源、格式、偏差区间与失败路径的离线标定。"""
import copy
import io
import struct
import sys
import unittest

from clock_probe import SERVERS, combine, encode_stamp, parse_response


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
