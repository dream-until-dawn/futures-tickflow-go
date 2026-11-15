"""零网络生成逐合约研究可获得性目录；观察边界不认证挂牌生命周期或回测准入。"""
import argparse
from collections import Counter
import csv
import datetime as dt
from decimal import Decimal
import hashlib
import io
import json
from pathlib import Path
import re
import sys
from urllib.parse import parse_qs, urlsplit

from frozen_inputs import FrozenInputs

PACKS = {
    'shfe': ('f72dea10c5f39b342c9ec6366f9d64e011c57cbd066ab2cc277c1da7347e3f69',
             'repo/docs/evidence/historical-contract-directory-20261115/daily-1406-full-official-lock.json',
             'private-reports/daily-1406-full-official-result.json'),
    'cffex': ('ac33ebd2da9c5a28ee42a0f081760754c35ba6b1ef33e0933fad6e8f76e0e130',
              'metadata/daily-cffex-372-lock.json', 'reports/full-report.json'),
    'czce': ('dc09ed2937300702ca8225dc4286c8f9da92f32933425ed5cf241b7c6df9a521',
             'metadata/daily-czce-observed-full-lock.json', 'reports/full-report.json'),
}
INVENTORY = 'acquisitions/offline-v2/inventory.json'
INVENTORY_SHA = 'c7ff4e26edda99e694149e16e4790050f4e847bebb4ad940b704340c64632545'
HEADER = ['datetime_nano', 'open', 'high', 'low', 'close', 'volume', 'open_oi', 'close_oi']
ZONE = dt.timezone(dt.timedelta(hours=8))


def candidates(inventory):
    result = {}; source_keys = set(); symbols = set()
    for c in inventory['contracts']:
        symbol, source = c['canonical'], c['source_key']
        expected = f"{c['exchange']}.{c['product']}{c['delivery_year']%100:02d}{c['delivery_month']:02d}"
        expiry = Decimal(str(c['expiry_seconds']))
        if symbol != expected or not re.fullmatch(r'[A-Za-z]+\.[A-Za-z]+[0-9]{4}', symbol) or not expiry.is_finite():
            raise ValueError('inventory_identity')
        dt.date(c['delivery_year'], c['delivery_month'], 1)
        if symbol in symbols or source in source_keys:
            raise ValueError('inventory_duplicate')
        source_keys.add(source); symbols.add(symbol)
        # 排除早于2021的候选只是本研究路线过滤，不证明其不存在或未来全集完整。
        if expiry >= 1609430400:
            result[symbol] = dict(symbol=symbol, source_key=source, exchange=c['exchange'], product=c['product'],
                                  supplier_expiry_seconds=str(c['expiry_seconds']),
                                  availability=dict(state='not_requested_on_directory_route'),
                                  official_listing_day=None, official_last_trading_day=None,
                                  lifecycle_certified=False, financial_eligible=False)
    return result


