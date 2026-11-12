"""从冻结官方事实生成LU 2021逐日日历；只离线读取，不自动放入内置日历。"""
import argparse
import datetime as dt
import hashlib
import io
import json
from pathlib import Path
import re
import sys
from html.parser import HTMLParser

ZONE = dt.timezone(dt.timedelta(hours=8))
VOID = {'br', 'hr', 'img', 'input', 'meta', 'link'}


class Article(HTMLParser):
    """只读已知官网正文容器；未知模板拒绝，避免把导航或挑战页当正文。"""
    def __init__(self):
        super().__init__()
        self.depth, self.parts = 0, []

    def handle_starttag(self, tag, attrs):
        classes = set(dict(attrs).get('class', '').split())
        if not self.depth and classes & {'notice_contain', 'TRS_Editor', 'm_content_test'}:
            self.depth = 1
        elif self.depth and tag not in VOID:
            self.depth += 1

    def handle_endtag(self, tag):
        if self.depth and tag not in VOID:
            self.depth -= 1

    def handle_data(self, data):
        if self.depth:
            self.parts.append(data)


def normalize(text):
    return re.sub(r'\s+', '', text)


def article(raw):
    reader = Article()
    reader.feed(raw.decode('utf-8'))
    text = normalize(''.join(reader.parts))
    if not text:
        raise ValueError('missing_article')
    return text


def date(number):
    return dt.datetime.strptime(str(number), '%Y%m%d').date()


def number(day):
    return int(day.strftime('%Y%m%d'))


def stamp(day, minute):
    return int(dt.datetime.combine(day, dt.time(), ZONE).timestamp() * 1000) + minute * 60000


def load_sources(facts, root):
    """源锁独立于回执，逐字节检查；本工具不认证任意新源锁的事实质量。"""
    result = {}
    for name, source in facts['sources'].items():
        filename = source['file']
        if Path(filename).name != filename:
            raise ValueError('unsafe_source_path')
        raw = (root / filename).read_bytes()
        if len(raw) != source['bytes'] or hashlib.sha256(raw).hexdigest() != source['ref']['sha256']:
            raise ValueError('source_hash:' + name)
        result[name] = raw
    return result


def validate(facts, raw):
    """对照原公告和已独立复核的规则文本，不接受静默改时段或遗漏假期。"""
    if facts['schema'] != 'lu2021-facts-v1' or facts['year'] != 2021:
        raise ValueError('wrong_profile')
    if facts['day_minutes'] != [[540, 615], [630, 690], [810, 900]] or facts['night_minutes'] != [[1260, 1380]]:
        raise ValueError('unsupported_hours')
    listing = article(raw['lu-listing-primary'])
    for token in ['上能发〔2020〕114号', '09:00-10:15', '10:30-11:30', '13:30-15:00', '21:00-23:00', '不包含周六和周日']:
        if token not in listing:
            raise ValueError('listing_hours_evidence')
    if len(facts['holidays']) != 7:
        raise ValueError('holiday_count')
    if len({event['name'] for event in facts['holidays']}) != 7:
        raise ValueError('holiday_duplicate')
    annual = article(raw['holiday-year-2021'])
    for event in facts['holidays']:
        body = article(raw[event['source']])
        for token in event['annual_tokens']:
            if token not in annual:
                raise ValueError('annual_holiday_evidence')
        for token in event['notice_tokens']:
            if token not in body:
                raise ValueError('holiday_notice_evidence')
        # 日期也必须绑定到公告的完整范围句，而非只在全文找到孤立数字。
        a, b = date(event['from']), date(event['to'])
        compact = re.sub(r'（[^）]*）', '', annual)
        pattern = rf'(?:2021年)?{a.month}月{a.day}日至(?:{b.month}月)?{b.day}日休市'
        if not re.search(pattern, compact):
            raise ValueError('holiday_range')
        stop = date(event['no_night_after'])
        token = f'{stop.month}月{stop.day}日晚上不进行夜盘交易'
        if token not in re.sub(r'（[^）]*）', '', body):
            raise ValueError('no_night_evidence')
        resume = date(event['resume'])
        if not re.search(rf'{resume.month}月{resume.day}日.*?照常开市', compact):
            raise ValueError('resume_evidence')
    if facts['versions'] != [[20210101, 20210610, 'trade-rule-202011-doc'],
                             [20210611, 20211125, 'trade-rule-202106-doc'],
                             [20211126, 20211231, 'trade-rule-202111-doc']]:
        raise ValueError('version_partition')
    if [r['raw_source'] for r in facts['rule_reviews']] != [v[2] for v in facts['versions']]:
        raise ValueError('rule_review_coverage')
    for review in facts['rule_reviews']:
        text = normalize(raw[review['text_source']].decode('utf-8'))
        if review['version_token'] not in text or review['session_clause'] not in text:
            raise ValueError('rule_clause')
        if facts['sources'][review['raw_source']]['ref']['sha256'] != review['raw_sha256']:
            raise ValueError('rule_origin')
    for role in ['rules-bc-202011', 'rules-sc-202106', 'rules-202111-official']:
        if '自发布之日起实施' not in article(raw[role]):
            raise ValueError('rule_effective_event')
    # 该搜索审计是已读官网索引的有界结果，不是未公开措施不存在的证明。
    for role in facts['search_roles']:
        obj = json.loads(raw[role])
        if obj['code'] != 20000 or len(obj['data']['content']) != obj['data']['totalElements']:
            raise ValueError('search_incomplete')


