"""首批真实逐日信号包：复用已成功原件，不联网，不把盘后包冒充长期历史。"""
import io
import sys
import argparse
from decimal import Decimal, InvalidOperation
import hashlib
import json
from pathlib import Path

from data_gates import evaluate, item
from reference_inventory import compare


def sha(raw):
    return hashlib.sha256(raw).hexdigest()


def fields(values):
    result = {}
    for key in ('open','high','low','close','settle','volume','open_interest','turnover'):
        value = values.get(key)
        if value is None or value == '':
            result[key] = None
            continue
        try:
            number = Decimal(str(value).replace(',',''))
            if not number.is_finite():raise ValueError('signal_nonfinite_numeric')
        except InvalidOperation as error:
            raise ValueError('signal_invalid_numeric') from error
        result[key] = format(number,'f')
    if result['settle'] is None or Decimal(result['settle']) <= 0:
        raise ValueError('signal_invalid_settlement')
    return result


def build(repo, model, increment, reference, reference_review, out):
    if out.exists():raise ValueError('signal_bundle_output_exists')
    facts = model['user_data_gate_facts']
    if facts['latest_completed_day'] != 20261116 or facts['current_trade_day'] != 20261117:
        raise ValueError('signal_bundle_policy_day')
    report = json.loads((increment/'manifest.json').read_bytes())
    raw = (increment/'records.jsonl').read_bytes()
    if sha(raw) != report['records_sha256']:raise ValueError('signal_input_records_sha')
    refraw = (reference/'reference.body').read_bytes()
    refreview = json.loads(reference_review.read_bytes())
    binding = {v[0]:k for k,v in compare(refraw,refreview).items()}
    payloads = []
    originals = {}
    def add(id, role, path, body, url=None, source_day=None, received=None):
        payloads.append((dict(id=id,role=role,path=path,bytes=len(body),sha256=sha(body),source_url=url,
                              source_day=source_day,retrieved_at=received,version='real-signal-snapshot-v1'),body))
    for exchange in ('shfe','czce'):
        prefix=exchange+'-daily-20261116'
        receipt_raw=(increment/exchange/'originals'/ (prefix+'.json')).read_bytes()
        receipt=json.loads(receipt_raw)
        body=(increment/exchange/'originals'/ (prefix+'.body')).read_bytes()
        ev=receipt['evidence']
        if (receipt['state']!='received' or ev['sha256']!=sha(body) or receipt['bytes']!=len(body)
                or ev['id']!=prefix):raise ValueError('signal_original_identity')
        originals[prefix]=ev
        add(prefix,'source-original','originals/'+prefix+'.body',body,ev['url'],20261116,ev['retrieved_at'])
        add(prefix+'-receipt','acquisition-receipt','receipts/'+prefix+'.json',receipt_raw)
        add(prefix+'-plan','daily-update-plan','plans/'+prefix+'.json',(increment/exchange/'frozen-plan.json').read_bytes())
    add('czce-reference','source-original','originals/czce-reference.xml',refraw,
        'https://www.czce.com.cn/cn/DFSStaticFiles/Future/2026/20261116/FutureDataReferenceData.xml',20261116,refreview['requested_at'])
    add('daily-entrypoint','daily-update-code','implementation/main.go',(repo/'cmd/archive-batch/main.go').read_bytes())
    add('daily-validation','daily-update-code','implementation/ingest.go',(repo/'archive/ingest/ingest.go').read_bytes())
    normalized=[];seen=set()
    for line in raw.splitlines():
        row=json.loads(line)['record'];ev=row['evidence']
        if ev['id'] not in originals or ev['sha256']!=originals[ev['id']]['sha256'] or row['day']!=20261116:
            raise ValueError('signal_record_original_binding')
        symbol=binding.get(row['native']) if row['exchange']=='CZCE' else row['exchange']+'.'+row['native']
        if not symbol:raise ValueError('signal_unresolved_year')
        if symbol in seen:raise ValueError('signal_duplicate_contract_day')
        seen.add(symbol)
        from inventory_model import product
        normalized.append(dict(product_key=product(row['exchange'],row['native']),symbol=symbol,
                               source_native=row['native'],day=row['day'],fields=fields(row['fields']),source_id=ev['id']))
    if len(normalized)!=report['rows']:raise ValueError('signal_input_count')
    quote=(''.join(json.dumps(r,ensure_ascii=False,separators=(',',':'))+'\n' for r in normalized)).encode()
    add('daily-records','daily-records','daily.jsonl',quote)
    products=[]
    for p in model['products']:
        scope=facts['settlement_scopes'].get(p['key'],[])
        matching={r['symbol'] for r in normalized if r['product_key']==p['key']}
        if not set(scope)<=matching:raise ValueError('signal_eligible_not_in_records')
        src='czce-daily-20261116' if p['exchange']=='CZCE' else 'shfe-daily-20261116'
        status='pass' if scope else 'pending'
        signal=dict(settlement=item(status,scope,['daily-records',src] if scope else [],
                                   '目标已收盘日真实结算；按当前合约范围筛选' if scope else '本包未取得本品种目标日有效结算'),
                    daily_update=item(status,scope,[src+'-plan',src+'-receipt','daily-entrypoint','daily-validation'] if scope else [],
                                      '既有按日HTTPS抓取及原件校验已实取；非自动部署承诺' if scope else '本包未核得本品种日更结算入口'))
        back={k:item(reason='本首批包只交付目标日日线；长期历史和当前约束未随包交付')
              for k in ('history_ohlc','current_margin','effective_min_open')}
        back['history_ohlc']['coverage']=dict(first=None,last=None,listing_day=None,artifact_ids=[],valid_days_by_year={})
        products.append(dict(key=p['key'],name=p['name'],gates=dict(backtest=evaluate(back,'backtest',version=1),daily_signal=evaluate(signal,'daily_signal',version=1)),
                             bonus=dict(calendar='not_in_this_bundle',history_rules='not_in_this_bundle',lifecycle='current_snapshot_only',
                                        pit='not_certified',reconciliation='original_identity_and_CZCE_year_binding' if scope else 'not_assessed',runtime_observations='single_snapshot' if scope else 'not_assessed')))
    manifest=dict(schema='user-data-gates-v1',synthetic=False,policy=dict(baseline_year=2021,latest_completed_day=20261116,
                  current_trade_day=20261117,minimum_lots_kind='effective_min_open'),products=products,
                  current_contracts=[],artifacts=[a for a,_ in payloads],limitations=[
                  '本包只支持指定日期和所列合约信号，回测未通过；总表已有长历史不等于本包已交付长历史',
                  '原件含当日到期旧合约，eligible_contracts已排除到期或当前身份未确认者；新合约无昨结算时不补造',
                  '按日接口已实取，未部署全市场自动任务；未来失败不得复用旧日冒充',
                  'SHFE/INE成交额单位仍未知；来源差异及零成交OHLC语义保留，收盘不代结算'])
    out.mkdir(parents=True)
    for artifact,body in payloads:
        path=out/artifact['path'];path.parent.mkdir(parents=True,exist_ok=True);path.write_bytes(body)
    (out/'manifest.json').write_text(json.dumps(manifest,ensure_ascii=False,indent=2)+'\n',encoding='utf-8',newline='\n')
    return manifest


if __name__=='__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    p=argparse.ArgumentParser(description=__doc__)
    for name in ('repo','model','increment','reference','reference-review','out'):p.add_argument('--'+name,type=Path,required=True)
    a=p.parse_args();r=build(a.repo,json.loads(a.model.read_bytes()),a.increment,a.reference,a.reference_review,a.out)
    print(json.dumps(dict(signal_products=sum(p['gates']['daily_signal']['status']=='pass' for p in r['products']),
                         backtest_products=0,artifacts=len(r['artifacts']),manifest_sha256=sha((a.out/'manifest.json').read_bytes())),ensure_ascii=False))
