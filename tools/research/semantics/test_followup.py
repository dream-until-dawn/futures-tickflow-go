"""负控必须命中原件身份、生效日冲突或条文语义，不能靠其他失败冒充。"""
import io
import sys
import tempfile
import unittest
from pathlib import Path
from followup import prior_rule_facts, review


class PriorRuleTest(unittest.TestCase):
    def fixture(self):
        return ['2019年10月16日 2019年10月29日 87号文件发布 自2020年1月1日起施行',
                '第二十六条 最后一笔成交价格 最高成交价格 最低成交价格 单边数量 第一笔成交价 撮合原则'] + ['']*8 + ['第四十七条 自2020年1月1日起施行']

    def test_prior_document_is_not_day_break_or_continuity_evidence(self):
        r = prior_rule_facts(self.fixture())
        self.assertEqual(r['effective_day'], 20200101)
        self.assertTrue(r['ohlc_defined_from_trades'])
        self.assertFalse(r['day_session_break_clause_found'])
        self.assertFalse(r['pk_initial_rule_gap_closed'])

    def test_both_effective_clauses_must_agree(self):
        p = self.fixture(); p[-1] = p[-1].replace('1月1日', '2月1日')
        with self.assertRaisesRegex(ValueError, '^prior_rule_effective_conflict$'):
            prior_rule_facts(p)

    def test_trade_definition_and_absent_break_are_checked_separately(self):
        p = self.fixture(); p[1] = p[1].replace('单边数量', '双边数量')
        with self.assertRaisesRegex(ValueError, '^prior_rule_marker:单边数量$'):
            prior_rule_facts(p)
        p = self.fixture(); p[3] = '10:15-10:30'
        with self.assertRaisesRegex(ValueError, '^unexpected_day_session_clause$'):
            prior_rule_facts(p)

    def test_changed_raw_rule_rejected_before_extraction(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d); (root/'rule2019.pdf').write_bytes(b'%PDF changed')
            with self.assertRaisesRegex(ValueError, '^prior_rule_sha$'):
                review(root)


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
