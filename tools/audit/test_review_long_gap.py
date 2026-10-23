"""独立长缺口复核器的合成证据与反例，完全离线。"""
import copy
import contextlib
import io
import json
from pathlib import Path
import sys
import tempfile
import unittest

from review_long_gap import main, millis, read_lines, verify


def fixture():
    env = {"INS": "SHFE.rb2701", "DAYS": "20261022,20261023", "START": "2026-10-23T13:30:00+08:00",
           "BEGIN": "2026-10-23T13:31:00+08:00", "DROP": "2026-10-23T13:36:00+08:00",
           "RESUME": "2026-10-23T14:08:00+08:00", "END": "2026-10-23T14:18:00+08:00"}
    start, restored = millis(env["START"]), millis(env["RESUME"]) + 200
    events = [{"event": "socket_closed", "when_ms": millis(env["DROP"]) + 100},
              {"event": "resume_next", "when_ms": restored},
              {"event": "capture_end", "when_ms": millis(env["END"]) + 10000}]
    raw, hist, live = [], [], []
    for i in range(48):
        ts = start + i * 60000
        row = {"ID": 100 + i, "Datetime": ts * 1000000, "Open": 100, "High": 102,
               "Low": 99, "Close": 101, "Volume": 2, "CloseOI": 30}
        hist.append(row)
        values = {"datetime": row["Datetime"], "open": 100, "high": 102, "low": 99,
                  "close": 101, "volume": 2, "close_oi": 30}
        recv = max(millis(env["BEGIN"]), ts + 500)
        raw.append({"conn": 1, "recv_ms": recv, "raw": {"aid": "rtn_data", "data": [
            {"klines": {env["INS"]: {"60000000000": {"data": {str(100 + i): values}}}}}]}})
        when = ts + 61000 if i < 5 or i >= 38 else restored + 1000 + i
        live.append({"ts": ts, "day": 20261023, "when_ms": when,
                     "o": 100, "h": 102, "l": 99, "c": 101, "v": 2, "oi": 30})
    return env, events, raw, hist, live


class ReviewTest(unittest.TestCase):
    def test_cli_sealed_bundle_and_manifest(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "capture").mkdir()
            env, events, raw, hist, live = fixture()
            manifest = {key: env[key.upper()] for key in ("begin", "drop", "resume", "end")}
            manifest.update(contract=env["INS"], sha="0" * 40)
            def save_manifest():
                (root / "manifest.json").write_text(json.dumps(manifest), encoding="utf-8")
            save_manifest()
            for name, items in {"reference": raw, "history": hist, "live": live, "events": [env] + events}.items():
                (root / "capture" / (name + ".jsonl")).write_text(
                    "\n".join(json.dumps(item) for item in items), encoding="utf-8")
            log = root / "run.log"
            log.write_text("--- PASS: TestLiveLongGapReal\nPASS\n", encoding="utf-8")
            output = io.StringIO()
            with contextlib.redirect_stdout(output):
                self.assertEqual(main([str(root)]), 0)
            report = json.loads(output.getvalue())
            self.assertEqual(report["complete_gap_rows"], 31)
            self.assertEqual(report["sha256_before"], report["sha256_after"])
            manifest["contract"] = "SHFE.rb2702"
            save_manifest()
            with self.assertRaises(ValueError):
                main([str(root)])
            manifest["contract"] = env["INS"]
            save_manifest()
            log.write_text("=== RUN TestLiveLongGapReal\n", encoding="utf-8")
            with self.assertRaises(ValueError):
                main([str(root)])

    def test_complete_and_negative_evidence(self):
        self.assertEqual(verify(*fixture())["complete_gap_rows"], 31)
        for mode in ("missing-reference", "changed-price", "wrong-day", "duplicate-output", "short-pause",
                     "reference-reconnect", "clock-backward", "no-pre-output", "historical-only", "non-finite"):
            with self.subTest(mode=mode):
                env, events, raw, hist, live = copy.deepcopy(fixture())
                if mode == "missing-reference":
                    del raw[15]
                elif mode == "changed-price":
                    live[15]["c"] = 900
                elif mode == "wrong-day":
                    live[15]["day"] += 1
                elif mode == "duplicate-output":
                    live[15] = live[14]
                elif mode == "short-pause":
                    events[1]["when_ms"] = millis(env["RESUME"]) - 60000
                elif mode == "reference-reconnect":
                    raw[15]["conn"] = 2
                elif mode == "clock-backward":
                    raw[15]["recv_ms"] = 1
                elif mode == "no-pre-output":
                    for output in live:
                        output["when_ms"] = events[1]["when_ms"] + 1000
                elif mode == "historical-only":
                    for record in raw:
                        record["recv_ms"] = millis(env["END"])
                else:
                    hist[15]["Close"] = float("nan")
                with self.assertRaises(ValueError):
                    verify(env, events, raw, hist, live)

    def test_rejects_broken_jsonl(self):
        self.assertEqual(read_lines(b'{"ok":1}\n'), [{"ok": 1}])
        for broken in (b'{"ok":1}\n]', b'{"ok":1}\n{', b'{"ok":1}]'):
            with self.assertRaises(ValueError):
                read_lines(broken)


if __name__ == "__main__":
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
