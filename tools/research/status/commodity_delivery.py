"""从已核冻结原件生成五交易所自包含商品价格子包，不联网、不改旧包。"""
import argparse
import copy
import csv
import hashlib
import io
import json
from pathlib import Path
import sys
from urllib.parse import parse_qs, urlparse
import zipfile

from backtest_v2_bundle import checked_parameters, valid_official_ohlc, verified_file
from broker_margin import build_current_facts
from commodity_prices import UniquePrices, canonical, coverage, normalize
from data_gates import current_margin, evaluate, item

SIGNAL_SHA = 'e6c8a72c101e0111eb331c0a8db029410ff99765575928f43182bb2e7ac41a1d'
CONTROL_SHA = '47d988f619fa978736d2ae7774fd39964ab28698d4479de657e6b9946e8876e1'
COMPANY_SHA = 'c10005856b7b9299295185505a50658442ae12dfaf96acc20cfaf7ba102410a1'
DIRECTORY_SHA = 'c7ff4e26edda99e694149e16e4790050f4e847bebb4ad940b704340c64632545'


def encode(value):
    return (json.dumps(value, ensure_ascii=False, indent=2) + '\n').encode()


def digest(raw):
    return hashlib.sha256(raw).hexdigest()


def policy(day, target):
    return dict(baseline_year=2021,current_trade_day=day,latest_completed_day=target,
        minimum_lots_kind='effective_min_open',backtest_hard_items=['history_ohlc','current_margin'],
        daily_signal_hard_items=['settlement','daily_update'])


def read_bundle(root, expected):
    raw = (root / 'manifest.json').read_bytes()
    if digest(raw) != expected:
        raise ValueError('commodity_parent_manifest')
    m = json.loads(raw)
    return m, {a['id']: (a, verified_file(root, a)) for a in m['artifacts']}


def company_bundle(path):
    """核已验父包的完整成员集；同名重复也拒绝，不能靠字典折叠隐藏。"""
    with zipfile.ZipFile(path) as archive:
        raw = archive.read('manifest.json')
        if digest(raw) != COMPANY_SHA:
            raise ValueError('commodity_company_manifest')
        m = json.loads(raw)
        if (len(archive.infolist())!=len(m['files'])+1 or any(i.is_dir() for i in archive.infolist()) or
                set(archive.namelist()) != {'manifest.json'} | {r['file'] for r in m['files']}):
            raise ValueError('commodity_company_members')
        files = {}
        for entry in m['files']:
            value = archive.read(entry['file'])
            if len(value) != entry['bytes'] or digest(value) != entry['sha256']:
                raise ValueError('commodity_company_source')
            files[entry['file']] = value
        return files


class Payloads:
    """声明与内存字节一同冻结；先核预算，最后才写全新子目录。"""
    def __init__(self):
        self.values = {}

    def add(self, identity, role, raw, url=None, day=None, received=None):
        if not identity or '/' in identity or '\\' in identity or ':' in identity or identity in {'.', '..'}:
            raise ValueError('commodity_artifact_path')
        if identity in self.values:
            if self.values[identity][1] != raw:
                raise ValueError('commodity_artifact_collision')
            return
        path = 'files/' + identity
        self.values[identity] = (dict(id=identity, role=role, path=path, bytes=len(raw), sha256=digest(raw),
                                     source_url=url, source_day=day, retrieved_at=received,
                                     version='commodity-current-parameter-scenario-v1'), raw)

    def write(self, out, manifest):
        manifest['artifacts'] = [a for a, _ in self.values.values()]
        raw = encode(manifest)
        total = len(raw) + sum(len(b) for _, b in self.values.values())
        if len(raw) > 4 * 1024**2 or total > 500 * 1024**2 or len(self.values) + 1 > 9900:
            raise ValueError('commodity_shard_budget')
        if any(len(b) > (32 if a['path'].endswith(('.json', '.jsonl')) else 128) * 1024**2 for a, b in self.values.values()):
            raise ValueError('commodity_member_budget')
        out.mkdir(parents=True)
        for a, b in self.values.values():
            p = out / a['path']; p.parent.mkdir(exist_ok=True); p.write_bytes(b)
        (out / 'manifest.json').write_bytes(raw)
        return dict(manifest_sha256=digest(raw), manifest_bytes=len(raw), source_bytes=total,
                    source_files=len(self.values)+1)


