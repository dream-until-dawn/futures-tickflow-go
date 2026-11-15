"""独立读取冻结EDB CSV与官方对照；只验证指定样本，不联网、不生成新日历。"""
import argparse
import csv
import datetime as dt
import hashlib
import io
import json
import sys
from decimal import Decimal
from pathlib import Path

HEADER = ['datetime_nano', 'open', 'high', 'low', 'close', 'volume', 'open_oi', 'close_oi']
FIELDS = ['open', 'high', 'low', 'close', 'volume', 'open_interest']
ZONE = dt.timezone(dt.timedelta(hours=8))


def number(text):
    value = Decimal(text)
    if len(text) > 128 or not value.is_finite() or value < 0 or (value and abs(value.adjusted()) > 308):
        raise ValueError('numeric_budget')
    return value


def rows(raw):
    reader = csv.DictReader(io.StringIO(raw.decode('utf-8')))
    if reader.fieldnames != HEADER:
        raise ValueError('csv_header')
    result = list(reader)
    if not result:
        raise ValueError('no_rows')
    for row in result:
        if set(row) != set(HEADER) or not row['datetime_nano'].isdigit():
            raise ValueError('csv_shape')
        values = {key: number(row[key]) for key in HEADER[1:]}
        if any(values[key] != values[key].to_integral_value() for key in ('volume', 'open_oi', 'close_oi')):
            raise ValueError('nonintegral_quantity')
        if values['low'] > min(values['open'], values['close']) or values['high'] < max(values['open'], values['close']) or values['low'] > values['high']:
            raise ValueError('ohlc_geometry')
    return result


def compare(actual, official):
    for field in FIELDS:
        if actual[field] != number(official[field]):
            raise ValueError('official_mismatch:' + field)


def review_minutes(raw, sessions, official):
    data = rows(raw)
    expected = [stamp * 1000000 for session in sessions for stamp in range(session['Start'], session['End'], 60000)]
    stamps = [int(row['datetime_nano']) for row in data]
    if stamps != expected:
        raise ValueError('minute_grid')
    actual = dict(open=number(data[0]['open']), high=max(number(r['high']) for r in data),
                  low=min(number(r['low']) for r in data), close=number(data[-1]['close']),
                  volume=sum(number(r['volume']) for r in data), open_interest=number(data[-1]['close_oi']))
    compare(actual, official)
    return dict(rows=len(data), first_ns=stamps[0], last_ns=stamps[-1], exact_expected_grid=True,
                aggregates={k: str(v) for k, v in actual.items()}, official_matched_fields=FIELDS,
                comparison='Decimal numeric equality; not original lexical equality',
                settlement_compared=False, turnover_compared=False, financial_eligible=False)


def review_daily(raw, official_by_day):
    data = rows(raw)
    seen, results = set(), []
    for row in data:
        stamp = int(row['datetime_nano'])
        instant = dt.datetime.fromtimestamp(stamp // 1000000000, ZONE)
        if stamp % 1000000000 or instant.time() != dt.time():
            raise ValueError('daily_observed_anchor')
        day = int(instant.strftime('%Y%m%d'))
        if day in seen or day not in official_by_day:
            raise ValueError('daily_keys')
        seen.add(day)
        actual = {key: number(row['close_oi' if key == 'open_interest' else key]) for key in FIELDS}
        compare(actual, official_by_day[day])
        results.append(dict(day=day, timestamp_ns=stamp, anchor_observed='00:00 Asia/Shanghai', matched_fields=FIELDS))
    if seen != set(official_by_day):
        raise ValueError('daily_keys')
    return dict(rows=len(data), records=results, observed_scope_only=True, query_boundary_inclusivity_certified=False,
                settlement_compared=False, turnover_compared=False, financial_eligible=False)


def locked(path, expected_sha):
    raw = path.read_bytes()
    if hashlib.sha256(raw).hexdigest() != expected_sha:
        raise ValueError('source_sha:' + str(path))
    return raw


def load_case(root, case):
    raw = locked(root / case['file'], case['sha256'])
    receipt = json.loads(locked(root / case['receipt'], case['receipt_sha256']))
    http = receipt['http']
    if receipt['state'] != 'received' or receipt['body_state'] != 'complete' or receipt['bytes'] != len(raw) or receipt['evidence']['sha256'] != case['sha256'] or receipt['evidence']['url'] != case['url'] or http['status'] != 200 or not http['complete'] or not http['attempted'] or not http['response_received'] or http['final_url'] != case['url']:
        raise ValueError('receipt_identity')
    return raw


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--lock', required=True, type=Path)
    p.add_argument('--data-root', required=True, type=Path)
    p.add_argument('--repo-root', type=Path, default=Path(__file__).resolve().parents[3])
    p.add_argument('--out', required=True, type=Path)
    args = p.parse_args()
    if args.out.resolve().is_relative_to(args.data_root.resolve()):
        raise ValueError('output_inside_source_tree')
    lock = json.loads(args.lock.read_text(encoding='utf-8'))
    official = json.loads(locked(args.repo_root / lock['official_file'], lock['official_sha256']))
    records = {(r['exchange'] + '.' + r['native'], r['day']): r for r in official['records']}
    results = {}
    for case in lock['minute_cases']:
        raw = load_case(args.data_root, case)
        calendar = json.loads(locked(args.repo_root / case['calendar_file'], case['calendar_sha256']))
        series = next(s for s in calendar['series'] if s['product']['Exchange'] == case['exchange'] and s['product']['Product'] == case['product'])
        session = next(r['sessions'] for r in series['records'] if r['day'] == case['day'])
        result = review_minutes(raw, session, records[(case['symbol'], case['day'])]['fields'])
        result['symbol'], result['day'] = case['symbol'], case['day']
        results[case['id']] = result
    case = lock['daily_case']
    raw = load_case(args.data_root, case)
    daily = json.loads(locked(args.repo_root / case['official_file'], case['official_sha256']))
    results[case['id']] = review_daily(raw, {r['day']: r['fields'] for r in daily['records']})
    args.out.write_text(json.dumps(dict(schema='edb-independent-reconciliation-v1', results=results,
                                      market_requests=0, full_market_coverage_certified=False,
                                      redistribution_grant_verified=False, financial_admission_expanded=False), ensure_ascii=False, indent=2) + '\n', encoding='utf-8', newline='\n')


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    main()
