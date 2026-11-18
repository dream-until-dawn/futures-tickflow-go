"""冻结既有官方原生观察及证据文件；不推定生命周期、主连或金融准入。"""
import argparse
import collections
import hashlib
import io
import json
from pathlib import Path
import re
import sys

CATALOG_SHA = '57c148fa66ce8276f92ee7883fe0aa909a643559efaab23833663caea868b3d8'
PLAN_SHA = 'c5edff30c9dcab004db8f3aaddb3ee4efedb28eec17fef398c4733e3b0a12980'
REPAIR_SHA = '387085a7a5aa6893d4331a6dea180e9481456a814b5358f70d27298f97d63aab'
PRODUCTS = {'SHFE.hc': ('SHFE', r'hc[0-9]{4}'), 'CZCE.RM': ('CZCE', r'RM[0-9]{3,4}')}


def sha(raw):
    return hashlib.sha256(raw).hexdigest()


def encoded(value):
    return (json.dumps(value, ensure_ascii=False, sort_keys=True) + '\n').encode()


def checked(path, expected, size=None):
    raw = Path(path).read_bytes()
    if sha(raw) != expected or (size is not None and len(raw) != size):
        raise ValueError('native_handoff_source_sha:' + str(path))
    return raw


def verify(root, expected_manifest):
    """迁移后逐件验身份；拒绝多余文件、重复声明和越界路径。"""
    manifest = json.loads(checked(root / 'manifest.json', expected_manifest))
    declared = set()
    for member in manifest['members']:
        name = member['path']
        path = Path(name)
        if (path.is_absolute() or path.drive or '\\' in name or ':' in name or
                '..' in path.parts or name != path.as_posix() or name in declared):
            raise ValueError('native_handoff_member_path')
        declared.add(name)
        target = root / path
        if target.is_symlink() or not target.resolve().is_relative_to(root.resolve()):
            raise ValueError('native_handoff_member_path')
        checked(target, member['sha256'], member['bytes'])
    actual = {p.relative_to(root).as_posix() for p in root.rglob('*') if p.is_file()}
    if actual != declared | {'manifest.json'}:
        raise ValueError('native_handoff_member_set')
    return len(actual)


def summarize(rows):
    """观察全集与有效价格分开；无效OHLC行中的真实结算仍保留。"""
    seen = set()
    years = {}
    days = collections.defaultdict(list)
    for row in rows:
        r = row['record']
        identity = (r['day'], r['native'])
        if identity in seen:
            raise ValueError('native_handoff_duplicate_contract_day')
        seen.add(identity)
        if row['state'] not in {'price-readable', 'quarantined'}:
            raise ValueError('native_handoff_unknown_state')
        year = str(r['day'])[:4]
        y = years.setdefault(year, dict(rows=0, price_readable=0, quarantined=0,
                                        nonempty_settlement=0, volume_and_oi=0, days=set()))
        y['rows'] += 1
        y[row['state'].replace('-', '_')] += 1
        y['days'].add(r['day'])
        f = r['fields']
        y['nonempty_settlement'] += f.get('settle') not in (None, '')
        y['volume_and_oi'] += all(f.get(k) not in (None, '') for k in ('volume', 'open_interest'))
        days[r['day']].append(dict(native=r['native'], state=row['state'],
                                  document_id=row['document_id']))
    for y in years.values():
        observed = y.pop('days')
        y.update(observed_days=len(observed), first=min(observed), last=max(observed))
    return years, [dict(day=d, observed_candidates=sorted(v, key=lambda x: x['native']),
                        legal_listing_universe_verified=False)
                   for d, v in sorted(days.items())]


