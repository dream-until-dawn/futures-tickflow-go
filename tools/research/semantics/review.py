"""冻结样本的口径假说复核，不以数值拟合替代独立事件证据。"""
import argparse
import csv
import datetime as dt
import hashlib
import io
import json
import sys
from decimal import Decimal
from pathlib import Path

MANIFEST_SHA = 'dc09ed2937300702ca8225dc4286c8f9da92f32933425ed5cf241b7c6df9a521'
ZONE = dt.timezone(dt.timedelta(hours=8))


def sha(raw):
    return hashlib.sha256(raw).hexdigest()


def forum_hypothesis_fit(volume, official_volume, oi, official_oi):
    """条件假说：量增=行权+自对冲，仓增=行权-自对冲；解存在也不是实际流水。"""
    values = [volume, official_volume, oi, official_oi]
    if any(type(v) is not int or v < 0 for v in values):
        raise ValueError('quantity_nonnegative_integer')
    dv, doi = official_volume-volume, official_oi-oi
    fits = dv >= abs(doi) and (dv+doi) % 2 == 0
    return dict(delta_volume=dv, delta_open_interest=doi,
                nonnegative_integer_solution=fits,
                implied_exercise=(dv+doi)//2 if fits else None,
                implied_self_hedge=(dv-doi)//2 if fits else None,
                independent_event_evidence=False, difference_resolved=False)


def zero_quote_matches(actual, reference):
    price_fields = ['open', 'high', 'low', 'close']
    if Decimal(actual['volume']) != 0 or decimal(reference['volume']) != 0 or any(decimal(reference[f]) != 0 for f in price_fields):
        raise ValueError('not_reported_zero_volume_and_zero_quote')
    values = [Decimal(actual[f]) for f in price_fields]
    constant = len(set(values)) == 1
    return dict(edb_ohlc_constant=constant,
                equals_reference_pre_settle=constant and values[0] == decimal(reference['pre_settle']),
                equals_reference_settle=constant and values[0] == decimal(reference['settle']),
                fill_rule_proven=False, difference_resolved=False)


def decimal(text):
    value = Decimal(text.replace(',', ''))
    if not value.is_finite() or value < 0:
        raise ValueError('invalid_reference_number')
    return value


def review(root):
    manifest_raw = (root/'manifest.json').read_bytes()
    if sha(manifest_raw) != MANIFEST_SHA:
        raise ValueError('manifest_sha')
    entries = {r['path']: r for r in json.loads(manifest_raw)['files']}
    def locked(rel):
        path = root/rel
        if not path.resolve().is_relative_to(root.resolve()):
            raise ValueError('input_path')
        raw = path.read_bytes(); entry = entries[rel]
        if len(raw) != entry['bytes'] or sha(raw) != entry['sha256']:
            raise ValueError('file_sha:'+rel)
        return raw
    lock = json.loads(locked('metadata/daily-czce-observed-full-lock.json'))
    dataset = json.loads(locked('controls/'+lock['official_inputs'][0]['sha256']+'.json'))
    controls = {r['native']: r['fields'] for r in dataset['records']}
    reports = json.loads(locked('reports/full-report.json'))['results']
    quantities, zero_quotes = [], []
    for case in lock['cases']:
        symbol = case['symbol']; result = reports[symbol]
        differences = result['differences']
        if not differences:
            continue
        rows = csv.DictReader(io.StringIO(locked('raw/edb/'+case['file']).decode('utf-8')))
        rows = [r for r in rows if dt.datetime.fromtimestamp(int(r['datetime_nano'])//1000000000, ZONE).strftime('%Y%m%d') == '20210104']
        if len(rows) != 1:
            raise ValueError('reference_day_row')
        actual = rows[0]; reference = controls[symbol.split('.')[1]]
        if any(d['field'] in ('volume', 'open_interest') for d in differences):
            numbers = [Decimal(actual['volume']), decimal(reference['volume']), Decimal(actual['close_oi']), decimal(reference['open_interest'])]
            if any(n != n.to_integral_value() for n in numbers):
                raise ValueError('quantity_integer')
            fit = forum_hypothesis_fit(*map(int, numbers))
            quantities.append(dict(symbol=symbol, day=20210104,
                                   same_volume_and_oi_units_times_two_explains_pair=numbers[0]*2 == numbers[1] and numbers[2]*2 == numbers[3],
                                   **fit))
        if any(d['field'] in ('open', 'high', 'low', 'close') for d in differences):
            zero_quotes.append(dict(symbol=symbol, day=20210104, **zero_quote_matches(actual, reference)))
    return dict(schema='daily-semantics-hypotheses-v1', manifest_sha256=MANIFEST_SHA,
                quantity_cases=quantities, zero_quote_cases=zero_quotes,
                quantity_fit_count=sum(r['nonnegative_integer_solution'] for r in quantities),
                quantity_model_rejected_count=sum(not r['nonnegative_integer_solution'] for r in quantities),
                zero_quote_pre_settle_match_count=sum(r['equals_reference_pre_settle'] for r in zero_quotes),
                zero_quote_settle_match_count=sum(r['equals_reference_settle'] for r in zero_quotes),
                all_differences_remain_unresolved=True, data_imputed=False,
                market_requests=0, calendar_qualified=False, financial_eligible=False)


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--package', required=True, type=Path)
    p.add_argument('--out', required=True, type=Path)
    args = p.parse_args()
    if args.out.resolve().is_relative_to(args.package.resolve()):
        raise ValueError('output_inside_sealed_package')
    report = review(args.package)
    args.out.write_text(json.dumps(report, ensure_ascii=False, indent=2)+'\n', encoding='utf-8', newline='\n')
    print(json.dumps({k:v for k,v in report.items() if k not in ('quantity_cases','zero_quote_cases')}, ensure_ascii=False))


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    main()
