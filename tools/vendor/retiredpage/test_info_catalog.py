"""纯离线目录常量审核正反例，不运行原脚本。"""
from pathlib import Path
import unittest
from info_catalog import review


class InfoCatalogTest(unittest.TestCase):
    def fixture(self):
        return (Path(__file__).resolve().parents[3] / "docs/evidence/sina-info-https-20261115/info.jsonp").read_bytes()

    def test_real_frozen_navigation_is_not_historical_roster(self):
        result = review(self.fixture())
        self.assertEqual(result["rows_by_market"]["大商所"], 17)
        self.assertNotIn("广期所", result["rows_by_market"])
        self.assertEqual(result["target_market_real_codes"], [])
        self.assertFalse(result["historical_roster_proved"])
        self.assertGreater(result["real_month_code_rows"], 0)  # 金融代码存在不应误认为商品合约。

    def test_executable_or_unrecognized_statement_rejected(self):
        for tail in (b"\nfetch('https://example.com');", b"\nYF[9] = evil();"):
            with self.assertRaisesRegex(ValueError, "未知"):
                review(self.fixture() + tail)

    def test_duplicate_and_missing_parent_rejected(self):
        with self.assertRaisesRegex(ValueError, "重复"):
            review(self.fixture() + b'\nJYS[0] = "again";')
        with self.assertRaisesRegex(ValueError, "父节点"):
            review(self.fixture() + b'\nYF[99][99][0] = new Array("fake","A2101");')

    def test_bad_encoding_and_empty_directory_rejected(self):
        with self.assertRaises(UnicodeDecodeError):
            review(self.fixture() + b"\xff")
        with self.assertRaisesRegex(ValueError, "不完整"):
            review(b"var JYS = new Array;")


if __name__ == "__main__":
    import io
    import sys
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