def build(catalog, out):
    """先核冻结输入，输出到全新目录；失败目录不得充当成功批次。"""
    if out.exists():
        raise ValueError('native_handoff_output_exists')
    plan_raw = checked(catalog / 'parse-plan.json', PLAN_SHA)
    records_path = catalog / 'catalog-v2/records.jsonl'
    # 流式校验完整父目录，避免把局部相符当成完整目录身份。
    h = hashlib.sha256()
    selected = {key: [] for key in PRODUCTS}
    with records_path.open('rb') as stream:
        for line in stream:
            h.update(line)
            row = json.loads(line)
            r = row['record']
            for key, (exchange, pattern) in PRODUCTS.items():
                if r['exchange'] == exchange and re.fullmatch(pattern, r['native']):
                    selected[key].append(row)
    if h.hexdigest() != CATALOG_SHA:
        raise ValueError('native_handoff_catalog_sha')
    documents = {d['id']: d for d in json.loads(plan_raw)['documents']}
    repair_path = catalog / 'recovered-20210728/parse-plan.json'
    repair_raw = checked(repair_path, REPAIR_SHA)
    for d in json.loads(repair_raw)['documents']:
        if d['id'] in documents:
            raise ValueError('native_handoff_duplicate_document')
        documents[d['id']] = d
    out.mkdir(parents=True)
    members = {}

    def store(name, raw):
        if name in members:
            if members[name]['sha256'] != sha(raw):
                raise ValueError('native_handoff_member_collision')
            return name
        path = out / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(raw)
        members[name] = dict(path=name, bytes=len(raw), sha256=sha(raw))
        return name

    store('parents/parse-plan.json', plan_raw)
    store('parents/recovery-plan.json', repair_raw)
    reports = {}
    for key, rows in selected.items():
        if not rows:
            raise ValueError('native_handoff_empty_product')
        years, candidates = summarize(rows)
        references = []
        ids = {row['document_id'] for row in rows}
        for identity in sorted(ids):
            d = documents[identity]
            expected = d.get('origin') or d['evidence']
            # 每条行证据必须绑定同一原件；不凭document_id名称相信来源。
            for row in (r for r in rows if r['document_id'] == identity):
                if row['record']['evidence']['sha256'] != expected['sha256']:
                    raise ValueError('native_handoff_row_original_binding')
            parsed = checked(d['file'], d['sha256'], d.get('bytes'))
            parsed_member = store('objects/' + sha(parsed), parsed)
            original = checked(d.get('original_file', d['file']), expected['sha256'])
            original_member = store('objects/' + sha(original), original)
            receipt_path = Path(d['receipt_file']) if d.get('receipt_file') else Path(d['file']).with_suffix('.json')
            if not receipt_path.exists():
                receipt_path = receipt_path.parent / 'export-manifest.json'
            receipt = receipt_path.read_bytes()
            if d.get('receipt_sha256') and sha(receipt) != d['receipt_sha256']:
                raise ValueError('native_handoff_receipt_sha')
            if not d.get('receipt_sha256'):
                receipt_json = json.loads(receipt)
                if 'records' in receipt_json:
                    entries = [v['receipt'] for v in receipt_json['records'] if v['id'] == expected['id']]
                    if len(entries) != 1:
                        raise ValueError('native_handoff_receipt_identity')
                    receipt_json = entries[0]
                if receipt_json.get('evidence', {}).get('sha256') != expected['sha256']:
                    raise ValueError('native_handoff_receipt_identity')
            receipt_member = store('objects/' + sha(receipt), receipt)
            references.append(dict(document_id=identity, source=expected, format=d['format'],
                                   parsed=parsed_member, original=original_member, receipt=receipt_member))
        prefix = key + '/'
        records_member = store(prefix + 'native-records.jsonl', b''.join(encoded(r) for r in sorted(
            rows, key=lambda r: (r['record']['day'], r['record']['native']))))
        candidates_member = store(prefix + 'observed-candidates.jsonl', b''.join(map(encoded, candidates)))
        reference_member = store(prefix + 'sources.json', encoded(references))
        reports[key] = dict(rows=len(rows), years=years, records=members[records_member],
                            candidates=members[candidates_member], sources=members[reference_member],
                            verified_source_documents=len(references))
    report = dict(schema='native-observation-handoff-v1', network_requests=0,
                  parent_catalog_sha256=CATALOG_SHA, parent_plan_sha256=PLAN_SHA,
                  products=reports, independent_engine_acceptance='pending',
                  limitations=['原生代码与日期是观察身份，非上市退市全集；郑商所三位代码未推定规范年月',
                               '保留无效OHLC隔离行及原结算；不补价、不生成主连或信号选择',
                               '年度原ZIP与已解码输入均冻结，未重新解码工作簿或全字段复算',
                               '历史日历时段、金融规则、PIT及正式准入未认证；当前参数不外推'])
    store('report.json', encoded(report))
    manifest = dict(schema='native-observation-files-v1', members=sorted(members.values(), key=lambda a: a['path']))
    (out / 'manifest.json').write_bytes(encoded(manifest))
    return dict(report=report, manifest_sha256=sha(encoded(manifest)), output=str(out), files=len(members)+1)


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('catalog', type=Path)
    parser.add_argument('out', type=Path)
    args = parser.parse_args()
    print(json.dumps(build(args.catalog, args.out), ensure_ascii=False, indent=2))
