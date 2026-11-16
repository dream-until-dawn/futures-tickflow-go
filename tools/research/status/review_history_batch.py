"""只读核对历史日线批次，保留空数据/缺日/差异；不联网，不公开批量报价。"""
import argparse
from collections import Counter
from decimal import Decimal, InvalidOperation
import hashlib
import io
import json
from pathlib import Path
import sys
from urllib.parse import parse_qs, urlparse

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'contracts'))
sys.path.insert(0, str(Path(__file__).resolve().parents[3]))
from reconcile import reconcile, public_report
from tools.research.edb.review import load_case

EMPTY = b'datetime_nano,open,high,low,close,volume,open_oi,close_oi\r\n'


def digest(raw):
    return hashlib.sha256(raw).hexdigest()


def require(condition, reason):
    if not condition:
        raise ValueError(reason)


def collect_cases(roots, jobs, canonical):
    """原任务与隔离任务按同一清单取并集；重复、孤儿正文和未知状态拒绝。"""
    cases, states, failures = [], Counter(), []
    stopped_roots = set()
    byte_count = 0
    for root in roots:
        report = root / 'report.json'
        if report.exists():
            require(json.loads(report.read_bytes())['state'] != 'running', 'acquisition_still_running')
    for job in jobs:
        found = []
        for root in roots:
            receipt = root / 'archive' / (job['id'] + '.json')
            body = receipt.with_suffix('.body')
            require(receipt.exists() or not body.exists(), 'acquisition_orphan_body')
            if receipt.exists():
                found.append((root, receipt, body))
        require(len(found) <= 1, 'acquisition_repeated_job')
        if not found:
            states['not_run'] += 1
            continue
        root, receipt, body = found[0]
        require(root not in stopped_roots, 'acquisition_after_stop')
        raw, meta = body.read_bytes(), json.loads(receipt.read_bytes())
        require(digest(raw) == meta['evidence']['sha256'] and len(raw) == meta['bytes'], 'acquisition_raw_identity')
        require(meta['evidence']['url'] == job['url'] and meta['http']['attempted'], 'acquisition_url_identity')
        byte_count += len(raw)
        if meta['state'] != 'received':
            empty = (meta['state'] == 'failed' and raw == EMPTY and meta['http']['status'] == 200
                     and meta['http']['complete'] and meta['body_state'] == 'complete'
                     and not meta['http'].get('failure')
                     and meta['error'] == 'CSV错误或没有样本行: <nil>')
            states['unavailable_complete_empty' if empty else 'other_failed'] += 1
            failures.append(dict(id=job['id'], url=job['url'], complete_empty=empty,
                                 body_sha256=digest(raw),receipt_sha256=digest(receipt.read_bytes()),
                                 requested_at=meta['http'].get('requested_at')))
            if not empty or not (root/'report.json').exists():
                stopped_roots.add(root)
            continue
        key = parse_qs(urlparse(job['url']).query)['symbol'][0]
        case = dict(symbol=canonical[key], product=job['product'], url=job['url'],
                    source_root=str(root), file='archive/' + body.name, sha256=digest(raw),
                    receipt='archive/' + receipt.name, receipt_sha256=digest(receipt.read_bytes()))
        load_case(root, case)
        cases.append(case)
        states['received'] += 1
    return cases, states, byte_count, failures


def load_controls(manifest, canonical, selected):
    """官方目录保留原生码；只按已有锁定身份映射，不猜十年。"""
    meta = json.loads(manifest.read_bytes())
    raw = (manifest.parent / 'records.jsonl').read_bytes()
    require(digest(raw) == meta['records_sha256'], 'official_records_sha')
    controls = {key: {} for key in selected}
    for line in raw.splitlines():
        row = json.loads(line)['record']
        source_key = row['exchange'] + '.' + row['native']
        key = canonical.get(source_key, source_key)
        if key not in controls:
            continue
        require(row['day'] not in controls[key], 'official_duplicate_day')
        controls[key][row['day']] = {k: v if v is not None else '' for k, v in row['fields'].items()}
    return controls


