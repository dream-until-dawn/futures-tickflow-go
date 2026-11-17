"""从不可变v1父包和已盘点HC原件构建v2对照包；不联网，不设置模拟手数。"""
import argparse
import copy
import csv
from decimal import Decimal, InvalidOperation
import hashlib
import io
import json
from pathlib import Path
import sys
from urllib.parse import parse_qs, urlparse

from data_gates import current_margin, effective_minimum, evaluate, item
from rb_backtest_bundle import normalize


PRODUCTS = {'SHFE.rb', 'SHFE.hc'}
PARENT_SHA = '20c0b2a8665ae02ed9701c793e3e5c995e3938da3e8bfd37d0478b56f0ab1c69'


def digest(raw):
    return hashlib.sha256(raw).hexdigest()


def valid_official_ohlc(fields):
    """原表空报价继续为空，不为复用EDB解析器而伪造价格或量仓零值。"""
    try:
        o, h, l, c = (Decimal(fields[name]) for name in ('open', 'high', 'low', 'close'))
        return all(value.is_finite() for value in (o, h, l, c)) and 0 < l <= min(o, c) <= max(o, c) <= h
    except (InvalidOperation, TypeError, KeyError):
        return False


def verified_file(root, artifact):
    path = (root / artifact['path']).resolve()
    if not path.is_relative_to(root.resolve()):
        raise ValueError('v2_source_path')
    raw = path.read_bytes()
    if len(raw) != artifact['bytes'] or digest(raw) != artifact['sha256']:
        raise ValueError('v2_source_sha')
    return raw


def checked_parameters(current, raw, day):
    original = {r['INSTRUMENTID'].strip(): r for r in json.loads(raw)['ContractDailyTradeArgument']}
    for row in current:
        source = original.get(row['source_native'])
        if not source or int(source['TRADINGDAY']) != day or not current_margin(row, day):
            raise ValueError('v2_parameter_day_or_scope')
        if (row['margin']['basis'] != 'exchange_spec' or
                row['margin']['long'] != source['SPEC_LONGMARGINRATIO'] or
                row['margin']['short'] != source['SPEC_SHORTMARGINRATIO']):
            raise ValueError('v2_parameter_original_value')


