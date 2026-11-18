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
EXTENSION_LEDGER_SHA = '5a71f410b809472cb0ef7d80c9773dcf01ea5f0cd5c01623db6578cf0f830a52'
EXTENSION_DAYS = {'20261104','20261105','20261106','20261109','20261110','20261111','20261112'}


def extension_entries(ledger):
    selected = [e for e in ledger if e['day'] > '20261103']
    expected = {(d, k) for d in EXTENSION_DAYS for k in KINDS}
    expected |= {('20261113', k) for k in KINDS - {'Settlement'}}
    actual = [(e['day'], e['kind']) for e in selected]
    if len(actual) != len(set(actual)) or set(actual) != expected:
        raise ValueError('hc_parameters_extension_exact_scope')
    return selected


def extend(source, out):
    """单独保留23表增量；13日缺结算业务表，绝不伪报三表齐备。"""
    if out.exists():
        raise ValueError('hc_parameters_output_exists')
    ledger_raw = checked(source / 'ledger.json', EXTENSION_LEDGER_SHA)
    entries = extension_entries(json.loads(ledger_raw))
    out.mkdir(parents=True)
    members = []
    def store(name, raw):
        path = out / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(raw)
        members.append(dict(path=name, bytes=len(raw), sha256=sha(raw)))
    store('ledger.json', ledger_raw)
    selected = []
    scopes = collections.defaultdict(dict)
    for e in entries:
        if Path(e['file']).name != e['file'] or e['state'] != 'dated_table_validated' or e['http_status'] != 200:
            raise ValueError('hc_parameters_extension_source_identity')
        raw = checked(source / e['file'], e['sha256'], e['bytes'])
        rows = select(raw, e['day'], e['kind'])
        name = 'originals/' + e['file']
        store(name, raw)
        scopes[e['day']][e['kind']] = sorted(r['INSTRUMENTID'] for r in rows)
        selected.extend(dict(day=int(e['day']), kind=e['kind'], fields=r,
                             source=name, source_url=e['url'], effective_at=None,
                             actual_min_open_lots=None) for r in rows)
    for day, values in scopes.items():
        if len({tuple(v) for v in values.values()}) != 1:
            raise ValueError('hc_parameters_extension_contract_scope')
    store('hc-native-parameters.jsonl', b''.join(map(encoded, selected)))
    report = dict(schema='hc-native-parameters-extension-v1', network_requests=0,
                  parent_ledger_sha256=EXTENSION_LEDGER_SHA, tables=len(entries), hc_rows=len(selected),
                  complete_three_table_days=sorted(EXTENSION_DAYS),
                  incomplete_days={'20261113':['Settlement']}, independent_engine_acceptance='pending',
                  limitations=['表日/生效/PIT仍分开；不把13日两表说成三表齐备',
                               '未补最低开仓、专项费用或金融准入；原件不覆写'])
    store('report.json', encoded(report))
    manifest = encoded(dict(schema='hc-native-parameter-files-v1', members=members))
    (out / 'manifest.json').write_bytes(manifest)
    assert verify(out, sha(manifest)) == len(members) + 1
    return dict(report=report, manifest_sha256=sha(manifest), output=str(out))


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
    parser.add_argument('--extension', action='store_true')
    args = parser.parse_args()
    print(json.dumps((extend if args.extension else build)(args.source, args.out), ensure_ascii=False, indent=2))
