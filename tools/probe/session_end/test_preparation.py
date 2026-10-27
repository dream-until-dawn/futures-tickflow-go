"""夜盘参数、原生合约响应与启动保护的正反验证。"""
import copy
import datetime as dt
import hashlib
import io
import json
from pathlib import Path
import tempfile
import sys
import unittest
from unittest import mock

from prepare import ROOT, launch, validate_spec, verify_contract
from clock_probe import SERVERS


class NightPreparationTest(unittest.TestCase):
    def setUp(self):
        self.spec = json.loads((ROOT/"docs/live-plans/20261027-night.json").read_text(encoding="utf-8"))
        self.now = dt.datetime.fromisoformat("2026-10-27T15:00:00+08:00").timestamp()

    def test_valid_spec_and_invalid_matrix(self):
        self.assertEqual(validate_spec(self.spec, self.now)["close"].hour, 23)
        for change in ({"symbol":"KQ.m@SHFE.rb"}, {"symbol":"SHFE.rb2713"}, {"trading_day":"20261027"},
                       {"start":"2026-10-27T22:58:00+08:00"}, {"close":"2026-10-27T15:00:00+08:00"},
                       {"end":"2026-10-27T23:06:00+08:00"}, {"deadline":"2026-10-27T23:11:00+08:00"},
                       {"close":"2026-10-27T15:00:00+00:00"}, {"secret":"forbidden"}):
            with self.subTest(change=change), self.assertRaises(ValueError):
                validate_spec(dict(self.spec, **change), self.now)
        with self.assertRaises(ValueError): validate_spec(self.spec, self.now+8*3600)

    def test_identity_requires_exact_key_complete_rows_and_fields(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp)/"raw.jsonl"
            rows = {str(i):dict(datetime=int(self.now-300+i*60)*1000000000, open=1, high=2, low=1,
                                close=2, volume=1, close_oi=20, open_oi=19) for i in range(5)}
            def write(data, symbol="SHFE.rb2701"):
                path.write_text(json.dumps(dict(raw=dict(aid="rtn_data", data=[dict(klines={symbol:{"60000000000":{"data":data}}})])))+'\n',encoding="utf-8")
            write(rows)
            self.assertEqual(verify_contract(path,"SHFE.rb2701",self.now)["matched_rows"],5)
            for kind in ("alias", "missing", "bool", "misaligned", "stale"):
                changed=copy.deepcopy(rows)
                if kind=="missing":changed.pop("0")
                elif kind=="bool":changed["0"]["close"]=True
                elif kind=="misaligned":changed["0"]["datetime"]+=1000000
                elif kind=="stale":
                    for row in changed.values():row["datetime"]-=2*86400*1000000000
                write(changed,"KQ.m@SHFE.rb" if kind=="alias" else "SHFE.rb2701")
                with self.subTest(kind=kind),self.assertRaises(ValueError):verify_contract(path,"SHFE.rb2701",self.now)

    def test_launch_rejects_early_stale_and_tampered_artifacts(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp);(root/"binary").write_bytes(b"changed")
            manifest=dict(spec=self.spec,prepared_at=self.now,files={"binary":"0"*64})
            (root/"manifest.json").write_text(json.dumps(manifest),encoding="utf-8")
            for moment, reason in ((self.now,"计划起点"),(self.now+8*3600-600,"一分钟内的新执行单")):
                with mock.patch("prepare.time.time",return_value=moment),self.assertRaisesRegex(ValueError,reason):launch(root)
            manifest["prepared_at"]=self.now+8*3600-600
            (root/"manifest.json").write_text(json.dumps(manifest),encoding="utf-8")
            with mock.patch("prepare.time.time",return_value=manifest["prepared_at"]),self.assertRaisesRegex(ValueError,"字节改变"):launch(root)
            self.assertFalse((root/"clock-task").exists())

    def test_capture_starts_only_after_valid_clock_is_active(self):
        for state in ("running", "failed"):
            with self.subTest(state=state), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp)
                plan = dict(command=["unused-local-fixture"], cwd=str(ROOT), watch=["raw.jsonl"],
                            max_seconds=60, startup_seconds=10, idle_seconds=5, poll_seconds=1,
                            process_scope="independent")
                for name in ("clock-plan.json", "capture-plan.json"):
                    (root/name).write_text(json.dumps(plan), encoding="utf-8")
                moment = self.now+8*3600-600
                manifest = dict(spec=self.spec, prepared_at=moment,
                                files={name:hashlib.sha256((root/name).read_bytes()).hexdigest()
                                       for name in ("clock-plan.json", "capture-plan.json")})
                (root/"manifest.json").write_text(json.dumps(manifest), encoding="utf-8")
                called = []

                def start(task, effective_plan):
                    called.append(task.name)
                    task.mkdir()
                    if task.name == "clock-task":
                        samples = [dict(server=h, offset_low_seconds=-1, offset_high_seconds=-0.8)
                                   for _ in range(3) for h in SERVERS]
                        (task/"clock.jsonl").write_text(json.dumps(dict(samples=samples))+"\n", encoding="utf-8")
                    return dict(state="launched")

                with mock.patch("prepare.time.time", return_value=moment), \
                        mock.patch("cli.launch", side_effect=start), \
                        mock.patch("worker.inspect", return_value=dict(state=state)):
                    if state == "running":
                        self.assertEqual(launch(root)["state"], "launched")
                        self.assertEqual(called, ["clock-task", "capture-task"])
                    else:
                        with self.assertRaisesRegex(ValueError, "不启动录制"):
                            launch(root)
                        self.assertEqual(called, ["clock-task"])


if __name__ == "__main__":
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
