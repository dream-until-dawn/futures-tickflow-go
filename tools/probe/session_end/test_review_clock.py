"""独立复核器的完整正例与可观察破坏矩阵。"""
import copy
import io
import json
from pathlib import Path
import struct
import sys
import tempfile
import unittest
from unittest.mock import patch

import clock_probe
from review_clock import review


def cycle(wall=1000.0, mono=100.0, sequence=0):
    samples = []
    for index in range(6):
        sent = wall+index*.1
        packet = bytearray(48)
        packet[0], packet[1] = 0x24, 2
        request = clock_probe.encode_stamp(sent)
        packet[24:32] = request
        packet[32:40] = clock_probe.encode_stamp(sent-.05)
        packet[40:48] = clock_probe.encode_stamp(sent-.04)
        samples.append(dict(server=clock_probe.SERVERS[index % 2], sent_at=sent, received_at=sent+.2,
                            elapsed_seconds=.2, request_stamp_hex=request.hex(), response_hex=packet.hex(),
                            **clock_probe.parse_response(packet, request, sent, sent+.2, .2)))
    proof = clock_probe.combine(samples)
    proof.update(schema="ntp-cycle-v2", recording_id="00000000-0000-4000-8000-000000000001",
                 sequence=sequence, attempts=6, measured_at=wall+2, cycle_started_at=wall, cycle_seconds=2,
                 anchors=dict(wall_start=wall, wall_end=wall+2, monotonic_start=mono, monotonic_end=mono+2))
    return proof


class ClockReviewTest(unittest.TestCase):
    def setUp(self):
        self.proofs = [cycle(), cycle(1035, 135, 1)]

    def test_complete_raw_packets_and_coverage(self):
        result = review(self.proofs, 1003, 1034)
        self.assertEqual((result["cycles"], result["samples"]), (2, 12))
        self.assertEqual(result["inter_cycle_jump_check"], "passed")
        self.assertEqual(review([cycle(), cycle(1035.03, 135, 1)])["cycles"], 2)

    def test_positive_and_negative_inter_cycle_jump(self):
        for delta in (-.2, .2):
            changed = [cycle(), cycle(1035+delta, 135, 1)]
            with self.subTest(delta=delta), self.assertRaisesRegex(ValueError, "周期间墙钟跳变"):
                review(changed)

    def test_mutation_matrix(self):
        for kind in ("legacy", "missing_anchor", "nan", "bool", "backwards", "overlap", "gap",
                     "sequence", "splice", "packet", "interval", "cycle_jump", "sample_outside"):
            changed = copy.deepcopy(self.proofs)
            if kind == "legacy": changed[0].pop("schema")
            elif kind == "missing_anchor": changed[0]["anchors"].pop("monotonic_start")
            elif kind == "nan": changed[0]["anchors"]["monotonic_start"] = float("nan")
            elif kind == "bool": changed[0]["anchors"]["wall_start"] = True
            elif kind == "backwards": changed[1] = cycle(1035, 90, 1)
            elif kind == "overlap": changed[1] = cycle(1001, 101, 1)
            elif kind == "gap": changed[1] = cycle(1100, 200, 1)
            elif kind == "sequence": changed[1]["sequence"] = 2
            elif kind == "splice": changed[1]["recording_id"] = "00000000-0000-4000-8000-000000000002"
            elif kind == "packet": changed[0]["samples"][0]["response_hex"] = "00"
            elif kind == "interval": changed[0]["offset_low_seconds"] += .01
            elif kind == "cycle_jump": changed[0]["anchors"]["wall_end"] += .1
            else: changed[0] = cycle(1000, 100); changed[0]["anchors"]["wall_end"] = 1000.1
            with self.subTest(kind=kind), self.assertRaises((ValueError, KeyError)):
                review(changed)

    def test_empty_and_missing_window_rejected(self):
        for proofs, begin, end in (([], None, None), (self.proofs, 1001, 1034),
                                   (self.proofs, 1003, 1036), (self.proofs, 1003, None)):
            with self.subTest(begin=begin, end=end), self.assertRaises(ValueError): review(proofs, begin, end)

    def test_recorder_persists_identity_and_sequence(self):
        now = [1000.0]
        def measure(trace, *, recording_id, sequence):
            proof = cycle(now[0], now[0]-900, sequence)
            proof["recording_id"] = recording_id
            now[0] += 2
            for sample in proof["samples"]: trace(dict(state="sample", sample=sample))
            trace(dict(state="cycle_pass"))
            return proof
        def pause(seconds): now[0] += seconds
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp)/"clock.jsonl"
            with patch("clock_probe.measure", side_effect=measure), \
                    patch("clock_probe.time.time", side_effect=lambda: now[0]), \
                    patch("clock_probe.time.monotonic", side_effect=lambda: now[0]-900), \
                    patch("clock_probe.time.sleep", side_effect=pause):
                clock_probe.record(path, 1033)
            proofs = [json.loads(line) for line in path.read_text(encoding="utf-8").splitlines()]
            self.assertEqual(review(proofs)["cycles"], 2)
            self.assertEqual([p["sequence"] for p in proofs], [0, 1])
            events = [json.loads(line) for line in path.with_name("clock-attempts.jsonl").read_text(encoding="utf-8").splitlines()]
            self.assertEqual(review(proofs, diagnostics=events)["recording_end_check"], "passed")
            for kind in ("missing_end", "jump", "wrong_count", "missing_sample", "anchor", "end_jump"):
                changed = copy.deepcopy(events)
                if kind == "missing_end": changed.pop()
                elif kind == "jump": changed.insert(-1, dict(state="record_jump"))
                elif kind == "wrong_count": changed[-1]["cycles"] += 1
                elif kind == "missing_sample": changed.pop(1)
                elif kind == "end_jump": changed[-1]["wall"] += .2
                else: changed[0]["monotonic"] += .2
                with self.subTest(kind=kind), self.assertRaises(ValueError): review(proofs, diagnostics=changed)


if __name__ == "__main__":
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
