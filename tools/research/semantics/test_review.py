"""可拟合不能结案，非整数或负事件量必须拒绝。"""
import io
import sys
import unittest

from review import forum_hypothesis_fit, zero_quote_matches


class SemanticsHypothesisTest(unittest.TestCase):
    def test_integer_fit_is_not_independent_evidence(self):
        r = forum_hypothesis_fit(100,109,50,51)
        self.assertTrue(r['nonnegative_integer_solution'])
        self.assertEqual((r['implied_exercise'],r['implied_self_hedge']),(5,4))
        self.assertFalse(r['independent_event_evidence'])
        self.assertFalse(r['difference_resolved'])

    def test_negative_and_half_event_solutions_rejected(self):
        for values in [(100,100,50,48),(100,102,50,51),(100,99,50,50)]:
            r = forum_hypothesis_fit(*values)
            self.assertFalse(r['nonnegative_integer_solution'])
            self.assertIsNone(r['implied_exercise'])
        with self.assertRaisesRegex(ValueError,'^quantity_nonnegative_integer$'):
            forum_hypothesis_fit(1.5,2,0,0)

    def test_equal_settlement_does_not_prove_fill_rule(self):
        actual=dict(open='10',high='10',low='10',close='10',volume='0')
        ref=dict(open='0',high='0',low='0',close='0',volume='0',pre_settle='10',settle='11')
        r=zero_quote_matches(actual,ref)
        self.assertTrue(r['equals_reference_pre_settle'])
        self.assertFalse(r['equals_reference_settle'])
        self.assertFalse(r['fill_rule_proven'])
        ref['volume']='1'
        with self.assertRaisesRegex(ValueError,'^not_reported_zero_volume_and_zero_quote$'):
            zero_quote_matches(actual,ref)


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