def coverage(rows, target):
    dates = {r['day'] for r in rows}
    years = {str(y): len({d for d in dates if d // 10000 == y}) for y in range(2021, 2027)}
    if not dates or min(dates) > 20211231 or max(dates) != target or any(v == 0 for v in years.values()):
        raise ValueError('v2_history_baseline')
    return dict(first=min(dates), last=max(dates), listing_day=None, valid_days_by_year=years)


def build(repo, parent, out):
    if out.exists():
        raise ValueError('v2_output_exists')
    parent_raw = (parent / 'manifest.json').read_bytes()
    if digest(parent_raw) != PARENT_SHA:
        raise ValueError('v2_parent_identity')
    manifest = json.loads(parent_raw)
    if manifest['schema'] != 'user-data-gates-v1' or manifest['synthetic'] is not False:
        raise ValueError('v2_parent_version')
    payloads = {}
    for artifact in manifest['artifacts']:
        if artifact['id'] in payloads:
            raise ValueError('v2_duplicate_artifact')
        payloads[artifact['id']] = (copy.deepcopy(artifact), verified_file(parent, artifact))

    def add(identity, role, path, raw, url=None, received=None):
        if identity in payloads:
            raise ValueError('v2_duplicate_artifact')
        payloads[identity] = (dict(id=identity, role=role, path=path, bytes=len(raw), sha256=digest(raw),
                                   source_url=url, source_day=None, retrieved_at=received,
                                   version='two-product-v2-current-parameter-scenario'), raw)

    facts = json.loads((repo / 'docs/data-gate-facts.json').read_bytes())
    day, target = facts['current_trade_day'], facts['latest_completed_day']
    if (day, target) != (20261117, 20261116):
        raise ValueError('v2_frozen_days')
    current = [r for r in facts['current_contracts'] if r['product_key'] in PRODUCTS]
    checked_parameters(current, payloads['ContractDailyTradeArgument-20261117.json'][1], day)
    daily = [json.loads(line) for line in payloads['daily-records'][1].splitlines()]
    daily = [r for r in daily if r['product_key'] in PRODUCTS]
    quote = ''.join(json.dumps(r, ensure_ascii=False, separators=(',', ':')) + '\n' for r in daily).encode()
    artifact = payloads['daily-records'][0]
    artifact.update(bytes=len(quote), sha256=digest(quote), version='two-product-target-day-projection-v2')
    payloads['daily-records'] = (artifact, quote)

    inventory = json.loads((repo / 'docs/evidence/project-data-inventory-20261116.json').read_bytes())
    pinned = {str(Path(r['path']).resolve()).casefold(): r['sha256'] for r in inventory['input_files']}
    config = json.loads((repo / 'tools/research/status/project-sources.json').read_bytes())
    history, seen, invalid = {}, set(), 0
    for entry in config['edb_roots']:
        for body in sorted(Path(entry['path']).rglob('hc*.body')):
            raw = body.read_bytes()
            if not raw.startswith(b'datetime_nano,'):
                continue
            receipt_raw = body.with_suffix('.json').read_bytes()
            receipt = json.loads(receipt_raw)
            evidence = receipt['evidence']
            query = parse_qs(urlparse(evidence['url']).query)
            symbol = query.get('symbol', [''])[0]
            if not symbol.startswith('SHFE.hc') or query.get('period') != ['86400'] or receipt['state'] != 'received':
                continue
            sha = digest(raw)
            if pinned.get(str(body.resolve()).casefold()) != sha or sha != evidence['sha256']:
                raise ValueError('v2_history_unpinned_source')
            if sha in seen:
                continue
            seen.add(sha)
            identity = 'hc-history-source-' + str(len(seen))
            add(identity, 'source-original', 'history-originals/' + identity + '.csv', raw,
                evidence['url'], evidence.get('retrieved_at'))
            add(identity + '-receipt', 'acquisition-receipt', 'history-receipts/' + identity + '.json', receipt_raw)
            for row in csv.DictReader(io.StringIO(raw.decode('utf-8'))):
                normalized = normalize(row)
                if normalized is None:
                    invalid += 1
                    continue
                date, fields = normalized
                if not 20210101 <= date <= 20261113:
                    continue
                record = dict(product_key='SHFE.hc', symbol=symbol, source_native=symbol.split('.')[1],
                              day=date, fields=fields, source_id=identity)
                key = symbol, date
                if key in history and history[key]['fields'] != fields:
                    raise ValueError('v2_history_source_conflict')
                history.setdefault(key, record)
    for record in daily:
        if record['product_key'] == 'SHFE.hc':
            if not valid_official_ohlc(record['fields']):
                invalid += 1
                continue
            history[(record['symbol'], record['day'])] = record
    hc_rows = [history[key] for key in sorted(history, key=lambda key: (key[1], key[0]))]
    raw = ''.join(json.dumps(r, ensure_ascii=False, separators=(',', ':')) + '\n' for r in hc_rows).encode()
    add('hc-history', 'daily-records', 'hc-history.jsonl', raw)
    products = []
    for product in manifest['products']:
        key = product['key']
        if key not in PRODUCTS:
            continue
        identity = 'rb-history' if key == 'SHFE.rb' else 'hc-history'
        rows = [json.loads(line) for line in payloads[identity][1].splitlines()]
        proof = coverage(rows, target)
        proof['artifact_ids'] = [identity]
        scope = [r['symbol'] for r in current if r['product_key'] == key]
        hard = dict(history_ohlc=item('pass', sorted({r['symbol'] for r in rows}), [identity],
                                     '自2021逐年有效观察，保留源版本及缺日边界'),
                    current_margin=item('pass', scope, ['ContractDailyTradeArgument-20261117.json'],
                                        '17日交易所投机多空原件的当前参数情景'))
        hard['history_ohlc']['coverage'] = proof
        product['gates']['backtest'] = evaluate(hard, 'backtest', version=2)
        minimum = [r['symbol'] for r in current if r['product_key'] == key and effective_minimum(r, day)]
        product['supplementary'] = dict(effective_min_open=item('pass' if minimum else 'pending', minimum,
            ['rb-minimum-model-review'] if minimum else [], '具名RB模型审核' if minimum else '实际未知，v2不以此阻断回测'))
        product['simulation_assumptions'] = dict(minimum_open_lots=None, configured_by='consumer-task',
                                               is_actual_rule=False, applied_to_engine=False)
        products.append(product)
    if {p['key'] for p in products} != PRODUCTS or not current:
        raise ValueError('v2_product_scope')
    add('v1-parent-manifest', 'review-evidence', 'derivation/v1-parent-manifest.json', parent_raw)
    manifest.update(schema='user-data-gates-v2', products=products, current_contracts=current)
    manifest['policy'].update(backtest_hard_items=['history_ohlc', 'current_margin'],
                              daily_signal_hard_items=['settlement', 'daily_update'])
    manifest['limitations'] = ['仅RB/HC现有价格的v2离线对照包；不是42品种完整可执行交付',
        '当前保证金回放历史是情景；实际最低开仓未知仍null，任务数量须显式配置，不代表真实客户可交易',
        'EDB版本、零成交语义、量仓差异及全历史完整分母未闭合，不拼主连、不补缺日、不以close填settle',
        '原始表含其他品种仅为完整原件；消费投影范围仅products/current_contracts，未注册或部署任务',
        'HC基本无效OHLC剔除' + str(invalid) + '行；原件保持，价格逐行source_id可查']
    manifest['artifacts'] = [a for a, raw in payloads.values()]
    out.mkdir(parents=True)
    for a, raw in payloads.values():
        path = out / a['path']
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(raw)
    manifest_raw = (json.dumps(manifest, ensure_ascii=False, indent=2) + '\n').encode()
    (out / 'manifest.json').write_bytes(manifest_raw)
    return dict(manifest_sha256=digest(manifest_raw), artifacts=len(payloads), hc_rows=len(hc_rows),
                hc_sources=len(seen), current_contracts=len(current), hc_invalid_ohlc_excluded=invalid)


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ('repo', 'parent', 'out'):
        parser.add_argument('--' + name, type=Path, required=True)
    args = parser.parse_args()
    print(json.dumps(build(args.repo, args.parent, args.out), ensure_ascii=False))
