"""用合成正文测试软件守卫；历史结论须另读冻结官方原件和独立Go复核。"""
import copy
import io
import json
from pathlib import Path
import sys
import unittest

import build


def html(text):
    return ('<div class="notice_contain"><p>' + text + '</p></div>').encode()


class ListedCalendarTest(unittest.TestCase):
    def setUp(self):
        self.facts = json.loads((Path(__file__).resolve().parents[3] /
                                 'docs/evidence/listed2023-facts.json').read_text(encoding='utf-8'))
        self.raw = {}
        for p in self.facts['profiles'].values():
            self.raw[p['listing']] = html(''.join(p['listing_tokens']))
        for role in ['shfe-year-2023', 'ine-year-2023-repub']:
            self.raw[role] = html(''.join(self.facts['annual_tokens']))
        for role, tokens in self.facts['notice_reviews'].items():
            self.raw[role] = html(''.join(tokens))
        for review in self.facts['rule_reviews'].values():
            self.raw[review['text_source']] = ''.join(review['tokens']).encode()
        for role in self.facts['search_roles']:
            self.raw[role] = b'{"code":20000,"data":{"content":[{}],"totalElements":1}}'

    def test_real_boundary_expectations(self):
        result = build.generate(self.facts, self.raw)
        rows = {s['product']['Product']: {r['day']: r for r in s['records']} for s in result['series']}
        self.assertEqual(result['summary']['INE.ec']['trading_days'], 90)
        self.assertEqual(result['summary']['SHFE.ao']['trading_days'], 132)
        self.assertEqual(result['summary']['SHFE.br']['trading_days'], 105)
        self.assertNotIn(20230618, rows['ao'])
        self.assertEqual(len(rows['ao'][20230619]['sessions']), 3)
        self.assertEqual(len(rows['ao'][20230626]['sessions']), 3)
        self.assertEqual(rows['ao'][20230703]['sessions'][0], {
            'Start': build.stamp(build.date(20230630), 1260),
            'End': build.stamp(build.date(20230701), 60)})
        self.assertEqual(rows['ao'][20231008]['sessions'], [])
        self.assertEqual(len(rows['br'][20231009]['sessions']), 3)
        self.assertEqual(rows['ec'][20231120]['template']['From'], 20230818)
        self.assertEqual(rows['ec'][20231121]['template']['From'], 20231121)
        self.assertEqual(rows['ec'][20231121]['template']['Night'], [])

    def test_fact_mutations_hit_target_guard(self):
        cases = [
            ('day_hours', lambda f: f['day_minutes'][0].__setitem__(1, 616)),
            ('holiday_profile', lambda f: f['closed_ranges'].pop()),
            ('holiday_profile', lambda f: f['no_night_after'].pop()),
            ('profile_scope', lambda f: f['profiles']['SHFE.ao'].__setitem__('from', 20230616)),
            ('profile_scope', lambda f: f['profiles']['INE.ec'].__setitem__('night_minutes', [[1260, 1380]])),
            ('effective_version', lambda f: f['profiles']['INE.ec']['versions'][0].__setitem__(0, 20230811)),
            ('effective_version', lambda f: f['profiles']['INE.ec']['versions'][1].__setitem__(0, 20231117)),
            ('rule_coverage', lambda f: f['rule_reviews'].pop('shfe-night-docx')),
            ('rule_origin', lambda f: f['rule_reviews']['shfe-night-docx'].__setitem__('raw_sha256', 'a'*64)),
        ]
        for reason, change in cases:
            with self.subTest(reason=reason):
                facts = copy.deepcopy(self.facts)
                change(facts)
                with self.assertRaisesRegex(ValueError, reason):
                    build.generate(facts, self.raw)

    def test_original_evidence_refusals(self):
        cases = [('annual_evidence', 'shfe-year-2023', html('2023')),
                 ('special_notice', 'shfe-dragon-2023', html('not a notice')),
                 ('listing_evidence', 'ec-listing-repub', html('09:00')),
                 ('rule_clause', 'ine-trade-202308-doc-text', b'2023'),
                 ('search_incomplete', self.facts['search_roles'][0],
                  b'{"code":20000,"data":{"content":[],"totalElements":1}}')]
        for reason, role, body in cases:
            with self.subTest(reason=reason):
                raw = dict(self.raw, **{role: body})
                with self.assertRaisesRegex(ValueError, reason):
                    build.generate(self.facts, raw)


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
