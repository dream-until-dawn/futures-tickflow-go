"""已授权单次月包的纯离线复核；不联网，不提升明文来源置信度。"""
import argparse
import hashlib
import json
from pathlib import Path

from additional import decode_cffex

PLAN_SHA='849ed0d68e21aa34fd36fc8f266ca42e72bb54e5b72956fe0d8762813da4053b'
INVENTORY_SHA='c7ff4e26edda99e694149e16e4790050f4e847bebb4ad940b704340c64632545'
IDENTITY='native-http/cleartext/unauthenticated/unqualified'


def sha(raw):
    return hashlib.sha256(raw).hexdigest()


def verify_month(root,job,known):
    """必须同时满足收据、URL、正文与成员约束，不能只信批状态。"""
    receipt_raw=(root/'receipt.json').read_bytes()
    r=json.loads(receipt_raw)
    if (r.get('state')!='received' or r.get('status')!=200 or
        r.get('body_complete') is not True or r.get('url')!=job['url'] or
        r.get('identity')!=IDENTITY or r.get('publisher_authentication') is not False or
        r.get('financial_eligible') is not False):raise ValueError('cffex_receipt_identity')
    raw=(root/'month.body').read_bytes()
    digest=sha(raw)
    if len(raw)>4<<20 or len(raw)!=r.get('bytes') or digest!=r.get('sha256') or digest!=r.get('evidence',{}).get('sha256'):
        raise ValueError('cffex_body_sha')
    stats={}
    records,unknown=decode_cffex(raw,known,stats,month=job['month'])
    summary=dict(month=job['month'],url=job['url'],bytes=len(raw),body_sha256=digest,
                 receipt_sha256=sha(receipt_raw),records=len(records),
                 contracts=len({v['native'] for v in records}),days=sorted({v['day'] for v in records}),
                 unresolved_count=len(unknown),**stats)
    return records,unknown,summary


def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--root',type=Path,required=True)
    p.add_argument('--plan',type=Path,required=True)
    p.add_argument('--inventory',type=Path,required=True)
    p.add_argument('--out',type=Path,required=True)
    args=p.parse_args()
    # 大批原价只允许输出仓外，公开报告另取聚合字段。
    repo=Path(__file__).resolve().parents[3]
    if args.out.resolve().is_relative_to(repo):raise ValueError('private_output_required')
    plan_raw=args.plan.read_bytes();inventory_raw=args.inventory.read_bytes()
    if sha(plan_raw)!=PLAN_SHA or sha(inventory_raw)!=INVENTORY_SHA:raise ValueError('locked_input_sha')
    jobs=json.loads(plan_raw)['jobs'];states=json.loads((args.root/'results.json').read_bytes())
    if len(states)!=len(jobs) or any(s['month']!=j['month'] or s['state']!='completed' for s,j in zip(states,jobs)):
        raise ValueError('batch_incomplete_no_success')
    known={}
    for c in json.loads(inventory_raw)['contracts']:
        if c['exchange']=='CFFEX':
            known.setdefault(c['canonical'].split('.')[1],[]).append((c['canonical'],c['product']))
    args.out.mkdir()
    summaries=[];identities=set();keys=set();total=0
    for job in jobs:
        rows,unknown,summary=verify_month(args.root/job['month'],job,known)
        for row in rows:
            key=(row['native'],row['day'])
            if key in keys:raise ValueError('cffex_cross_month_duplicate')
            keys.add(key);identities.add(row['native'])
        data=dict(schema='cffex-month-observation-v1',identity=IDENTITY,financial_eligible=False,
                  publisher_authentication=False,volume_unit='unknown in CSV header',
                  turnover_unit='unknown in CSV header',records=rows,unresolved=unknown,summary=summary)
        encoded=(json.dumps(data,ensure_ascii=False,indent=2)+'\n').encode()
        (args.out/(job['month']+'.json')).write_bytes(encoded)
        summary['decoded_sha256']=sha(encoded);summaries.append(summary);total+=len(rows)
    report=dict(schema='cffex-69-month-offline-review-v1',state='completed',plan_sha256=PLAN_SHA,
                inventory_sha256=INVENTORY_SHA,identity=IDENTITY,publisher_authentication=False,
                financial_eligible=False,months=len(summaries),records=total,contracts=len(identities),
                contract_keys=sorted(identities),unresolved_count=sum(s['unresolved_count'] for s in summaries),
                bytes=sum(s['bytes'] for s in summaries),excluded_options=sum(s['excluded_options'] for s in summaries),
                excluded_aggregate_rows=sum(s['excluded_aggregate_rows'] for s in summaries),items=summaries,
                limits=['明文不能认证发布者','量额单位未在CSV表头声明','观察日期不能证明挂牌生命周期或PIT','无官方日历时不声称缺日已核'])
    (args.out/'review.json').write_text(json.dumps(report,ensure_ascii=False,indent=2)+'\n',encoding='utf-8',newline='\n')
    print(json.dumps({k:v for k,v in report.items() if k not in ('items','contract_keys')},ensure_ascii=False))


if __name__=='__main__':main()
