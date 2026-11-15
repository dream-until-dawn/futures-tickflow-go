"""冻结日线小批逐日对账；原表缺日、日历内部缺日和范围外分开报告。"""
import argparse
import copy
import datetime as dt
import hashlib
import io
import json
import math
import re
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
                control_text=official[day][field]
                if re.fullmatch(r'[0-9]{1,3}(?:,[0-9]{3})+(?:\.[0-9]+)?',control_text):
                    control_text=control_text.replace(',','')
                control = number(control_text)
            except (ValueError, ArithmeticError):
                unknown.append(dict(day=day, field=field, official=official[day][field]))
                continue
            if value != control:
                # 逐字面数值不同仍保留；另标注精确binary64等价，不用epsilon抹掉差异。
                a, b = float(value), float(control)
                differences.append(dict(day=day, field=field, edb=str(value), official=str(control),
                                        binary64_equal=math.isfinite(a) and math.isfinite(b) and a == b))
            else:
                matched += 1
    start, end = min(actual), max(actual)
    trading = {d for d, open_day in calendar.items() if open_day and start <= d <= end}
    return dict(rows=len(actual), first_day=start, last_day=end, official_days=len(official),
                overlapping_days=len(set(actual)&set(official)), matched_values=matched,
                differences=differences, official_unpriced_fields=unknown,
                binary64_equivalent_differences=sum(d['binary64_equal'] for d in differences),
                binary64_distinct_differences=sum(not d['binary64_equal'] for d in differences),
                missing_official_days=sorted(set(official)-set(actual)),
                extra_in_official_years=sorted(d for d in set(actual)-set(official) if d//10000 in covered_years),
                outside_official_years=sorted(d for d in actual if d not in official and d//10000 not in covered_years),
                calendar_interior_missing_days=sorted(trading-set(actual)),
                calendar_unknown_actual_days=sorted(set(actual)-set(calendar)),
                calendar_product_profile_available=bool(calendar),
                calendar_unknown_reason='outside verified profile ranges' if calendar else 'no verified product profile',
                calendar_closed_actual_days=sorted(d for d in actual if d in calendar and not calendar[d]),
                zero_volume_days_count=sum(number(r['volume'])==0 for r in actual.values()),
                zero_ohlc_days_count=sum(any(number(r[f])==0 for f in ['open','high','low','close']) for r in actual.values()),
                calendar_check_scope='interior first-to-last observed bar only; not lifecycle completeness',
                missing_rows_imputed=False, settlement_compared=False, turnover_compared=False,
                official_lifecycle_certified=False, financial_eligible=False)


def build_controls(datasets):
    # 原表先索引一次，避免每个合约重扫五年全表；相交与年份覆盖分开。
    index,years={},{}
    for dataset in datasets:
        for row in dataset['records']:
            key=(row['exchange'],row['native'])
            product=re.fullmatch(r'([A-Za-z]{1,3})[0-9]{4}',row['native'])
            if product is None:raise ValueError('official_canonical_code')
            records=index.setdefault(key,{})
            if row['day'] in records:raise ValueError('official_duplicate_day')
            records[row['day']]=row['fields']
            if dataset.get('control_scope')!='exact-dates':
                years.setdefault((row['exchange'],product[1]),set()).add(dataset['year'])
    return index,years


def public_report(report):
    # 不把数千条供应商价位通过“表示差异”报告变相公开；完整细节只留研究包。
    full_bytes=(json.dumps(report,ensure_ascii=False,indent=2)+'\n').encode('utf-8')
    result=copy.deepcopy(report)
    result['schema']='directory-daily-official-review-public-v1'
    result['private_full_report_sha256']=hashlib.sha256(full_bytes).hexdigest()
    result['public_value_scope']='distinct anomaly examples only; bulk representation-equivalent quote values withheld'
    for item in result['results'].values():
        representations=[d for d in item['differences'] if d['binary64_equal']]
        item['all_decimal_difference_fields']=len(item['differences'])
        item['representation_detail_sha256']=hashlib.sha256(json.dumps(representations,ensure_ascii=False,sort_keys=True,separators=(',',':')).encode()).hexdigest()
        item['differences']=[d for d in item['differences'] if not d['binary64_equal']]
    return result


def check_output(out,repo,data,public):
    if out.resolve().is_relative_to(data.resolve()):
        raise ValueError('output_inside_source_tree')
    if not public and out.resolve().is_relative_to(repo.resolve()):
        raise ValueError('full_report_inside_repo_use_public_summary')


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--lock', type=Path, required=True)
    p.add_argument('--data-root', type=Path, required=True)
    p.add_argument('--repo-root', type=Path, default=Path(__file__).resolve().parents[3])
    p.add_argument('--official-root', type=Path, help='迁移包内按SHA命名的官方解码输入目录；省略则沿用原取证路径')
    p.add_argument('--public-summary',action='store_true',help='只公开聚合与必要异常样例，不公开批量表示等价价格')
    p.add_argument('--out', type=Path, required=True)
    args = p.parse_args()
    check_output(args.out,args.repo_root,args.data_root,args.public_summary)
    lock = json.loads(args.lock.read_bytes())
    official = [json.loads(locked(args.official_root/(r['sha256']+'.json') if args.official_root else Path(r['file']), r['sha256'])) for r in lock['official_inputs']]
    controls_index,year_index=build_controls(official)
    del official
    calendars = [json.loads(locked(args.repo_root/r['file'], r['sha256'])) for r in lock['calendars']]
    result = {}
    for case in lock['cases']:
        raw = load_case(args.data_root, case)
        exchange, native = case['symbol'].split('.')
        controls=controls_index.get((exchange,native),{})
        years=year_index.get((exchange,case['product']),set())
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
        if 'control_identity' in case:
            result[case['symbol']]['control_identity']=case['control_identity']
    report=dict(schema='directory-daily-official-review-v1', results=result,
                market_requests=0, full_market_coverage_certified=False,legacy_37_differences_resolved=False)
    if args.public_summary:report=public_report(report)
    args.out.write_text(json.dumps(report, ensure_ascii=False, indent=2)+'\n',
                        encoding='utf-8', newline='\n')


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    main()
