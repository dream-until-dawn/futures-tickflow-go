"""生成ec/ao/br上市当年有据日历；仅离线，范围外不推测。"""
import argparse
import datetime as dt
import io
import json
from pathlib import Path
import re
import sys

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from official_text import article, date, number, stamp, normalize, load_sources

# 这是复核后的特定批次，不是允许任意参数自行认证历史的通用模板。
PROFILES = {'INE.ec': (20230818, []), 'SHFE.ao': (20230619, [[1260, 1500]]),
            'SHFE.br': (20230728, [[1260, 1380]])}
DAY = [[540, 615], [630, 690], [810, 900]]


def validate(facts, raw):
    if facts['schema'] != 'listed2023-facts-v1' or set(facts['profiles']) != set(PROFILES):
        raise ValueError('profile_identity')
    if facts['day_minutes'] != DAY:
        raise ValueError('day_hours')
    # 两个交易场所各自有年度原件；不因同属一集团就借用另一所日历。
    for role in ['shfe-year-2023', 'ine-year-2023-repub']:
        body = re.sub(r'（[^）]*）', '', article(raw[role]))
        for token in facts['annual_tokens']:
            if token not in body:
                raise ValueError('annual_evidence:' + role)
    expected_holidays = [[20221231, 20230102], [20230121, 20230127], [20230405, 20230405],
                         [20230429, 20230503], [20230622, 20230624], [20230929, 20231006]]
    if facts['closed_ranges'] != expected_holidays or facts['no_night_after'] != [20230621, 20230928, 20231229]:
        raise ValueError('holiday_profile')
    for role, tokens in facts['notice_reviews'].items():
        text = re.sub(r'（[^）]*）', '', article(raw[role]))
        if not all(token in text for token in tokens):
            raise ValueError('special_notice:' + role)
    for key, (start, night) in PROFILES.items():
        p = facts['profiles'][key]
        if p['from'] != start or p['to'] != 20231231 or p['night_minutes'] != night:
            raise ValueError('profile_scope:' + key)
        listing = article(raw[p['listing']])
        if not all(token in listing for token in p['listing_tokens']):
            raise ValueError('listing_evidence:' + key)
        expected_versions = ([[20230818, 20231120, 'ine-trade-202308-doc'],
                              [20231121, 20231231, 'ine-trade-202311-docx']] if key == 'INE.ec' else
                             [[20230526, 20231231, 'shfe-trade-docx']])
        if p['versions'] != expected_versions:
            raise ValueError('effective_version:' + key)
    expected_rules = {'shfe-trade-docx', 'shfe-night-docx', 'ine-trade-202308-doc', 'ine-trade-202311-docx'}
    if set(facts['rule_reviews']) != expected_rules:
        raise ValueError('rule_coverage')
    for role, review in facts['rule_reviews'].items():
        text = normalize(raw[review['text_source']].decode('utf-8'))
        if not all(token in text for token in review['tokens']):
            raise ValueError('rule_clause:' + role)
        if facts['sources'][role]['ref']['sha256'] != review['raw_sha256']:
            raise ValueError('rule_origin:' + role)
    for role in facts['search_roles']:
        obj = json.loads(raw[role])
        if obj['code'] != 20000 or len(obj['data']['content']) != obj['data']['totalElements']:
            raise ValueError('search_incomplete:' + role)


def generate(facts, raw):
    validate(facts, raw)
    holidays = [(date(a), date(b)) for a, b in facts['closed_ranges']]

    def closed(day):
        return day.weekday() >= 5 or any(a <= day <= b for a, b in holidays)

    series, counts = [], {}
    stops = set(facts['no_night_after'])
    for key, p in facts['profiles'].items():
        rows, first, previous = [], True, None
        current, end = date(p['from']), date(p['to'])
        open_days, day_only = 0, []
        while current <= end:
            value = number(current)
            vf, vt, rule = next(v for v in p['versions'] if v[0] <= value <= v[1])
            template = {'From': vf, 'To': vt,
                        'Day': [{'Start': a*60000, 'End': b*60000} for a, b in DAY],
                        'Night': [{'Start': a*60000, 'End': b*60000} for a, b in p['night_minutes']]}
            sessions = []
            if not closed(current):
                open_days += 1
                # 上市首日明确09:00开盘；不能凭标称夜盘倒造上市前一晚。
                if p['night_minutes'] and not first and number(previous) not in stops:
                    sessions.extend({'Start': stamp(previous, a), 'End': stamp(previous, b)}
                                    for a, b in p['night_minutes'])
                elif p['night_minutes']:
                    day_only.append(value)
                sessions.extend({'Start': stamp(current, a), 'End': stamp(current, b)} for a, b in DAY)
                first, previous = False, current
            roles = [p['listing'], p['annual'], rule, p['rule_notice']]
            if p['night_minutes']:
                roles += ['shfe-night-docx', 'shfe-rules-202304', 'shfe-dragon-2023',
                          'shfe-autumn-2023', 'shfe-boundary-2024']
            else:
                roles += ['ine-autumn-2023-repub', 'ine-boundary-2024-repub']
                if value >= 20231121:
                    roles.append('ine-rules-202311-repub')
            refs = [facts['sources'][role]['ref'] for role in dict.fromkeys(roles)]
            rows.append({'day': value, 'sessions': sessions, 'template': template, 'evidence': refs})
            current += dt.timedelta(days=1)
        exchange, product = key.split('.')
        series.append({'product': {'Exchange': exchange, 'Product': product}, 'records': rows})
        counts[key] = {'from': p['from'], 'to': p['to'], 'natural_days': len(rows),
                       'trading_days': open_days, 'night_product_day_only': day_only}
    return {'schema': 'listed2023-calendar-v1', 'scope': 'published continuous trading schedules only; no auction or intraday-halt exhaustiveness',
            'series': series, 'summary': counts, 'sources': {k: v['ref'] for k, v in facts['sources'].items()}}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ['facts', 'evidence-dir', 'out']:
        parser.add_argument('--' + name, required=True, type=Path)
    args = parser.parse_args()
    facts = json.loads(args.facts.read_text(encoding='utf-8'))
    result = generate(facts, load_sources(facts, args.evidence_dir))
    with args.out.open('x', encoding='utf-8', newline='\n') as stream:
        json.dump(result, stream, ensure_ascii=False, indent=2)
        stream.write('\n')
    print(json.dumps(result['summary'], ensure_ascii=False))


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    main()
