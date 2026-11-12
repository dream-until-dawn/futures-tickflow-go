"""年度编译器正反控制；合成正文只测软件，历史结论由冻结原件和Go独立核查建立。"""
import copy
import hashlib
import io
import json
from pathlib import Path
import sys
import unittest

import build
from observations import record


class AnnualBuildTest(unittest.TestCase):
    def setUp(self):
        root = Path(__file__).resolve().parents[3]
        self.source = (root / 'docs/evidence/annual-2024-facts.json').read_bytes()
        self.review = json.loads((root / 'docs/evidence/annual-2024-review.json').read_bytes())
        self.facts = build.checked_facts(self.source, self.review)
        texts = {}
        for r in self.facts['reviews']:
            texts[r['source']] = texts.get(r['source'], '') + ''.join(r['tokens'])
        self.raw = {role: ('<div class="notice_contain">' + text + '</div>').encode()
                    if any(r['source'] == role and r['kind'] == 'article' for r in self.facts['reviews'])
                    else text.encode() for role, text in texts.items()}

    def test_leap_day_first_night_and_rule_cutover(self):
        result = build.generate(self.facts, self.raw, hashlib.sha256(self.source).hexdigest())
        rows = {s['product']['Product']: {r['day']: r for r in s['records']} for s in result['series']}
        self.assertEqual(set(rows), {'ao', 'br'})
        self.assertEqual(result['summary']['SHFE.ao']['trading_days'], 242)
        self.assertEqual(len(rows['ao']), 366)
        self.assertEqual(len(rows['ao'][20240102]['sessions']), 3)
        self.assertEqual(len(rows['ao'][20240229]['sessions']), 4)
        self.assertEqual(rows['ao'][20240218]['sessions'], [])
        self.assertEqual(rows['ao'][20240108]['sessions'][0], {
            'Start': build.stamp(build.date(20240105), 1260),
            'End': build.stamp(build.date(20240106), 60)})
        self.assertEqual(rows['ao'][20241022]['template']['From'], 20230526)
        self.assertEqual(rows['ao'][20241023]['template']['From'], 20241023)

    def test_fact_pin_and_qualification_scope(self):
        with self.assertRaisesRegex(ValueError, 'facts_pin'):
            build.checked_facts(self.source + b' ', self.review)
        review = dict(self.review, qualified_profiles=['INE.ec'])
        with self.assertRaisesRegex(ValueError, 'qualified_profile_mismatch'):
            build.checked_facts(self.source, review)

    def test_review_and_partition_refusal(self):
        raw = dict(self.raw, **{'shfe-management-final': b'not the final rule'})
        with self.assertRaisesRegex(ValueError, 'review_clause'):
            build.generate(self.facts, raw, 'test')
        facts = copy.deepcopy(self.facts)
        facts['profiles']['SHFE.ao']['versions'][1][0] = 20241024
        with self.assertRaisesRegex(ValueError, 'version_partition'):
            build.generate(facts, self.raw, 'test')
        facts = copy.deepcopy(self.facts)
        facts['events'][1]['no_night_after'] = 20240210
        with self.assertRaisesRegex(ValueError, 'event_order'):
            build.generate(facts, self.raw, 'test')

    def test_observation_cell_identity(self):
        self.assertEqual(record(['rb2405', '20240229'], 2024), ('rb2405', 20240229))
        self.assertEqual(record(['螺纹钢', 'rb2405', 20240229.0], 2024), ('rb2405', 20240229))
        self.assertIsNone(record(['ao2405', '20240229'], 2024))
        for values, reason in [(['rb2405', '20230228'], 'observation_year'),
                               (['rb2405', '20240230'], 'range'),
                               (['rb2405', 'rb2410', '20240229'], 'ambiguous_rb_column')]:
            with self.subTest(values=values), self.assertRaisesRegex(ValueError, reason):
                record(values, 2024)


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
