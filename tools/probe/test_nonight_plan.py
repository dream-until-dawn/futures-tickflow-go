"""参数化执行单的独立正反校验；所有公告材料均为合成。"""
import copy
import io
import json
from pathlib import Path
import sys
import tempfile
import unittest

from nonight_plan import validate


class NonightPlanTest(unittest.TestCase):
    def test_positive_and_invalid_matrix(self):
        fixture = Path(__file__).parent/"testdata/nonight-synthetic/plan.json"
        original = json.loads(fixture.read_text(encoding="utf-8"))
        self.assertEqual(validate(fixture, True)["resume_day"], 20261008)
        with self.assertRaises(ValueError): validate(fixture)
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp)/"plan.json"
            for kind in ("valid", "unknown", "dates", "duplicate", "weekend", "bind", "timezone", "window", "symbol", "hash", "url", "class"):
                plan = copy.deepcopy(original)
                plan["evidence_file"] = str((fixture.parent/"evidence.txt").resolve())
                if kind == "unknown": plan["extra"] = 1
                elif kind == "dates": plan["trading_days"][0] = 20260230
                elif kind == "duplicate": plan["trading_days"][0] = 20260930
                elif kind == "weekend": plan["trading_days"][0] = 20260927
                elif kind == "bind": plan["resume_day"] = 20261009
                elif kind == "timezone": plan["evening_begin"] = "2026-09-30T12:59:00Z"
                elif kind == "window": plan["fetch_begin"] = "2026-10-08T09:05:00+08:00"
                elif kind == "symbol": plan["symbol"] = "SHFE.rb2713"
                elif kind == "hash": plan["evidence_sha256"] = "0"*64
                elif kind == "url": plan["official_url"] = "https://www.shfe.cn.evil.example/"
                elif kind == "class": plan["evidence_class"] = "unknown"
                path.write_text(json.dumps(plan), encoding="utf-8")
                with self.subTest(kind=kind):
                    if kind == "valid": self.assertEqual(validate(path, True)["symbol"], "SHFE.rb2701")
                    else:
                        with self.assertRaises(ValueError): validate(path, True)


if __name__ == "__main__":
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