def build(repo, signal, control, company, directory, out):
    """绑定冻结资料日，不用本机日期或最新网页无声替换场景参数。"""
    if out.exists():
        raise ValueError('commodity_output_exists')
    facts_raw = (repo / 'docs/data-gate-facts.json').read_bytes(); facts = json.loads(facts_raw)
    pointer = json.loads((repo / 'docs/data-gate-assessment-current.json').read_bytes())
    assessment_raw = (repo / pointer['file']).read_bytes()
    if digest(assessment_raw) != pointer['sha256']:
        raise ValueError('commodity_assessment_sha')
    assessment = json.loads(assessment_raw)
    for entry in assessment['inputs']:
        raw = (repo / entry['path']).read_bytes()
        if len(raw) != entry['bytes'] or digest(raw) != entry['sha256']:
            raise ValueError('commodity_assessment_input')
    selected = {r['key']: r for r in assessment['products'] if r['gates']['backtest']['status'] == 'pass'}
    if len(selected) != 75 or any(k.startswith('CFFEX.') for k in selected):
        raise ValueError('commodity_75_scope')
    day, target = facts['current_trade_day'], facts['latest_completed_day']
    if (day, target) != (20261117, 20261116):
        raise ValueError('commodity_frozen_dates')
    inventory = json.loads((repo / 'docs/evidence/project-data-inventory-20261117.json').read_bytes())
    directory_raw = directory.read_bytes()
    if digest(directory_raw) != DIRECTORY_SHA:
        raise ValueError('commodity_directory_sha')
    aliases = {r['source_key']: r['canonical'] for r in json.loads(directory_raw)['contracts']}
    seed, seed_payloads = read_bundle(signal, SIGNAL_SHA)
    _, control_payloads = read_bundle(control, CONTROL_SHA)
    company_files = company_bundle(company)
    rebuilt, _ = build_current_facts(json.loads(company_files['base-facts.json']),
        company_files['company-original.html'], json.loads(company_files['company-receipt.json']),
        company_files['scope-original.gz'], json.loads(company_files['known-products.json']))
    rebuilt_rows = {r['symbol']: r for r in rebuilt['current_contracts']}
    current = [r for r in facts['current_contracts'] if r['product_key'] in selected and current_margin(r, day)]
    for row in current:
        if row != rebuilt_rows.get(row['symbol']):
            raise ValueError('commodity_current_recomputation:' + row['symbol'])
    exchange_rows = [r for r in current if r['margin']['basis'] == 'exchange_spec']
    checked_parameters(exchange_rows, control_payloads['ContractDailyTradeArgument-20261117.json'][1], day)
    if len(current) != 789:
        raise ValueError('commodity_current_scope')

    # 每个交易所只保留自身所需原件；完整原件可能含其他品种，不形成消费投影。
    shards = {ex: Payloads() for ex in ('SHFE', 'INE', 'CZCE', 'DCE', 'GFEX')}
    rows = UniquePrices(); invalid = {k: 0 for k in selected}; originals = 0
    for entry in inventory['input_files']:
        body = Path(entry['path'])
        if body.suffix != '.body':
            continue
        raw = body.read_bytes()
        if not raw.startswith(b'datetime_nano,'):
            continue
        receipt_raw = body.with_suffix('.json').read_bytes(); receipt = json.loads(receipt_raw)
        ev = receipt['evidence']; url = urlparse(ev['url']); query = parse_qs(url.query)
        if query.get('period') != ['86400']:
            continue
        source = query.get('symbol', [''])[0]
        if source not in aliases or aliases[source][:-4] not in selected:
            continue
        symbol = canonical(source, aliases); key = symbol[:-4]
        if key not in selected:
            continue
        if receipt['state'] != 'received' or len(raw) != entry['bytes'] or digest(raw) != entry['sha256'] or digest(raw) != ev['sha256']:
            raise ValueError('commodity_csv_original')
        if url.scheme != 'https' or url.netloc != 'edb.shinnytech.com' or url.path != '/md/kline':
            raise ValueError('commodity_csv_source')
        ex = key.split('.')[0]; pack = shards[ex]
        identity = 'edb-' + digest((ev['url'] + digest(raw)).encode())
        pack.add(identity, 'source-original', raw, ev['url'], received=ev.get('retrieved_at'))
        pack.add(identity+'-receipt.json', 'acquisition-receipt', receipt_raw)
        originals += 1
        for record in csv.DictReader(io.StringIO(raw.decode('utf-8'))):
            normalized = normalize(record)
            if normalized is None:
                invalid[key] += 1; continue
            date, fields = normalized
            if 20210101 <= date <= 20261113:
                rows.add(dict(product_key=key, symbol=symbol, source_native=source.split('.')[1],
                              day=date, fields=fields, source_id=identity))

    daily = [json.loads(line) for line in seed_payloads['daily-records'][1].splitlines()]
    for record in daily:
        if record['product_key'] in selected and valid_official_ohlc(record['fields']):
            rows.add(record)
    from official_prefix import extend
    prefixes = extend(rows, selected, facts, inventory,
                      json.loads((repo/'tools/research/status/project-sources.json').read_bytes()), shards, aliases)
    source_by_sha = {r['sha256']: Path(r['path']) for r in inventory['input_files']}
    # 早期官方上市原件按已核路径与SHA定位；不扫描负例、迁移或失败副本。
    for folder in ['tickflow-calendar-2025-20261112', 'tickflow-session-followup-20261112',
                   'tickflow-czce-px-sh-2023-20261113', 'tickflow-calendar-next-20261112']:
        for path in Path('C:/Temp', folder).glob('*.body'):
            source_by_sha.setdefault(digest(path.read_bytes()), path)
    names = {p['key']: p['name'] for p in json.loads((repo/'docs/data-status.json').read_bytes())['products']}
    index = dict(schema='commodity-price-delivery-index-v1', synthetic=False, products=75,
                 current_contracts=789, assessment_sha256=pointer['sha256'], shards=[])
    seed_products = {p['key']: p for p in seed['products']}
    for ex, pack in shards.items():
        keys = sorted(k for k in selected if k.startswith(ex+'.'))
        for identity in ['facts.json', 'assessment.json', 'canonical-directory.json']:
            pack.add(identity, 'review-evidence', {'facts.json': facts_raw, 'assessment.json': assessment_raw,
                                                 'canonical-directory.json': directory_raw}[identity])
        for identity, (artifact, raw) in control_payloads.items():
            if identity in {'ContractDailyTradeArgument-20261117.json', 'rb-minimum-model-review'}:
                pack.add(identity, artifact['role'], raw, artifact.get('source_url'))
        if ex in {'CZCE', 'DCE', 'GFEX'}:
            for name, raw in company_files.items():
                identity = 'broker-margin-20261117' if name == 'company-original.html' else 'company-'+name
                pack.add(identity, 'source-original' if name.endswith(('.html', '.gz')) else 'review-evidence', raw,
                         'https://www.minfutures.com/main/a/20261116/86319.shtml' if name == 'company-original.html' else None)
        projection = [r for r in daily if r['product_key'] in keys]
        if projection:
            for identity, (a, raw) in seed_payloads.items():
                if identity != 'daily-records':
                    pack.add(identity, a['role'], raw, a.get('source_url'), a.get('source_day'), a.get('retrieved_at'))
            pack.add('daily-records', 'daily-records', b''.join((json.dumps(r, ensure_ascii=False)+'\n').encode() for r in projection))
        products, statistics = [], []
        for key in keys:
            history = rows.product(key); listing = facts.get('listing_days', {}).get(key)
            try:
                proof = coverage(history, listing, target)
            except ValueError as error:
                raise ValueError('commodity_history_year_or_listing:' + key + ':' + str(
                    {str(y):sum(r['day']//10000==y for r in history) for y in range(2021,2027)})) from error
            history_id = key+'-history.jsonl'
            pack.add(history_id, 'daily-records', b''.join((json.dumps(r, ensure_ascii=False,separators=(',', ':'))+'\n').encode() for r in history))
            proof['artifact_ids'] = [history_id]
            gate = {name: copy.deepcopy(selected[key]['gates'][name]) for name in ('backtest', 'daily_signal')}
            hard = gate['backtest']['hard_items']
            hard['history_ohlc'].update(scope=sorted({r['symbol'] for r in history}), coverage=proof, evidence_ids=[history_id])
            gate['backtest'] = evaluate(hard, 'backtest')
            gate['daily_signal'] = copy.deepcopy(seed_products[key]['gates']['daily_signal'])
            if listing:
                refs = []
                for ev in listing['evidence']:
                    path = source_by_sha.get(ev['sha256'])
                    if path is None or digest(path.read_bytes()) != ev['sha256']:
                        raise ValueError('commodity_listing_original:' + key)
                    identity = 'listing-'+ev['sha256']
                    pack.add(identity, 'source-original', path.read_bytes(), ev['url'], received=ev.get('retrieved_at'))
                    refs.append(identity)
                hard['history_ohlc']['evidence_ids'] += refs
            product = dict(key=key, name=names[key], gates=gate,
                supplementary=copy.deepcopy(selected[key]['gates']['supplementary']),
                simulation_assumptions=dict(minimum_open_lots=None,configured_by='consumer-task',is_actual_rule=False,applied_to_engine=False))
            products.append(product)
            statistics.append(dict(key=key, rows=len(history), historical_contracts=len({r['symbol'] for r in history}),
                coverage=proof, invalid_ohlc_excluded=invalid[key], history_artifact=history_id,
                official_earlier_prefix=prefixes.get(key),
                current_contracts=sum(r['product_key']==key for r in current)))
        conflicts = [dict(symbol=k[0], day=k[1], versions=v) for k,v in sorted(rows.conflicts.items()) if k[0].startswith(ex+'.')]
        pack.add('isolated-source-conflicts.json', 'review-evidence', encode(conflicts))
        pack.add('product-coverage.json', 'review-evidence', encode(statistics))
        manifest = dict(schema='user-data-gates-v2', synthetic=False,
            policy=policy(day,target),
            market_scope=[ex], excluded_financial=['CFFEX'], products=products,
            current_contracts=[r for r in current if r['product_key'] in keys],
            limitations=['仅具体合约原件观察，不拼主连、不认证逐日全集/PIT/历史制度',
                '17日保证金用于历史属于明确情景；公司标准不是交易所原件或账户实收',
                '实际最低开仓未知保留null；模拟数量由消费者显式配置',
                'EDB不含结算与成交额，不用close补settle；旧差异保持待核',
                '同值重叠显式去重，异值逐约逐日隔离并保留全部源版本，不静默选择',
                '新上市原件仅证日期，置信度与原站/转载身份见facts，不等于日历获证'])
        summary = pack.write(out/ex, manifest)
        index['shards'].append(dict(exchange=ex, manifest=ex+'/manifest.json', **summary,
            products=statistics, price_rows=sum(r['rows'] for r in statistics),isolated_conflict_days=len(conflicts)))
    index['raw_csv_sources'] = originals
    index['same_value_duplicates_removed'] = rows.duplicates
    index['price_rows'] = sum(s['price_rows'] for s in index['shards'])
    (out/'index.json').write_bytes(encode(index))
    return index


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ('repo', 'signal', 'control', 'company', 'directory', 'out'):
        parser.add_argument('--'+name, type=Path, required=True)
    args = parser.parse_args()
    report = build(args.repo,args.signal,args.control,args.company,args.directory,args.out)
    print(json.dumps(dict(price_rows=report['price_rows'], shards=len(report['shards'])),ensure_ascii=False))