def daily_days(raw):
    reader = csv.DictReader(io.StringIO(raw.decode('utf-8')))
    if reader.fieldnames != HEADER:
        raise ValueError('daily_header')
    days = []
    for row in reader:
        stamp = row.get('datetime_nano', '')
        if set(row) != set(HEADER) or not stamp.isdigit():
            raise ValueError('daily_row_shape')
        ns = int(stamp)
        instant = dt.datetime.fromtimestamp(ns//10**9, ZONE)
        day = int(instant.strftime('%Y%m%d'))
        if ns % 10**9 or instant.time() != dt.time() or day < 20210101 or (days and day <= days[-1]):
            raise ValueError('daily_anchor_or_order')
        days.append(day)
    return days


def receipt_check(receipt, raw, source_key, state):
    url = receipt['evidence']['url']
    parsed = urlsplit(url); query = parse_qs(parsed.query)
    if parsed.scheme != 'https' or parsed.netloc != 'edb.shinnytech.com' or parsed.path != '/md/kline' or query.get('period') != ['86400'] or query.get('symbol') != [source_key]:
        raise ValueError('receipt_symbol_or_period')
    if receipt['state'] != state or receipt['body_state'] != 'complete' or receipt['http']['status'] != 200 or not receipt['http']['complete'] or receipt['http']['final_url'] != url or receipt['bytes'] != len(raw) or receipt['evidence']['sha256'] != hashlib.sha256(raw).hexdigest():
        raise ValueError('receipt_complete_identity')


def attach_nonempty(entry, raw, receipt, review, source_id):
    if entry['availability']['state'] != 'not_requested_on_directory_route':
        raise ValueError('duplicate_contract_observation')
    receipt_check(receipt, raw, entry['source_key'], 'received')
    days = daily_days(raw)
    if not days or (len(days), days[0], days[-1]) != (review['rows'], review['first_day'], review['last_day']):
        raise ValueError('daily_report_range')
    if review.get('financial_eligible') is not False or review.get('official_lifecycle_certified') is not False:
        raise ValueError('review_scope_promotion')
    missing = review['missing_official_days']
    if set(missing) & set(days):
        raise ValueError('reference_missing_conflict')
    entry['availability'] = dict(state='nonempty', rows=len(days), first_observed_day=days[0], last_observed_day=days[-1],
                                 body_sha256=receipt['evidence']['sha256'], source_id=source_id)
    entry['reference_review'] = dict(reference_days=review['official_days'], overlapping_days=review['overlapping_days'],
                                   missing_reference_days=missing,
                                   unresolved_distinct_fields=review['binary64_distinct_differences'],
                                   calendar_unknown_actual_days=len(review['calendar_unknown_actual_days']),
                                   calendar_product_profile_available=review['calendar_product_profile_available'],
                                   interior_only=True,
                                   reference_transport_identity='native-http/unauthenticated' if source_id == 'cffex' else 'official-https-endpoint')


def summarize(result):
    counts = Counter(c['availability']['state'] for c in result)
    products = {}
    for c in result:
        key = c['exchange']+'.'+c['product']
        p = products.setdefault(key, dict(candidates=0, nonempty=0, empty_unavailable=0,
                                         not_requested_on_directory_route=0, rows=0,
                                         calendar_unknown_actual_rows=0, unresolved_distinct_fields=0,
                                         missing_reference_days_in_nonempty=0))
        p['candidates'] += 1
        p[c['availability']['state']] += 1
        p['rows'] += c['availability'].get('rows', 0)
        r = c.get('reference_review', {})
        p['calendar_unknown_actual_rows'] += r.get('calendar_unknown_actual_days', 0)
        p['unresolved_distinct_fields'] += r.get('unresolved_distinct_fields', 0)
        p['missing_reference_days_in_nonempty'] += len(r.get('missing_reference_days', []))
    return dict(candidates=len(result), states=dict(counts), rows=sum(c['availability'].get('rows', 0) for c in result),
                observed_directory_attempts=counts['nonempty']+counts['empty_unavailable'],
                by_product=dict(sorted(products.items())))


def build(packages):
    source = packages['shfe'].read(INVENTORY)
    if hashlib.sha256(source).hexdigest() != INVENTORY_SHA:
        raise ValueError('inventory_sha')
    entries = candidates(json.loads(source))
    provenance = []
    for source_id, package in packages.items():
        _, lock_path, report_path = PACKS[source_id]
        lock, report = package.data(lock_path), package.data(report_path)
        cases = lock['cases']; results = report['results']; observed = set()
        for case in cases:
            symbol = case['symbol']
            if symbol not in entries or symbol not in results or symbol in observed:
                raise ValueError('locked_symbol_or_duplicate')
            observed.add(symbol)
            raw = package.read('raw/edb/' + case['file'])
            receipt_raw = package.read('raw/edb/' + case['receipt'])
            if hashlib.sha256(raw).hexdigest() != case['sha256'] or hashlib.sha256(receipt_raw).hexdigest() != case['receipt_sha256']:
                raise ValueError('case_identity')
            receipt = json.loads(receipt_raw)
            if receipt['evidence']['url'] != case['url']:
                raise ValueError('case_url')
            attach_nonempty(entries[symbol], raw, receipt, results[symbol], source_id)
        if set(results) != observed:
            raise ValueError('report_contract_set')
        provenance.append(dict(id=source_id, manifest_sha256=package.trusted, lock=lock_path, report=report_path))
    # 空响应原failed状态保持；不冒充成功，也不纳入可覆盖的合约数。
    package = packages['czce']; empty_count = 0
    for name in sorted(package.entries):
        if not name.startswith(('acquisition/continuation/archive/', 'acquisition/parent-stopped/archive/')) or not name.endswith('.json'):
            continue
        receipt = package.data(name)
        if receipt.get('state') != 'failed' or receipt.get('bytes') != 59:
            continue
        raw = package.read(name[:-5] + '.body')
        native = parse_qs(urlsplit(receipt['evidence']['url']).query)['symbol'][0]
        matching = [c for c in entries.values() if c['source_key'] == native]
        if len(matching) != 1:
            raise ValueError('empty_identity')
        entry = matching[0]
        if entry['availability']['state'] != 'not_requested_on_directory_route' or daily_days(raw):
            raise ValueError('empty_not_empty_or_duplicate')
        receipt_check(receipt, raw, entry['source_key'], 'failed')
        entry['availability'] = dict(state='empty_unavailable', rows=0, raw_receipt_state='failed',
                                     body_sha256=receipt['evidence']['sha256'], source_id='czce',
                                     counts_as_success=False, counts_as_coverage=False)
        empty_count += 1
    if empty_count != 11:
        raise ValueError('empty_contract_set')
    result = sorted(entries.values(), key=lambda c: c['symbol'])
    return dict(schema='historical-research-catalog-v1', inventory_sha256=INVENTORY_SHA,
                source_packages=provenance, contracts=result,
                summary=summarize(result),
                market_requests=0, full_market_universe_certified=False, strict_backtest_admission_expanded=False,
                excludes_legacy_separate_vendor_only_samples=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in PACKS:
        parser.add_argument('--'+name+'-package', type=Path, required=True)
    parser.add_argument('--out-root', type=Path, required=True)
    parser.add_argument('--verify-complete-packages', action='store_true', help='额外核完整旧包，不适用于最小输入迁移包')
    a = parser.parse_args()
    packages = {name: FrozenInputs(getattr(a, name+'_package'), spec[0]) for name, spec in PACKS.items()}
    if any(a.out_root.resolve().is_relative_to(p.root.resolve()) for p in packages.values()):
        raise ValueError('output_inside_input')
    if a.out_root.exists():
        raise ValueError('output_already_exists')
    if a.verify_complete_packages:
        for package in packages.values():
            package.verify_all()
    result = build(packages)
    a.out_root.mkdir(parents=True)
    (a.out_root/'catalog.json').write_text(json.dumps(result, ensure_ascii=False, indent=2)+'\n', encoding='utf-8', newline='\n')
    for name, package in packages.items():
        package.export_used(a.out_root/'inputs'/name)
    print(json.dumps({k:v for k,v in result['summary'].items() if k != 'by_product'}, ensure_ascii=False))


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    main()
