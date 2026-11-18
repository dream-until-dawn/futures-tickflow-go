"""冻结已有HC逐日业务表原行；只证明表内观察，不推定规则生效时刻。"""
import argparse
import collections
from decimal import Decimal
import io
import json
from pathlib import Path
import re
import sys

from native_handoff import checked, encoded, sha, verify

LEDGER_SHA = '497819e8c28ea1a9cc7f2c92b997b4dc9d3a6d27df7f7a8f83d91f45f4c96132'
KINDS = {'ContractBaseInfo', 'ContractDailyTradeArgument', 'Settlement'}


def select(raw, day, kind):
    value = json.loads(raw)
    if kind not in KINDS or str(value['report_date']).replace('-', '')[:8] != day:
        raise ValueError('hc_parameters_table_day')
    rows = [r for r in value[kind] if re.fullmatch(r'hc[0-9]{4}', r.get('INSTRUMENTID', ''))]
    if not rows:
        raise ValueError('hc_parameters_empty_hc')
    seen = set()
    for row in rows:
        if str(row['TRADINGDAY']) != day:
            raise ValueError('hc_parameters_row_day')
        if row['INSTRUMENTID'] in seen:
            raise ValueError('hc_parameters_duplicate_contract')
        seen.add(row['INSTRUMENTID'])
        # 仅做量纲明确的比例范围检查；不猜费用生效时点或实际最低开仓。
        for key, literal in row.items():
            if key.endswith('MARGINRATIO'):
                ratio = Decimal(literal)
                if not ratio.is_finite() or not 0 < ratio <= 1:
                    raise ValueError('hc_parameters_margin_ratio')
    return rows


def build(source, out):
    if out.exists():
        raise ValueError('hc_parameters_output_exists')
    ledger_raw = checked(source / 'ledger.json', LEDGER_SHA)
    ledger = json.loads(ledger_raw)
    out.mkdir(parents=True)
    (out / 'ledger.json').write_bytes(ledger_raw)
    years = {}
    for entry in ledger:
        day, kind = entry['day'], entry['kind']
        if kind not in KINDS or entry['state'] != 'dated_table_validated' or entry['http_status'] != 200:
            raise ValueError('hc_parameters_ledger_state')
        if Path(entry['file']).name != entry['file']:
            raise ValueError('hc_parameters_source_path')
        raw = checked(source / entry['file'], entry['sha256'], entry['bytes'])
        rows = select(raw, day, kind)
        year = day[:4]
        y = years.setdefault(year, dict(files=[], rows=[], scopes=collections.defaultdict(dict)))
        if kind in y['scopes'][day]:
            raise ValueError('hc_parameters_duplicate_table')
        y['scopes'][day][kind] = sorted(r['INSTRUMENTID'] for r in rows)
        path = 'originals/' + entry['file']
        target = out / year / path
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(raw)
        y['files'].append(dict(path=path, bytes=len(raw), sha256=sha(raw), source_url=entry['url'],
                               table_day=day, kind=kind, original_http_time=None))
        y['rows'].extend(dict(day=int(day), kind=kind, fields=row, source=path,
                             effective_at=None, actual_min_open_lots=None) for row in rows)
    result = {}
    for year, y in sorted(years.items()):
        for day, scopes in y['scopes'].items():
            if set(scopes) != KINDS or len({tuple(v) for v in scopes.values()}) != 1:
                raise ValueError('hc_parameters_three_table_contract_scope')
        raw = b''.join(encoded(r) for r in y['rows'])
        (out / year / 'hc-native-parameters.jsonl').write_bytes(raw)
        y['files'].append(dict(path='hc-native-parameters.jsonl', bytes=len(raw), sha256=sha(raw)))
        manifest = encoded(dict(schema='hc-native-parameter-files-v1', members=y['files']))
        (out / year / 'manifest.json').write_bytes(manifest)
        assert verify(out / year, sha(manifest)) == len(y['files']) + 1
        days = sorted(y['scopes'])
        result[year] = dict(days=len(days), first=days[0], last=days[-1], tables=len(y['files'])-1,
                            hc_rows=len(y['rows']), source_bytes=sum(r['bytes'] for r in y['files']),
                            manifest_sha256=sha(manifest))
    report = dict(schema='hc-native-parameters-handoff-v1', network_requests=0, parent_ledger_sha256=LEDGER_SHA,
                  years=result, independent_engine_acceptance='pending',
                  limitations=['原表日不自动等于盘前生效或历史首次可知时点；effective_at为null',
                               '原账本没有每次HTTP原始回执；只核已冻结字节，不认证PIT',
                               '原字段不改单位，不推定开平今手续费完整；实际最低开仓未知保留null',
                               '三表候选相符不自动认证法定挂牌全集、时段日历或金融准入'])
    (out / 'report.json').write_bytes(encoded(report))
    return report


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('source', type=Path)
    parser.add_argument('out', type=Path)
    args = parser.parse_args()
    print(json.dumps(build(args.source, args.out), ensure_ascii=False, indent=2))
