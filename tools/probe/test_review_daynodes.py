"""L9独立重放的合成正例与逐项破坏；不访问真实行情。"""
import base64
import copy
import datetime as dt
import hashlib
import io
import json
from pathlib import Path
import sys
import tempfile
import unittest

from review_daynodes import L9_SYMBOLS, review


def fixture(root):
    symbols = sorted(L9_SYMBOLS)
    start = dt.datetime.fromisoformat("2026-10-08T09:05:00+08:00")
    metadata = dict(schema="day-nodes-v1", classification="node-collection-only", started_at=start.isoformat(), timeout_seconds=90,
                    requests=[dict(symbol=s, chart="t"+str(i), need_span=s != "SHFE.rb1605") for i, s in enumerate(symbols)])
    charts, klines, nodes = {}, {}, []
    for i, symbol in enumerate(symbols):
        stamp = 1475715600000000000 if symbol == "SHFE.rb1605" else 1791421200000000000
        data = {"0": dict(datetime=stamp), "1": dict(datetime=stamp+60000000000)}
        serial = dict(last_id=1, data=data)
        item = dict(symbol=symbol, last_id=1, start_id=None, end_id=None, first_ns=None, newest_ns=stamp+60000000000, have_span=False)
        if symbol != "SHFE.rb1605":
            serial.update(trading_day_start_id=0, trading_day_end_id=224)
            item.update(start_id=0, end_id=224, first_ns=stamp, have_span=True)
        charts["t"+str(i)] = dict(ready=True, more_data=False)
        klines[symbol] = {"60000000000": serial}
        nodes.append(item)
    frames = []
    for index, patch in enumerate((dict(charts=charts), dict(klines=klines))):
        payload = json.dumps(dict(aid="rtn_data", data=[patch])).encode()
        frames.append(dict(recv_ms=int(start.timestamp()*1000)+index*1000, elapsed_ns=index*1000000000,
                           message_type=1, payload_base64=base64.b64encode(payload).decode()))
    summary = dict(schema="day-nodes-v1", classification="node-collection-only", state="completed", reason="complete_nodes", frames=2,
                   elapsed_ns=1100000000, ended_at=(start+dt.timedelta(seconds=1.1)).isoformat(), nodes=nodes)
    return metadata, summary, frames


def write(root, metadata, summary, frames):
    raw = b"".join(json.dumps(frame).encode()+b"\n" for frame in frames)
    (root/"frames.jsonl").write_bytes(raw)
    summary["frames_sha256"] = hashlib.sha256(raw).hexdigest()
    sent = [dict(sent_ms=frames[0]["recv_ms"], elapsed_ns=0, request=dict(aid="set_chart", chart_id=r["chart"], ins_list=r["symbol"], duration=60000000000, view_width=500)) for r in metadata["requests"]]
    for frame in frames:
        sent.append(dict(sent_ms=frame["recv_ms"], elapsed_ns=frame["elapsed_ns"], request=dict(aid="peek_message")))
    sent_raw = b"".join(json.dumps(row).encode()+b"\n" for row in sent)
    (root/"requests.jsonl").write_bytes(sent_raw)
    summary.update(requests_count=len(sent), requests_sha256=hashlib.sha256(sent_raw).hexdigest())
    (root/"metadata.json").write_text(json.dumps(metadata), encoding="utf-8")
    (root/"summary.json").write_text(json.dumps(summary), encoding="utf-8")


class DayNodeReviewTest(unittest.TestCase):
    def test_complete_and_mutations(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            original = fixture(root)
            for case in ("valid", "failed", "missing_control", "false_requirement", "backwards", "clock_jump", "bad_payload", "bad_json",
                         "fractional", "missing_span", "sentinel", "wrong_summary", "not_ready", "more_data", "null_row", "duration", "request_wrong"):
                metadata, summary, frames = copy.deepcopy(original)
                if case == "failed": summary["state"] = "failed"
                elif case == "missing_control": metadata["requests"].pop()
                elif case == "false_requirement": metadata["requests"][0]["need_span"] = False
                elif case == "backwards": frames[1]["elapsed_ns"] = -1
                elif case == "clock_jump": frames[1]["recv_ms"] += 100
                elif case == "bad_payload": frames[1]["payload_base64"] = "!"
                elif case == "bad_json": frames[1]["payload_base64"] = base64.b64encode(b'{broken').decode()
                elif case == "wrong_summary": summary["nodes"][0]["last_id"] = 0
                elif case == "duration": summary["elapsed_ns"] = 100000000000
                elif case in ("fractional", "missing_span", "sentinel", "null_row"):
                    packet = json.loads(base64.b64decode(frames[1]["payload_base64"]))
                    serial = packet["data"][0]["klines"]["KQ.m@SHFE.rb"]["60000000000"]
                    if case == "fractional": serial["last_id"] = 1.5
                    elif case == "missing_span": serial.pop("trading_day_end_id")
                    elif case == "sentinel": serial["trading_day_end_id"] = -1
                    else: serial["data"]["0"] = None
                    frames[1]["payload_base64"] = base64.b64encode(json.dumps(packet).encode()).decode()
                elif case in ("not_ready", "more_data"):
                    packet = json.loads(base64.b64decode(frames[0]["payload_base64"]))
                    packet["data"][0]["charts"]["t0"]["ready" if case == "not_ready" else "more_data"] = case == "more_data"
                    frames[0]["payload_base64"] = base64.b64encode(json.dumps(packet).encode()).decode()
                write(root, metadata, summary, frames)
                if case == "request_wrong":
                    sent = [json.loads(line) for line in (root/"requests.jsonl").read_bytes().splitlines()]
                    sent[0]["request"]["ins_list"] = "wrong-symbol"
                    changed = b"".join(json.dumps(row).encode()+b"\n" for row in sent)
                    (root/"requests.jsonl").write_bytes(changed)
                    summary["requests_sha256"] = hashlib.sha256(changed).hexdigest()
                    (root/"summary.json").write_text(json.dumps(summary), encoding="utf-8")
                with self.subTest(case=case):
                    if case == "valid":
                        result = review(root)
                        self.assertTrue(result["l9_control_set_complete"])
                        self.assertEqual(result["l9_rule"], "not_selected")
                    else:
                        with self.assertRaises((ValueError, TypeError, KeyError)): review(root)

    def test_raw_tamper_rejected_even_with_unchanged_summary(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp); write(root, *fixture(root))
            with (root/"frames.jsonl").open("ab") as output: output.write(b'changed')
            with self.assertRaisesRegex(ValueError, "哈希"): review(root)


if __name__ == "__main__":
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
