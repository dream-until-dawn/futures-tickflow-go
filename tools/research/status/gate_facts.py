"""从冻结原件抽取两门槛适用合约；不联网、不导出账户或修改源文件。"""
import io
import sys
import argparse
import hashlib
import json
from pathlib import Path
import re

from snapshot_inventory import verified_members
from data_gates import current_margin, effective_minimum


def build(config, increment_root, review_path, package_root):
    spec = config['engine_snapshot']
    archive = Path(spec['archive']).read_bytes()
    manifest_raw = Path(spec['manifest']).read_bytes()
    manifest, members = verified_members(archive, manifest_raw, spec['archive_sha256'], spec['manifest_sha256'])
    evidence = []
    parameters = {}
    for source in manifest['parameter_records']:
        name, = [n for n in members if n.endswith('/parameters/' + source['file'])]
        raw = members[name]
        if hashlib.sha256(raw).hexdigest() != source['sha256'] or len(raw) != source['bytes']:
            raise ValueError('gate_parameter_identity')
        parameters[(source['kind'], int(source['day']))] = json.loads(raw)
        evidence.append(dict(id=source['file'], sha256=source['sha256'], bytes=source['bytes'],
                             source_url=source['url'], source_day=int(source['day']), retrieved_at=None))
    review_raw = review_path.read_bytes()
    review = json.loads(review_raw)
    if (review['Approved'] is not True or review['ExecutionDay'] != 20261117 or
            review['DecisionDay'] != 20261116 or review['Product'] != 'SHFE.rb'):
        raise ValueError('gate_minimum_review_scope')
    # 独立核所有引用原件的字节身份；派生审核仍明确标为指定日的模型审核。
    for source in review['Sources']:
        path = (package_root / source['Path']).resolve()
        if not path.is_relative_to(package_root.resolve()):
            raise ValueError('gate_review_source_path')
        raw = path.read_bytes()
        if hashlib.sha256(raw).hexdigest() != source['SHA256']:
            raise ValueError('gate_review_original_sha')
    evidence.append(dict(id='rb-minimum-model-review', sha256=hashlib.sha256(review_raw).hexdigest(),
                         bytes=len(review_raw), source_url=None, source_day=20261116,
                         retrieved_at=None, reviewed_at=review['ReviewedAt'],
                         kind='model-review-specific-contracts-and-day-not-general-official-rule'))
    mapping = {k.split('.')[1]:k.split('.')[0] for k in config['known_products'] if k.split('.')[0] in {'SHFE','INE'}}
    current = []
    for row in parameters[('ContractDailyTradeArgument',20261117)]['ContractDailyTradeArgument']:
        native = row['INSTRUMENTID'].strip()
        code = re.fullmatch(r'([a-z]+)\d{4}', native)
        if not code or code[1] not in mapping:
            raise ValueError('gate_parameter_product')
        symbol = mapping[code[1]] + '.' + native
        minimum = review['MinimumOpening'].get(symbol)
        contract = dict(product_key=mapping[code[1]]+'.'+code[1], symbol=symbol,
                        source_native=native, source_snapshot_day=20261117, as_of_trade_day=20261117,
                        margin=dict(long=row['SPEC_LONGMARGINRATIO'],short=row['SPEC_SHORTMARGINRATIO'],
                                    unit='ratio',basis='exchange_spec',evidence_ids=['ContractDailyTradeArgument-20261117.json']),
                        effective_min_open_lots=minimum, ordinary_min_order_lots=None,
                        minimum_lots_kind='effective_min_open',special_adjustments_reviewed=minimum is not None,
                        minimum_evidence_ids=['rb-minimum-model-review'] if minimum is not None else [])
        if int(row['TRADINGDAY']) != 20261117 or not current_margin(contract,20261117):
            raise ValueError('gate_parameter_value_or_day')
        current.append(contract)
    if set(review['MinimumOpening']) - {r['symbol'] for r in current}:
        raise ValueError('gate_minimum_not_in_current_table')
    meta = json.loads((increment_root/'manifest.json').read_bytes())
    raw = (increment_root/'records.jsonl').read_bytes()
    if hashlib.sha256(raw).hexdigest() != meta['records_sha256']:
        raise ValueError('gate_settlement_records_sha')
    settlement = {}
    from inventory_model import product, numeric
    from reference_inventory import compare
    bindings = {}
    reference_last = {}
    for entry in config.get('reference_snapshots', []):
        reference = json.loads(Path(entry['review']).read_bytes())
        if reference['observed_day'] == 20261116:
            body = (Path(entry['root'])/'reference.body').read_bytes()
            actual = compare(body, reference)
            bindings = {native:canonical for canonical,(native,_,_) in actual.items()}
            reference_last = {native:last for _,(native,_,last) in actual.items()}
            evidence.append(dict(id='czce-reference-20261116',sha256=reference['body_sha256'],
                                 bytes=len(body),source_day=20261116,retrieved_at=reference['requested_at']))
    count = 0
    current_symbols = {row['symbol'] for row in current}
    excluded_expired_or_unconfirmed = []
    for line in raw.splitlines():
        row = json.loads(line)['record'];count += 1
        if row['day'] != 20261116:
            raise ValueError('gate_settlement_day')
        if (numeric(row['fields'].get('settle')) or 0) > 0:
            key = product(row['exchange'],row['native'])
            symbol = bindings.get(row['native']) if row['exchange']=='CZCE' else row['exchange']+'.'+row['native']
            if not symbol:raise ValueError('gate_settlement_canonical_unresolved')
            if ((row['exchange']=='CZCE' and reference_last.get(row['native'],0)<20261117) or
                    (row['exchange'] in {'SHFE','INE'} and symbol not in current_symbols)):
                excluded_expired_or_unconfirmed.append(symbol)
                continue
            settlement.setdefault(key,[]).append(symbol)
    if count != meta['rows']:
        raise ValueError('gate_settlement_count')
    # 只绑定已核的上市年度档案，不把普通年度档案首日误当上市日。
    repo=Path(__file__).resolve().parents[3]
    locks=json.loads((repo/'tools/research/status/sources.json').read_bytes())
    listing_days={}
    for filename,keys in [('listed-2023.json',{'INE.ec','SHFE.ao','SHFE.br'}),
                          ('shfe-2025.json',{'SHFE.ad','SHFE.op'})]:
        rel='calendar/verified/profiles/'+filename
        lock=next(r for r in locks['profiles'] if r['file']==rel)
        raw=(repo/rel).read_bytes()
        if hashlib.sha256(raw).hexdigest()!=lock['sha256']:raise ValueError('gate_listing_profile_sha')
        profile=json.loads(raw)
        for series in profile['series']:
            key=series['product']['Exchange']+'.'+series['product']['Product']
            if key not in keys:continue
            first=series['records'][0]
            listing=[r for r in first['evidence'] if 'listing' in r['id']]
            if not listing:raise ValueError('gate_listing_direct_evidence')
            listing_days[key]=dict(day=first['day'],profile=rel,profile_sha256=lock['sha256'],evidence=listing)
    return dict(schema='user-data-gate-facts-v1', current_trade_day=20261117,latest_completed_day=20261116,
                evidence=evidence,current_contracts=current,settlement_scopes=settlement,
                listing_days=listing_days,
                excluded_expired_or_unconfirmed=sorted(excluded_expired_or_unconfirmed),
                daily_update_exchanges=['SHFE','INE','CZCE'],
                daily_update_implementation='cmd/archive-batch + archive/ingest: frozen one-day plan, validate original before records output',
                source_limits=['最低开仓仅为指定日指定RB合约的模型审核；非真实客户普遍资格证明',
                               '其他品种专项最低开仓未知，普通1手不得代填',
                               'CZCE结算scope依据同日XML显式年月绑定；不猜具体合约年份',
                               '日更能力不等于已部署全市场自动服务或未来每次成功',
                               '不是可迁移完整行情交付包；原件字节留在冻结目录，派生审核与原件身份分开'])


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--config',type=Path,required=True)
    p.add_argument('--increment-root',type=Path,required=True)
    p.add_argument('--review',type=Path,required=True)
    p.add_argument('--package-root',type=Path,required=True)
    p.add_argument('--out',type=Path,required=True)
    a=p.parse_args()
    if a.out.exists():raise ValueError('gate_facts_output_exists')
    result=build(json.loads(a.config.read_bytes()),a.increment_root,a.review,a.package_root)
    a.out.write_text(json.dumps(result,ensure_ascii=False,indent=2)+'\n',encoding='utf-8',newline='\n')
    print(json.dumps(dict(current_contracts=len(result['current_contracts']),margin_products=len({r['product_key'] for r in result['current_contracts']}),
                         minimum_contracts=sum(effective_minimum(r,20261117) for r in result['current_contracts']),
                         settlement_products=len(result['settlement_scopes'])),ensure_ascii=False))
