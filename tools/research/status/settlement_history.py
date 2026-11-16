"""从已冻结官方原件补全当前合约结算历史；零网络，不猜年份、不缩短指标。"""
import io
import sys
import argparse
import datetime as dt
from decimal import Decimal, InvalidOperation
import hashlib
import json
from pathlib import Path
import re

from reference_inventory import compare


def digest(raw):
    return hashlib.sha256(raw).hexdigest()


def locked(path, expected):
    raw = Path(path).read_bytes()
    if digest(raw) != expected:
        raise ValueError('warmup_input_sha:' + str(path))
    return raw


def current_symbol(record, bounds):
    """三位码只在显式上市/到期区间内绑定，避免把旧十年的同码当当前约。"""
    if record['exchange'] == 'CZCE':
        entry = bounds.get(record['native'])
        if not entry or entry[1] <= 0 or not entry[1] <= record['day'] <= entry[2]:
            return None
        return entry[0]
    if record['exchange'] in {'SHFE', 'INE'}:
        return record['exchange'] + '.' + record['native']
    return None


def select_records(lines, active, bounds, first, last):
    """完整哈希父目录，另选当前约既有合法结算；不以首尾日期补造中间样本。"""
    rows = {}
    hasher = hashlib.sha256()
    count = invalid = overlap = 0
    for line in lines:
        if len(line) > 65536:
            raise ValueError('warmup_record_line_budget')
        hasher.update(line)
        count += 1
        record = json.loads(line)['record']
        day = record['day']
        if type(day) is not int or not first <= day <= last:
            continue
        symbol = current_symbol(record, bounds)
        if symbol not in active:
            continue
        dt.date(day // 10000, day // 100 % 100, day % 100)
        try:
            value = Decimal(str(record['fields'].get('settle', '')).replace(',', ''))
        except InvalidOperation:
            invalid += 1
            continue
        if not value.is_finite() or value <= 0:
            invalid += 1
            continue
        key = (symbol, day)
        if key in rows:
            previous = Decimal(rows[key]['fields']['settle'].replace(',', ''))
            if previous != value:
                raise ValueError('warmup_conflicting_settlement:' + symbol + ':' + str(day))
            overlap += 1
            continue
        rows[key] = record
    return rows, dict(rows_checked=count, records_sha256=hasher.hexdigest(),
                      invalid_settlement_rows=invalid, same_value_overlap=overlap)


def build(spec, out, repo):
    """绑定全部来源后再建新输出；原响应、派生产物和规范序列分角色保存。"""
    if spec['schema'] != 'official-settlement-warmup-plan-v1' or spec['network_requests'] != 0:
        raise ValueError('warmup_plan_scope')
    if spec.get('lookback_observations') is not None:
        raise ValueError('warmup_requires_full_existing_sequence')
    if out.exists():
        raise ValueError('warmup_output_exists')
    seed_root = Path(spec['seed_root'])
    seed = json.loads(locked(seed_root / 'manifest.json', spec['seed_manifest_sha256']))
    if seed['synthetic'] is not False or seed['current_contracts']:
        raise ValueError('warmup_not_pure_real_signal')
    if (seed['policy']['latest_completed_day'] != spec['target_day'] or
            seed['policy']['current_trade_day'] != spec['signal_trade_day'] or
            not spec['history_from'] <= spec['history_to'] < spec['target_day']):
        raise ValueError('warmup_day_scope')
    active = {s for p in seed['products'] if p['gates']['daily_signal']['status'] == 'pass'
              for s in p['gates']['daily_signal']['eligible_contracts']}
    if len(active) != spec['expected_eligible_contracts']:
        raise ValueError('warmup_eligible_scope')
    ref_raw = locked(spec['reference_body'], spec['reference_body_sha256'])
    review = json.loads(locked(spec['reference_review'], spec['reference_review_sha256']))
    identities = compare(ref_raw, review)
    bounds = {native: (canonical, first, last) for canonical, (native, first, last) in identities.items()}
    meta = json.loads(locked(spec['catalog_manifest'], spec['catalog_manifest_sha256']))
    records_path = Path(spec['catalog_manifest']).parent / 'records.jsonl'
    if records_path.stat().st_size > spec['max_records_bytes']:
        raise ValueError('warmup_records_byte_budget')
    with records_path.open('rb') as stream:
        rows, stats = select_records(iter(lambda: stream.readline(65537), b''), active, bounds,
                                     spec['history_from'], spec['history_to'])
    if (stats['rows_checked'] != spec['expected_catalog_rows'] or stats['rows_checked'] != meta['rows'] or
            stats['records_sha256'] != spec['records_sha256'] or stats['records_sha256'] != meta['records_sha256']):
        raise ValueError('warmup_catalog_identity')
    parse_raw = locked(spec['parse_plan'], spec['parse_plan_sha256'])
    parse_plan = json.loads(parse_raw)
    documents = {d['id']: d for d in parse_plan['documents']}
    if len(documents) != len(parse_plan['documents']):
        raise ValueError('warmup_duplicate_document_id')
    payloads = {}
    def add(id, role, path, body, url=None, source_day=None, received=None, version='official-settlement-history-v1'):
        if id in payloads or not re.fullmatch(r'[A-Za-z0-9_.-]+', id):
            raise ValueError('warmup_artifact_identity')
        payloads[id] = (dict(id=id, role=role, path=path, bytes=len(body), sha256=digest(body),
                            source_url=url, source_day=source_day, retrieved_at=received, version=version), body)
    for artifact in seed['artifacts']:
        path = (seed_root / artifact['path']).resolve()
        if not path.is_relative_to(seed_root.resolve()):
            raise ValueError('warmup_seed_path')
        raw = locked(path, artifact['sha256'])
        if len(raw) != artifact['bytes']:
            raise ValueError('warmup_seed_bytes')
        payloads[artifact['id']] = (dict(artifact), raw)
    sources = {}
    for row in rows.values():
        evidence = row['evidence']
        if evidence['id'] in sources and sources[evidence['id']]['sha256'] != evidence['sha256']:
            raise ValueError('warmup_source_version_conflict')
        sources[evidence['id']] = evidence
    if len(sources) > spec['max_sources']:
        raise ValueError('warmup_source_count_budget')
    source_bytes = derived_bytes = 0
    derivations = {}
    for id, evidence in sorted(sources.items()):
        document = documents.get(id)
        if document is None:
            raise ValueError('warmup_source_not_registered:' + id)
        raw_path = document.get('original_file', document['file'])
        if source_bytes + Path(raw_path).stat().st_size > spec['max_source_bytes']:
            raise ValueError('warmup_source_byte_budget')
        body = locked(raw_path, evidence['sha256'])
        source_bytes += len(body)
        if source_bytes > spec['max_source_bytes']:
            raise ValueError('warmup_source_byte_budget')
        annual = document['format'] == 'annual-decoded-v1'
        unknown_http = 'first original HTTP UTC unresolved' in evidence.get('version', '')
        add('warmup-' + id, 'source-original', 'history-originals/' + id + '.body', body,
            evidence['url'], None if annual else document['day'],
            None if unknown_http else evidence.get('retrieved_at'), evidence.get('version', ''))
        if digest(body) != document['sha256']:
            if derived_bytes + Path(document['file']).stat().st_size > spec['max_derived_bytes']:
                raise ValueError('warmup_derived_byte_budget')
            decoded = locked(document['file'], document['sha256'])
            derived_bytes += len(decoded)
            if derived_bytes > spec['max_derived_bytes']:
                raise ValueError('warmup_derived_byte_budget')
            derived_id = 'decoded-' + id
            add(derived_id, 'derived-source', 'decoded/' + id + '.json', decoded,
                version='派生解码产物，非原始响应；解码器SHA ' + parse_plan['decoder_sha256'])
            derivations[id] = derived_id
        if document.get('receipt_file'):
            receipt = locked(document['receipt_file'], document['receipt_sha256'])
            add('receipt-' + id, 'acquisition-receipt', 'history-receipts/' + id + '.json', receipt)
    # 年度归档附原解码器及父解析计划，便于重做原件→派生数据的链路。
    decoder = locked(repo / 'tools/archive/decode_annual.py', parse_plan['decoder_sha256'])
    add('annual-decoder', 'derivation-code', 'derivation/decode_annual.py', decoder)
    add('history-parent-plan', 'review-evidence', 'derivation/parent-plan.json', parse_raw)
    normalized = []
    lineage = []
    for (symbol, day), record in sorted(rows.items(), key=lambda pair: (pair[0][1], pair[0][0])):
        value = Decimal(record['fields']['settle'].replace(',', ''))
        values = {k: None for k in ('open', 'high', 'low', 'close', 'volume', 'open_interest', 'turnover')}
        values['settle'] = format(value, 'f')
        normalized.append(dict(product_key=record['exchange'] + '.' + re.match(r'[A-Za-z]+', record['native'])[0],
                               symbol=symbol, source_native=record['native'], day=day, fields=values,
                               source_id='warmup-' + record['evidence']['id']))
        lineage.append(dict(symbol=symbol, day=day, source_id='warmup-' + record['evidence']['id'],
                            member=record.get('member'), row=record.get('row'),
                            decoded_source_id=derivations.get(record['evidence']['id'])))
    target_raw = payloads['daily-records'][1]
    target = [json.loads(line) for line in target_raw.splitlines()]
    if any(r['day'] != spec['target_day'] for r in target):
        raise ValueError('warmup_target_seed_day')
    normalized.extend(target)
    if len({(r['symbol'], r['day']) for r in normalized}) != len(normalized):
        raise ValueError('warmup_duplicate_normalized_contract_day')
    combined = ''.join(json.dumps(r, ensure_ascii=False, separators=(',', ':')) + '\n' for r in normalized).encode()
    artifact, _ = payloads['daily-records']
    artifact.update(bytes=len(combined), sha256=digest(combined), version='完整既有结算用于预热；非连续日历或历史制度认证')
    payloads['daily-records'] = (artifact, combined)
    lineage_raw = ''.join(json.dumps(r, ensure_ascii=False, separators=(',', ':')) + '\n' for r in lineage).encode()
    add('history-row-lineage', 'review-evidence', 'derivation/row-lineage.jsonl', lineage_raw)
    observations = {}
    for record in normalized:
        if record['symbol'] in active:
            observations[record['symbol']] = observations.get(record['symbol'], 0) + 1
    report = dict(stats, history_rows=len(rows), target_rows=len(target), normalized_rows=len(normalized),
                  sources=len(sources), source_bytes=source_bytes, derived_bytes=derived_bytes,
                  observations_by_contract=observations, actual_strategy_ready='not_yet_tested')
    report_raw = (json.dumps(report, ensure_ascii=False, indent=2) + '\n').encode()
    add('history-selection-report', 'review-evidence', 'derivation/selection-report.json', report_raw)
    seed['artifacts'] = [artifact for artifact, _ in payloads.values()]
    seed['limitations'] += [
        '历史部分只交付真实settle，其他列null表示本包未交付，不表示原表无值；目标日原字段不改',
        '每约使用2021起能明确绑定的全部已有结算，不截64、不猜主连、不补日历缺口或未来数据',
        '现行CN MACD实际第607观察日才可ready；真实可就绪数量须消费者实测，不由数据长度冒认',
        '最初HTTP时间未知的旧原件仍标null，留存/解码/打包时间不能冒充取得时间',
        'current_contracts为空，保证金和最低开仓不适用于本pure-signal包；不输出订单或真实前向样本']
    out.mkdir(parents=True)
    for artifact, raw in payloads.values():
        path = (out / artifact['path']).resolve()
        if not path.is_relative_to(out.resolve()):
            raise ValueError('warmup_output_path')
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(raw)
    manifest = (json.dumps(seed, ensure_ascii=False, indent=2) + '\n').encode()
    (out / 'manifest.json').write_bytes(manifest)
    return dict(history_rows=len(rows), target_rows=len(target), normalized_rows=len(normalized),
                artifacts=len(payloads), manifest_sha256=digest(manifest),
                source_bytes=source_bytes, derived_bytes=derived_bytes)


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--spec', type=Path, required=True)
    parser.add_argument('--out', type=Path, required=True)
    parser.add_argument('--repo', type=Path, default=Path(__file__).resolve().parents[3])
    args = parser.parse_args()
    print(json.dumps(build(json.loads(args.spec.read_bytes()), args.out, args.repo), ensure_ascii=False))