def summarize(results, controls):
    fields, by_product = Counter(), {}
    zero_volume_differences = missing_valid = 0
    for symbol, result in results.items():
        product = symbol.rstrip('0123456789')
        group = by_product.setdefault(product, dict(contracts=0, rows=0, overlapping_days=0,
                                                    distinct_fields=0, missing_official_days=0))
        group['contracts'] += 1
        for key in ('rows', 'overlapping_days'):
            group[key] += result[key]
        group['distinct_fields'] += result['binary64_distinct_differences']
        group['missing_official_days'] += len(result['missing_official_days'])
        for difference in result['differences']:
            fields[difference['field']] += 1
            official = controls[symbol][difference['day']]
            if Decimal(official['volume'].replace(',', '')) == 0:
                zero_volume_differences += 1
        for day in result['missing_official_days']:
            record = controls[symbol][day]
            try:
                o, h, l, c = (Decimal(record[k].replace(',', '')) for k in ('open', 'high', 'low', 'close'))
                missing_valid += int(0 < l <= min(o, c) <= max(o, c) <= h)
            except (ValueError, InvalidOperation):
                pass  # 缺报价保留，不以零或前值填补。
    return dict(by_product=by_product, differences_by_field=dict(fields),
                zero_volume_official_difference_fields=zero_volume_differences,
                missing_official_dates_with_basic_valid_ohlc=missing_valid)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--data-root', type=Path, action='append', required=True)
    parser.add_argument('--plan', type=Path, required=True)
    parser.add_argument('--official-manifest', type=Path, required=True)
    parser.add_argument('--out-root', type=Path, required=True)
    args = parser.parse_args()
    require(not any(args.out_root.resolve().is_relative_to(r.resolve()) for r in args.data_root), 'review_output_inside_originals')
    args.out_root.mkdir()
    plan = json.loads(args.plan.read_bytes())
    raw = Path(plan['prior_catalog']['file']).read_bytes()
    require(digest(raw) == plan['prior_catalog']['sha256'], 'prior_catalog_sha')
    canonical = {r['source_key']: r['symbol'] for r in json.loads(raw)['contracts']}
    base_catalog=json.loads(raw)
    cases, states, byte_count, failures = collect_cases(args.data_root, plan['jobs'], canonical)
    controls = load_controls(args.official_manifest, canonical, {r['symbol'] for r in cases})
    results = {}
    for case in cases:
        raw = (Path(case['source_root']) / case['file']).read_bytes()
        results[case['symbol']] = reconcile(raw, controls[case['symbol']], {}, set(range(2021, 2027)))
    full = dict(schema='directory-daily-official-review-v1', results=results, market_requests=0,
                full_market_coverage_certified=False, legacy_37_differences_resolved=False)
    summary = dict(requests=sum(v for k, v in states.items() if k != 'not_run'), bytes=byte_count, states=dict(states),
                   successful_rows=sum(r['rows'] for r in results.values()),
                   overlapping_days=sum(r['overlapping_days'] for r in results.values()),
                   matched_fields=sum(r['matched_values'] for r in results.values()),
                   new_distinct_fields=sum(r['binary64_distinct_differences'] for r in results.values()),
                   official_unpriced_fields=sum(len(r['official_unpriced_fields']) for r in results.values()),
                   missing_reference_days=sum(len(r['missing_official_days']) for r in results.values()),
                   failures=failures, old_144_and_37_resolved=False, financial_admission_added=0,
                   comparison_scope='已锁目录与显式年月参考绑定源键；官方累计目录相交对账，不认证版本/PIT/完整生命周期')
    summary.update(summarize(results, controls))
    case_index={r['symbol']:r for r in cases}
    failure_index={r['id']:r for r in failures}
    base_index={r['symbol']:r for r in base_catalog['contracts']}
    entries=[]
    for job in plan['jobs']:
        source_key=parse_qs(urlparse(job['url']).query)['symbol'][0]
        symbol=canonical[source_key]
        previous=base_index[symbol]['availability']['state']
        require(previous=='not_requested_on_directory_route','extension_prior_state')
        item=dict(symbol=symbol,source_key=source_key,exchange=job['exchange'],product=job['product'],previous_state=previous,financial_eligible=False)
        if symbol in results:
            result=results[symbol]
            item.update(state='nonempty',rows=result['rows'],first_observed_day=result['first_day'],last_observed_day=result['last_day'],body_sha256=case_index[symbol]['sha256'])
        elif job['id'] in failure_index:
            failure=failure_index[job['id']]
            item.update(state='empty_unavailable' if failure['complete_empty'] else 'failed',rows=0,
                        counts_as_success=False,counts_as_coverage=False,body_sha256=failure['body_sha256'])
        else:
            item.update(state='not_run',rows=0)
        entries.append(item)
    extension=dict(schema='research-directory-extension-v1',parent_catalog_sha256=plan['prior_catalog']['sha256'],base_summary=base_catalog['summary'],
                   entries=entries,rows=summary['successful_rows'],states=dict(Counter(r['state'] for r in entries)),
                   strict_backtest_admission_expanded=False,full_listing_certified=False)
    public = public_report(full)
    for row in public['results'].values():
        row['differences'] = []  # 完整差异值留私有研究包；公开计数和指纹。
        row['difference_details_private'] = True
    for name, value in [('daily-cases.json', cases), ('daily-full-review.json', full),
                        ('daily-public-review.json', public), ('daily-summary.json', summary),('new-route-catalog.json',extension)]:
        (args.out_root / name).write_text(json.dumps(value, ensure_ascii=False, indent=2) + '\n', encoding='utf8', newline='\n')
    print(json.dumps({k: v for k, v in summary.items() if k not in ('failures', 'by_product')}, ensure_ascii=False))


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    main()
