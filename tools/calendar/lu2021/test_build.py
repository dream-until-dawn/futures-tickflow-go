"""用合成正文回归解析与拒绝路径；历史事实由原件和独立Go复核认证。"""
import copy
import hashlib
import json
import io
from pathlib import Path
import sys
import tempfile
import unittest

import build


def html(text):
    return ('<nav>忽略导航</nav><div class="notice_contain"><p>' + text +
            '</p><br/><p>正文结束</p></div>').encode()


class CalendarBuildTest(unittest.TestCase):
    def setUp(self):
        self.facts = json.loads((Path(__file__).resolve().parents[3] /
                                 'docs/evidence/lu2021-facts.json').read_text(encoding='utf-8'))
        self.raw = {'lu-listing-primary': html('上能发〔2020〕114号09:00-10:15 '
                    '10:30-11:30 13:30-15:00 21:00-23:00 不包含周六和周日'),
                    'holiday-newyear-2022-boundary': html('2021年12月31日晚上不进行夜盘交易')}
        annual = '〔2020〕73号'
        for event in self.facts['holidays']:
            a, b, resume, stop = [build.date(event[k]) for k in
                                  ['from', 'to', 'resume', 'no_night_after']]
            annual += f'{a.month}月{a.day}日至{b.month}月{b.day}日休市；{resume.month}月{resume.day}日照常开市。'
            self.raw[event['source']] = html('上海国际能源交易中心集合竞价' +
                                            f'{stop.month}月{stop.day}日晚上不进行夜盘交易')
        self.raw['holiday-year-2021'] = html(annual)
        for review in self.facts['rule_reviews']:
            self.raw[review['text_source']] = (review['version_token'] + review['session_clause']).encode()
        for role in ['rules-bc-202011', 'rules-sc-202106', 'rules-202111-official']:
            self.raw[role] = html('自发布之日起实施')
        for role in self.facts['search_roles']:
            self.raw[role] = b'{"code":20000,"data":{"content":[{}],"totalElements":1}}'

    def test_boundaries_and_friday_night(self):
        result = build.generate(self.facts, self.raw)
        rows = {r['day']: r for r in result['series'][0]['records']}
        self.assertEqual(result['summary']['trading_days'], 243)
        self.assertEqual(len(rows), 365)
        self.assertEqual(len(rows[20210111]['sessions']), 4)
        self.assertEqual(rows[20210111]['sessions'][0]['Start'],
                         build.stamp(build.date(20210108), 1260))
        self.assertEqual(len(rows[20210218]['sessions']), 3)
        self.assertEqual(rows[20210220]['sessions'], [])  # 政务补班周六仍不交易。
        self.assertEqual(rows[20211007]['sessions'], [])
        self.assertEqual(rows[20211008]['sessions'][0]['Start'],
                         build.stamp(build.date(20211008), 540))

    def test_profile_and_holiday_mutations(self):
        cases = [
            ('unsupported_hours', lambda f: f['day_minutes'][0].__setitem__(0, 541)),
            ('holiday_count', lambda f: f['holidays'].pop()),
            ('holiday_duplicate', lambda f: f['holidays'].__setitem__(1, f['holidays'][0])),
            ('holiday_range', lambda f: f['holidays'][1].__setitem__('to', 20210216)),
            ('no_night_evidence', lambda f: f['holidays'][1].__setitem__('no_night_after', 20210209)),
            ('resume_evidence', lambda f: f['holidays'][1].__setitem__('resume', 20210219)),
            ('version_partition', lambda f: f['versions'][1].__setitem__(0, 20210612)),
            ('rule_review_coverage', lambda f: f['rule_reviews'].pop()),
        ]
        for reason, change in cases:
            with self.subTest(reason=reason):
                facts = copy.deepcopy(self.facts)
                change(facts)
                with self.assertRaisesRegex(ValueError, reason):
                    build.generate(facts, self.raw)

    def test_rule_search_and_boundary_refusal(self):
        cases = [('rule_clause', self.facts['rule_reviews'][0]['text_source'], b'wrong'),
                 ('search_incomplete', self.facts['search_roles'][0],
                  b'{"code":20000,"data":{"content":[],"totalElements":1}}'),
                 ('year_end_boundary', 'holiday-newyear-2022-boundary', html('晚上不进行夜盘交易'))]
        for reason, role, content in cases:
            with self.subTest(reason=reason):
                raw = dict(self.raw, **{role: content})
                with self.assertRaisesRegex(ValueError, reason):
                    build.generate(self.facts, raw)

    def test_source_hash_and_path(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            raw = b'original'
            (root / 'a.body').write_bytes(raw)
            facts = {'sources': {'a': {'file': 'a.body', 'bytes': len(raw),
                     'ref': {'sha256': hashlib.sha256(raw).hexdigest()}}}}
            self.assertEqual(build.load_sources(facts, root), {'a': raw})
            (root / 'a.body').write_bytes(b'tampered')
            with self.assertRaisesRegex(ValueError, 'source_hash:a'):
                build.load_sources(facts, root)
            facts['sources']['a']['file'] = '../a.body'
            with self.assertRaisesRegex(ValueError, 'unsafe_source_path'):
                build.load_sources(facts, root)

    def test_challenge_not_article(self):
        with self.assertRaisesRegex(ValueError, 'missing_article'):
            build.article(b'<html>Access denied 2021 21:00-23:00</html>')
        self.assertEqual(build.article(html('甲<br/>乙')), '甲乙正文结束')
        self.assertEqual(build.article(html('<style>伪日期20240101</style>甲<script>伪条款</script>乙')),
                         '甲乙正文结束')


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
