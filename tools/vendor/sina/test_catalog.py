import unittest
import io
import sys
from catalog import product_map, list_jobs
from observations import contract_observations


class CatalogTest(unittest.TestCase):
    def raw(self, suffix=""):
        return ("ARRFUTURESNODES = {dce:['大商所',['玉米','zly_qh','0']],gfex:['广期所',['工业硅','si_qh','20','gfuturesindex']],};" + suffix).encode("gb18030")

    def test_inert_product_constant_and_precise_plan(self):
        jobs, rows = list_jobs(self.raw("evil();"))
        self.assertEqual(2, len(jobs))
        self.assertIn("node=zly_qh&base=futures", jobs[0]["url"])
        self.assertEqual("DCE", rows[0]["exchange"])
        self.assertFalse(rows[0]["historical_roster_complete"])
        self.assertEqual("工业硅", product_map(self.raw())["gfex"][1][0])

    def test_invalid_encoding_is_not_replaced(self):
        with self.assertRaises(UnicodeDecodeError):
            product_map(self.raw().replace("玉米".encode("gb18030"), b"\xff"))

    def test_reject_expression_duplicate_node_and_structure(self):
        for raw in (self.raw().replace(b"'zly_qh'", b"call()"), self.raw().replace(b"si_qh", b"zly_qh"), self.raw().replace(b"dce:", b"other:"), self.raw().replace(b"};", b"}"), self.raw().replace(b"gfex:", b"dce:")):
            with self.assertRaises(ValueError):
                list_jobs(raw)

    def test_contracts_keep_only_observed_real_codes(self):
        contracts, excluded = contract_observations('[{"symbol":"C0","exchange":"dce"},{"symbol":"C2701","exchange":"dce","tradedate":"2026-11-16"}]', "DCE")
        self.assertEqual(["C0"], excluded)
        self.assertEqual("C2701", contracts[0]["code"])
        for raw in ('[]', '[{"symbol":"C2713","exchange":"dce"}]', '[{"symbol":"C2701","exchange":"gfex"}]', '[{"symbol":"C2701","symbol":"C2703","exchange":"dce"}]', '[{"symbol":"C2701","exchange":"dce"},{"symbol":"C2701","exchange":"dce"}]'):
            with self.assertRaises(ValueError):
                contract_observations(raw, "DCE")


if __name__ == "__main__":
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
