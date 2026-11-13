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

    def test_listing_partial_coverage_and_first_night(self):
        facts = copy.deepcopy(self.facts)
        p = facts['profiles']['SHFE.ao']
        p['from'] = p['listed'] = 20240612
        result = build.generate(facts, self.raw, 'test')
        rows = result['series'][0]['records']
        self.assertEqual(rows[0]['day'], 20240612)
        self.assertEqual(len(rows[0]['sessions']), 3)
        self.assertEqual(len(rows[1]['sessions']), 4)
        self.assertEqual(rows[1]['sessions'][0]['Start'], build.stamp(build.date(20240612), 1260))
        p['from'] = 20240611
        with self.assertRaisesRegex(ValueError, 'profile_listing'):
            build.generate(facts, self.raw, 'test')

        p['from'] = 20240613
        with self.assertRaisesRegex(ValueError, 'profile_previous_day'):
            build.generate(facts, self.raw, 'test')
        p['previous_trading_day'] = 20240612
        self.assertEqual(build.generate(facts, self.raw, 'test')['series'][0]['records'][0]['day'], 20240613)
        p['previous_trading_day'] = 20240613
        with self.assertRaisesRegex(ValueError, 'profile_previous_day'):
            build.generate(facts, self.raw, 'test')
        p['to'] = 20250101
        with self.assertRaisesRegex(ValueError, 'profile_coverage'):
            build.generate(facts, self.raw, 'test')

    def test_partial_year_does_not_invent_future_boundary(self):
        facts = copy.deepcopy(self.facts)
        del facts['year_end_no_night_after']
        del facts['year_end_source']
        with self.assertRaisesRegex(ValueError, 'missing_year_boundary'):
            build.generate(facts, self.raw, 'test')
        for p in facts['profiles'].values():
            p['to'] = 20241104
        result = build.generate(facts, self.raw, 'test')
        self.assertEqual(result['series'][0]['records'][-1]['day'], 20241104)

    def test_ine_day_only_product_and_night_boundaries(self):
        root = Path(__file__).resolve().parents[3]
        source = (root / 'docs/evidence/ine-2025-facts.json').read_bytes()
        review = json.loads((root / 'docs/evidence/ine-2025-review.json').read_bytes())
        facts = build.checked_facts(source, review)
        # 合成正文只测生成器；原件SHA及外部序号由独立Go审核器验证。
        texts = {}
        for r in facts['reviews']:
            texts[r['source']] = texts.get(r['source'], '') + ''.join(r['tokens'])
        raw = {role: ('<div class="notice_contain">' + text + '</div>').encode()
               if any(r['source'] == role and r['kind'] == 'article' for r in facts['reviews'])
               else text.encode() for role, text in texts.items()}
        result = build.generate(facts, raw, hashlib.sha256(source).hexdigest())
        rows = {s['product']['Product']: {r['day']: r for r in s['records']} for s in result['series']}
        self.assertEqual(len(result['summary']['INE.ec']['day_only_trade_days']), 243)
        self.assertEqual(result['summary']['INE.lu']['day_only_trade_days'],
                         [20250102, 20250205, 20250407, 20250506, 20250603, 20251009])
        self.assertEqual(rows['ec'][20250106]['template']['Night'], [])
        self.assertEqual(len(rows['ec'][20250106]['sessions']), 3)
        self.assertEqual(rows['lu'][20250106]['sessions'][0]['Start'], build.stamp(build.date(20250103), 1260))
        self.assertEqual(len(rows['lu'][20250205]['sessions']), 3)
        self.assertEqual(rows['ec'][20250126]['sessions'], [])
        self.assertEqual(rows['lu'][20251231]['template']['From'], 20231121)
        raw['ine-year-2025'] = b'<div class="notice_contain">unrelated</div>'
        with self.assertRaisesRegex(ValueError, 'review_clause:ine-year-2025'):
            build.generate(facts, raw, 'test')


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
