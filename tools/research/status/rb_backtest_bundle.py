"""将已有RB原件转为当前参数情景回测包；不抓新数据，不无声消解版本冲突。"""
import io
import sys
import argparse
import csv
import datetime as dt
from decimal import Decimal
import hashlib
import json
from pathlib import Path
from urllib.parse import parse_qs,urlparse

from data_gates import item,evaluate


def digest(raw):
    return hashlib.sha256(raw).hexdigest()


def normalize(row):
    values={k:Decimal(row[k].replace(',','')) for k in ('open','high','low','close','volume','close_oi')}
    if not all(v.is_finite() for v in values.values()):raise ValueError('rb_history_nonfinite')
    o,h,l,c=(values[k] for k in ('open','high','low','close'))
    if not 0<l<=min(o,c)<=max(o,c)<=h:return None
    if any(values[k]<0 or values[k]!=values[k].to_integral_value() for k in ('volume','close_oi')):
        raise ValueError('rb_history_invalid_quantity')
    fields={k:format(values[k],'f') for k in ('open','high','low','close','volume')}
    fields.update(open_interest=format(values['close_oi'],'f'),settle=None,turnover=None)
    day=int(dt.datetime.fromtimestamp(int(row['datetime_nano'])//1_000_000_000,dt.timezone.utc).astimezone(dt.timezone(dt.timedelta(hours=8))).strftime('%Y%m%d'))
    return day,fields


def build(repo,config,signal_root,review_path,review_root,out):
    if out.exists():raise ValueError('rb_bundle_output_exists')
    inventory=json.loads((repo/'docs/evidence/project-data-inventory-20261116.json').read_bytes())
    pinned={str(Path(r['path']).resolve()).casefold():r['sha256'] for r in inventory['input_files']}
    seed=json.loads((signal_root/'manifest.json').read_bytes())
    if seed['synthetic'] is not False:raise ValueError('rb_bundle_synthetic_seed')
    payloads={a['id']:(dict(a),(signal_root/a['path']).read_bytes()) for a in seed['artifacts']}
    for a,raw in payloads.values():
        if len(raw)!=a['bytes'] or digest(raw)!=a['sha256']:raise ValueError('rb_bundle_seed_sha')
    history={};bodies=set();invalid=0;same_values=0
    def add(id,role,path,raw,url=None,day=None,received=None):
        if id in payloads:raise ValueError('rb_bundle_artifact_id')
        payloads[id]=(dict(id=id,role=role,path=path,bytes=len(raw),sha256=digest(raw),source_url=url,
                          source_day=day,retrieved_at=received,version='current-parameter-RB-scenario-v1'),raw)
    for entry in config['edb_roots']:
        for body in sorted(Path(entry['path']).rglob('rb*.body')):
            raw=body.read_bytes()
            if not raw.startswith(b'datetime_nano,'):continue
            receipt_raw=body.with_suffix('.json').read_bytes();receipt=json.loads(receipt_raw);ev=receipt['evidence']
            query=parse_qs(urlparse(ev['url']).query)
            symbol=query.get('symbol',[''])[0]
            if not symbol.startswith('SHFE.rb') or query.get('period')!=['86400']:continue
            if receipt['state']!='received':continue
            sha=digest(raw)
            if pinned.get(str(body.resolve()).casefold())!=sha or sha!=ev['sha256']:
                raise ValueError('rb_bundle_unpinned_original')
            if sha in bodies:continue
            bodies.add(sha);id='rb-history-source-'+str(len(bodies))
            add(id,'source-original','history-originals/'+id+'.csv',raw,ev['url'],received=ev.get('retrieved_at'))
            add(id+'-receipt','acquisition-receipt','history-receipts/'+id+'.json',receipt_raw)
            for row in csv.DictReader(io.StringIO(raw.decode('utf-8'))):
                normalized=normalize(row)
                if normalized is None:invalid+=1;continue
                day,fields=normalized
                if not 20210101<=day<=20261113:continue
                record=dict(product_key='SHFE.rb',symbol=symbol,source_native=symbol.split('.')[1],day=day,fields=fields,source_id=id)
                key=(symbol,day)
                if key in history:
                    if history[key]['fields']!=fields:raise ValueError('rb_history_source_version_conflict:'+symbol+':'+str(day))
                    same_values+=1;continue
                history[key]=record
    # 11/16官方快照独立来源；不以重叠日覆盖EDB历史版本。
    daily=payloads['daily-records'][1]
    for line in daily.splitlines():
        record=json.loads(line)
        if record['product_key']=='SHFE.rb' and record['day']==20261116:
            key=(record['symbol'],record['day'])
            if key in history:raise ValueError('rb_history_official_overlap')
            o,h,l,c=(Decimal(record['fields'][k]) for k in ('open','high','low','close'))
            if 0<l<=min(o,c)<=max(o,c)<=h:history[key]=record
            else:invalid+=1
    dates={r['day'] for r in history.values()}
    years={str(y):len({d for d in dates if d//10000==y}) for y in range(2021,2027)}
    if not history or any(v==0 for v in years.values()) or min(dates)>20211231 or max(dates)!=20261116:
        raise ValueError('rb_history_baseline_not_met')
    rows=[history[k] for k in sorted(history,key=lambda k:(k[1],k[0]))]
    raw=''.join(json.dumps(r,ensure_ascii=False,separators=(',',':'))+'\n' for r in rows).encode()
    add('rb-history','daily-records','rb-history.jsonl',raw)
    facts=json.loads((repo/'docs/data-gate-facts.json').read_bytes())
    current=[r for r in facts['current_contracts'] if r['product_key']=='SHFE.rb' and r['effective_min_open_lots'] is not None]
    review_raw=review_path.read_bytes();review=json.loads(review_raw)
    proof=next(r for r in facts['evidence'] if r['id']=='rb-minimum-model-review')
    if digest(review_raw)!=proof['sha256']:raise ValueError('rb_bundle_review_identity')
    add('rb-minimum-model-review','review-evidence','rb-review.json',review_raw,day=20261116)
    for i,source in enumerate(review['Sources']):
        path=(review_root/source['Path']).resolve()
        if not path.is_relative_to(review_root.resolve()):raise ValueError('rb_review_path')
        raw=path.read_bytes()
        if digest(raw)!=source['SHA256']:raise ValueError('rb_review_original_sha')
        id='ContractDailyTradeArgument-20261117.json' if source['Path'].endswith('ContractDailyTradeArgument-20261117.body') else 'rb-rule-source-'+str(i)
        add(id,'source-original',source['Path'],raw,source['URL'])
    rb=next(p for p in seed['products'] if p['key']=='SHFE.rb')
    scope=[r['symbol'] for r in current]
    hard=dict(history_ohlc=item('pass',sorted({r['symbol'] for r in rows}),['rb-history'],'2021起具体合约源版本序列；非完整无缺日认证'),
              current_margin=item('pass',scope,['ContractDailyTradeArgument-20261117.json'],'仅20261117当前交易所投机比例情景'),
              effective_min_open=item('pass',scope,['rb-minimum-model-review'],'仅指定日指定RB合约模型审核，含专项复核；真实客户适用性另核'))
    hard['history_ohlc']['coverage']=dict(first=min(dates),last=max(dates),listing_day=None,artifact_ids=['rb-history'],valid_days_by_year=years)
    rb['gates']['backtest']=evaluate(hard,'backtest',version=1)
    seed['current_contracts']=current
    seed['artifacts']=[a for a,_ in payloads.values()]
    seed['limitations']=[s for s in seed['limitations'] if not s.startswith('本包只支持')]
    seed['limitations'] += ['仅RB历史具体合约独立回测；将已通过当前约约束回用于历史是明确情景，不代表历史真实制度或实际客户约束',
                           'EDB历史版本、零成交OHLC及盘后量仓语义仍待核；本包不修正待核差异、不自动拼主力或多合约组合收益',
                           '历史EDB没有结算或成交额，不用close补settle；回测估值方法和乘数需明确情景假设',
                           '已剔除基本无效OHLC '+str(invalid)+'行；相同值重叠 '+str(same_values)+'行仅取一次；源版本冲突会停止打包']
    out.mkdir(parents=True)
    for a,raw in payloads.values():
        path=out/a['path'];path.parent.mkdir(parents=True,exist_ok=True);path.write_bytes(raw)
    (out/'manifest.json').write_text(json.dumps(seed,ensure_ascii=False,indent=2)+'\n',encoding='utf-8',newline='\n')
    return dict(rows=len(rows),contracts=len({r['symbol'] for r in rows}),valid_days_by_year=years,artifacts=len(payloads),manifest_sha256=digest((out/'manifest.json').read_bytes()))


if __name__=='__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    p=argparse.ArgumentParser(description=__doc__)
    for name in ('repo','config','signal-root','review','review-root','out'):p.add_argument('--'+name,type=Path,required=True)
    a=p.parse_args();print(json.dumps(build(a.repo,json.loads(a.config.read_bytes()),a.signal_root,a.review,a.review_root,a.out),ensure_ascii=False))
