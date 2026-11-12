"""编译仓库已复核并锁SHA的年度事实；不认证任意外来配置或扩展未知范围。"""
import argparse
import datetime as dt
import hashlib
import io
import json
import re
from pathlib import Path
import sys

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from official_text import article, date, number, stamp, normalize, load_sources


def checked_facts(raw, review):
    """资格来自固定提交及人工原件核查；这一步仅防止事实文件脱离审计记录。"""
    if review['schema'] != 'annual-calendar-review-v1' or review['facts_sha256'] != hashlib.sha256(raw).hexdigest():
        raise ValueError('facts_pin')
    facts = json.loads(raw)
    if sorted(facts['profiles']) != sorted(review['qualified_profiles']):
        raise ValueError('qualified_profile_mismatch')
    return facts


def validate(facts, raw):
    year = facts['year']
    if facts['schema'] != 'annual-calendar-facts-v1' or not 1990 <= year <= 2999:
        raise ValueError('annual_identity')
    start, end = date(year*10000+101), date(year*10000+1231)
    if date(facts['previous_trading_day']) >= start:
        raise ValueError('previous_day')
    for review in facts['reviews']:
        text = re.sub(r'（[^）]*）', '', article(raw[review['source']])) if review['kind'] == 'article' else normalize(raw[review['source']].decode('utf-8'))
        if not review['tokens'] or not all(token in text for token in review['tokens']):
            raise ValueError('review_clause:' + review['source'])
        if 'origin' in review:
            source = facts['sources'][review['origin']]['ref']
            if source['sha256'] != review['origin_sha256']:
                raise ValueError('review_origin')
    # 事件日期必须完整、顺序不交叠；首夜和年界分别有冻结依据。
    last = None
    for event in facts['events']:
        a, b, resume, stop = [date(event[k]) for k in ['from', 'to', 'resume', 'no_night_after']]
        if not stop < a <= b < resume or (last is not None and a <= last):
            raise ValueError('event_order')
        last = b
    date(facts['year_end_no_night_after'])
    for key, p in facts['profiles'].items():
        if len(key.split('.')) != 2 or date(p['listed']) > start:
            raise ValueError('profile_listing')
        for parts in [facts['day_minutes'], p['night_minutes']]:
            previous = -1
            for a, b in parts:
                if not 0 <= a < b <= 2880 or a < previous:
                    raise ValueError('relative_hours')
                previous = b
        if not facts['day_minutes']:
            raise ValueError('empty_day_template')
        previous_end = None
        for a, b, role in p['versions']:
            va, vb = date(a), date(b)
            if va > vb or (previous_end is not None and va != previous_end + dt.timedelta(days=1)):
                raise ValueError('version_partition')
            if role not in facts['sources']:
                raise ValueError('version_source')
            previous_end = vb
        if date(p['versions'][0][0]) > start or previous_end < end:
            raise ValueError('version_coverage')


def generate(facts, raw, facts_sha):
    validate(facts, raw)
    year = facts['year']
    start, end = date(year*10000+101), date(year*10000+1231)
    closed_ranges = [(date(e['from']), date(e['to'])) for e in facts['events']]
    stops = {e['no_night_after'] for e in facts['events']} | {facts['year_end_no_night_after']}

    def closed(day):
        return day.weekday() >= 5 or any(a <= day <= b for a, b in closed_ranges)

    result, summary = [], {}
    for key, p in facts['profiles'].items():
        rows, day_only, count = [], [], 0
        previous = date(facts['previous_trading_day'])
        day = start
        while day <= end:
            value = number(day)
            vf, vt, role = next(v for v in p['versions'] if v[0] <= value <= v[1])
            sessions = []
            if not closed(day):
                count += 1
                if p['night_minutes'] and number(previous) not in stops:
                    sessions += [{'Start': stamp(previous, a), 'End': stamp(previous, b)} for a, b in p['night_minutes']]
                elif p['night_minutes']:
                    day_only.append(value)
                sessions += [{'Start': stamp(day, a), 'End': stamp(day, b)} for a, b in facts['day_minutes']]
                previous = day
            roles = p['base_sources'] + [role]
            roles += p.get('version_sources', {}).get(role, [])
            roles += [e['source'] for e in facts['events'] if e['no_night_after'] <= value <= e['resume']]
            if value >= facts['year_end_no_night_after']:
                roles.append(facts['year_end_source'])
            refs = [facts['sources'][r]['ref'] for r in dict.fromkeys(roles)]
            template = {'From': vf, 'To': vt,
                        'Day': [{'Start': a*60000, 'End': b*60000} for a, b in facts['day_minutes']],
                        'Night': [{'Start': a*60000, 'End': b*60000} for a, b in p['night_minutes']]}
            rows.append({'day': value, 'sessions': sessions, 'template': template, 'evidence': refs})
            day += dt.timedelta(days=1)
        exchange, product = key.split('.')
        result.append({'product': {'Exchange': exchange, 'Product': product}, 'records': rows})
        summary[key] = {'natural_days': len(rows), 'trading_days': count, 'day_only_trade_days': day_only}
    return {'schema': 'annual-calendar-v1', 'year': year, 'facts_sha256': facts_sha,
            'scope': 'reviewed published continuous schedules; no auction or unlisted profile coverage',
            'series': result, 'summary': summary}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ['facts', 'review', 'evidence-dir', 'out']:
        parser.add_argument('--' + name, required=True, type=Path)
    args = parser.parse_args()
    source = args.facts.read_bytes()
    facts = checked_facts(source, json.loads(args.review.read_bytes()))
    result = generate(facts, load_sources(facts, args.evidence_dir), hashlib.sha256(source).hexdigest())
    with args.out.open('x', encoding='utf-8', newline='\n') as stream:
        json.dump(result, stream, ensure_ascii=False, indent=2)
        stream.write('\n')
    print(json.dumps(result['summary'], ensure_ascii=False))


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    main()
