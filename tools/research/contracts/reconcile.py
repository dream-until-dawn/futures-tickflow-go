"""冻结日线小批逐日对账；原表缺日、日历内部缺日和范围外分开报告。"""
import argparse
import datetime as dt
import io
import json
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[3]))
from tools.research.edb.review import FIELDS, ZONE, load_case, locked, number, rows


def reconcile(raw, official, calendar, covered_years):
    actual, previous = {}, 0
    for row in rows(raw):
        stamp = int(row['datetime_nano'])
        instant = dt.datetime.fromtimestamp(stamp//1000000000, ZONE)
        if stamp % 1000000000 or instant.time() != dt.time():
            raise ValueError('daily_anchor')
        day = int(instant.strftime('%Y%m%d'))
        if day <= previous or not 20210101 <= day <= 20261113:
            raise ValueError('daily_order_or_window')
        previous = day
        actual[day] = row
    differences, unknown, matched = [], [], 0
    for day in sorted(set(actual) & set(official)):
        for field in FIELDS:
            value = number(actual[day]['close_oi' if field == 'open_interest' else field])
            try:
                control = number(official[day][field])
            except (ValueError, ArithmeticError):
                unknown.append(dict(day=day, field=field, official=official[day][field]))
                continue
            if value != control:
                differences.append(dict(day=day, field=field, edb=str(value), official=str(control)))
            else:
                matched += 1
    start, end = min(actual), max(actual)
    trading = {d for d, open_day in calendar.items() if open_day and start <= d <= end}
    return dict(rows=len(actual), first_day=start, last_day=end, official_days=len(official),
                overlapping_days=len(set(actual)&set(official)), matched_values=matched,
                differences=differences, official_unpriced_fields=unknown,
                missing_official_days=sorted(set(official)-set(actual)),
                extra_in_official_years=sorted(d for d in set(actual)-set(official) if d//10000 in covered_years),
                outside_official_years=sorted(d for d in actual if d//10000 not in covered_years),
                calendar_interior_missing_days=sorted(trading-set(actual)),
                calendar_unknown_actual_days=sorted(set(actual)-set(calendar)),
                calendar_closed_actual_days=sorted(d for d in actual if d in calendar and not calendar[d]),
                calendar_check_scope='interior first-to-last observed bar only; not lifecycle completeness',
                missing_rows_imputed=False, settlement_compared=False, turnover_compared=False,
                official_lifecycle_certified=False, financial_eligible=False)


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--lock', type=Path, required=True)
    p.add_argument('--data-root', type=Path, required=True)
    p.add_argument('--repo-root', type=Path, default=Path(__file__).resolve().parents[3])
    p.add_argument('--out', type=Path, required=True)
    args = p.parse_args()
    if args.out.resolve().is_relative_to(args.data_root.resolve()):
        raise ValueError('output_inside_source_tree')
    lock = json.loads(args.lock.read_bytes())
    official = [json.loads(locked(Path(r['file']), r['sha256'])) for r in lock['official_inputs']]
    calendars = [json.loads(locked(args.repo_root/r['file'], r['sha256'])) for r in lock['calendars']]
    result = {}
    for case in lock['cases']:
        raw = load_case(args.data_root, case)
        exchange, native = case['symbol'].split('.')
        controls, years = {}, set()
        for dataset in official:
            if dataset['product_filter'] != case['product']:
                continue
            years.add(dataset['year'])
            for row in dataset['records']:
                if row['exchange'] == exchange and row['native'] == native:
                    if row['day'] in controls:
                        raise ValueError('official_duplicate_day')
                    controls[row['day']] = row['fields']
        if not controls:
            raise ValueError('no_official_control')
        calendar = {}
        for profile in calendars:
            for series in profile['series']:
                if series['product'] != dict(Exchange=exchange, Product=case['product']):
                    continue
                for row in series['records']:
                    if row['day'] in calendar:
                        raise ValueError('calendar_duplicate_day')
                    calendar[row['day']] = bool(row['sessions'])
        result[case['symbol']] = reconcile(raw, controls, calendar, years)
    args.out.write_text(json.dumps(dict(schema='directory-daily-official-review-v1', results=result,
                                        market_requests=0, full_market_coverage_certified=False,
                                        legacy_37_differences_resolved=False), ensure_ascii=False, indent=2)+'\n',
                        encoding='utf-8', newline='\n')


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    main()