def generate(facts, raw):
    validate(facts, raw)
    closed = set()
    stops = set()
    for item in facts['holidays']:
        a, b = date(item['from']), date(item['to'])
        while a <= b:
            closed.add(a)
            a += dt.timedelta(days=1)
        stops.add(date(item['no_night_after']))
    # 年末晚间属于下一交易年；保留该明确停夜盘事实，但不扩展2022覆盖。
    boundary = article(raw['holiday-newyear-2022-boundary'])
    if '2021年12月31日' not in boundary or '晚上不进行夜盘交易' not in boundary:
        raise ValueError('year_end_boundary')
    stops.add(date(20211231))
    refs = [value['ref'] for key, value in facts['sources'].items()
            if key in ['lu-listing-primary', 'holiday-year-2021']]
    rows, trading, no_night = [], 0, []
    previous = date(20201231)
    for offset in range(365):
        day = date(20210101) + dt.timedelta(days=offset)
        current = number(day)
        start, end, role = next(v for v in facts['versions'] if v[0] <= current <= v[1])
        template = {'From': start, 'To': end,
                    'Day': [{'Start': a*60000, 'End': b*60000} for a, b in facts['day_minutes']],
                    'Night': [{'Start': a*60000, 'End': b*60000} for a, b in facts['night_minutes']]}
        sessions = []
        if day.weekday() < 5 and day not in closed:
            trading += 1
            if previous not in stops:
                sessions.extend({'Start': stamp(previous, a), 'End': stamp(previous, b)} for a, b in facts['night_minutes'])
            else:
                no_night.append(current)
            sessions.extend({'Start': stamp(day, a), 'End': stamp(day, b)} for a, b in facts['day_minutes'])
            previous = day
        evidence = refs + [facts['sources'][role]['ref']]
        evidence += [facts['sources'][e['source']]['ref'] for e in facts['holidays']
                     if date(e['from']) <= day <= date(e['resume'])]
        rows.append({'day': current, 'sessions': sessions, 'template': template, 'evidence': evidence})
    return {'schema': 'lu2021-calendar-v1', 'scope': 'official published schedule reconstruction; no tick-completeness or price certification',
            'series': [{'product': {'Exchange': 'INE', 'Product': 'lu'}, 'records': rows}],
            'summary': {'natural_days': 365, 'trading_days': trading, 'day_only_trade_days': no_night,
                        'no_night_after': sorted(number(d) for d in stops)},
            'sources': {k: v['ref'] for k, v in facts['sources'].items()}}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--facts', type=Path, required=True)
    parser.add_argument('--evidence-dir', type=Path, required=True)
    parser.add_argument('--out', type=Path, required=True)
    args = parser.parse_args()
    facts = json.loads(args.facts.read_text(encoding='utf-8'))
    value = generate(facts, load_sources(facts, args.evidence_dir))
    with args.out.open('x', encoding='utf-8', newline='\n') as stream:
        json.dump(value, stream, ensure_ascii=False, indent=2)
        stream.write('\n')
    print(json.dumps(value['summary'], ensure_ascii=False))


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    main()
