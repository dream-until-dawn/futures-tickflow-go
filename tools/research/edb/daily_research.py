"""离线比较EDB与既存新浪日线；供应商相符不等于官方获证。"""
import argparse
import datetime as dt
import io
import json
import re
import sys
from pathlib import Path

from review import FIELDS, ZONE, load_case, locked, number, rows


def compare_vendors(edb, sina):
    # 仅惰性解析已知赋值形式，不执行JSONP内的脚本。
    text = sina.decode('utf-8')
    match = re.fullmatch(r"/\*<script>location.href='//sina.com';</script>\*/\s*var _V21052021_4_12=\((\[.*\])\);?\s*", text, re.S)
    if not match:
        raise ValueError('sina_envelope')
    reference = {}
    for row in json.loads(match.group(1)):
        day = dt.date.fromisoformat(row['d'])
        if day in reference:
            raise ValueError('sina_duplicate_day')
        reference[day] = {key: number(row[column]) for key, column in
                          zip(FIELDS, ('o', 'h', 'l', 'c', 'v', 'p'))}
    observed, differences, matched, previous = {}, [], 0, None
    for row in rows(edb):
        stamp = int(row['datetime_nano'])
        instant = dt.datetime.fromtimestamp(stamp // 1000000000, ZONE)
        if stamp % 1000000000 or instant.time() != dt.time():
            raise ValueError('daily_observed_anchor')
        day = instant.date()
        if previous is not None and day <= previous:
            raise ValueError('edb_day_order')
        previous = day
        observed[day] = row
        if day not in reference:
            continue
        for field in FIELDS:
            actual = number(row['close_oi' if field == 'open_interest' else field])
            expected = reference[day][field]
            if actual == expected:
                matched += 1
            else:
                differences.append(dict(day=day.isoformat(), field=field,
                                        edb=str(actual), sina=str(expected)))
    days = sorted(observed)
    # 查询窗明确从2021年起；旧样本窗外行不算本批缺失。
    start, end = dt.date(2021, 1, 1), dt.date(2026, 11, 13)
    if any(day < start or day > end for day in days):
        raise ValueError('outside_query_window')
    reference_window = {day for day in reference if start <= day <= end}
    return dict(rows=len(days), first_day=str(days[0]), last_day=str(days[-1]),
                overlapping_days=len(set(days) & reference_window), matched_values=matched,
                differences=differences, edb_only_days=[str(d) for d in sorted(set(days) - reference_window)],
                sina_only_days=[str(d) for d in sorted(reference_window - set(days))],
                comparison='six fields, exact Decimal numeric equality, vendor cross-check only',
                official_reconciled=False, lifecycle_certified=False, calendar_certified=False,
                settlement_compared=False, turnover_compared=False, financial_eligible=False)


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
    result = {}
    for case in lock['cases']:
        edb = load_case(args.data_root, case)
        sina = locked(args.repo_root / case['sina_file'], case['sina_sha256'])
        result[case['symbol']] = compare_vendors(edb, sina)
    args.out.write_text(json.dumps(dict(schema='edb-daily-vendor-cross-check-v1', results=result,
                                        market_requests=0, full_market_coverage_certified=False),
                                   ensure_ascii=False, indent=2) + '\n', encoding='utf-8', newline='\n')


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    main()
