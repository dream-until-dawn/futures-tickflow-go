"""只读核对选择性消费原包；仅输出去敏日期/合约/时限，不认证完整金融链。"""
import argparse
import datetime as dt
from decimal import Decimal
import hashlib
import io
import json
from pathlib import Path
import re
import sys
import tarfile


def digest(raw):return hashlib.sha256(raw).hexdigest()


def instant(value):
    result=dt.datetime.fromisoformat(value.replace('Z','+00:00'))
    if result.tzinfo is None:raise ValueError('consumer_naive_time')
    return result


def rb_packet(raw,receipt,seal):
    packet=json.loads(raw);identity=digest(raw)
    if receipt['PacketSHA']!=identity or receipt['Status']!='sealed' or seal!=identity:
        raise ValueError('consumer_packet_identity')
    created,close,opened=(instant(packet[k]) for k in ('CreatedAt','CloseAt','NextOpenAt'))
    if not close<=created<opened or not created<=instant(receipt['PersistedAt'])<opened:
        raise ValueError('consumer_after_deadline')
    if packet['NextDay']<=packet['Day']:raise ValueError('consumer_trading_days')
    target=packet['Forecast']['NextContract'] or packet['Forecast']['ActualContract']
    if target!='SHFE.rb2701':raise ValueError('consumer_target_contract')
    return dict(market_day=packet['Day'],execution_day=packet['NextDay'],consumed_by_utc=packet['CreatedAt'],
                persisted_at=receipt['PersistedAt'],first_open=packet['NextOpenAt'],contract=target,packet_sha256=identity)


def review(root,archive_sha,manifest_sha):
    archive=(root/'originals.tgz').read_bytes()
    if digest(archive)!=archive_sha:raise ValueError('consumer_archive_sha')
    original=root/'extracted';raw=(original/'export-manifest.json').read_bytes()
    if digest(raw)!=manifest_sha:raise ValueError('consumer_manifest_sha')
    meta=json.loads(raw);data={}
    with tarfile.open(fileobj=io.BytesIO(archive),mode='r:gz') as tar:
        members=tar.getmembers();names=[m.name for m in members]
        if len(names)!=len(set(names)) or set(names)!=set(meta['files'])|{'export-manifest.json'}:raise ValueError('consumer_archive_members')
        for member in members:
            if not member.isfile() or member.name.startswith('/') or '..' in Path(member.name).parts:raise ValueError('consumer_member_path')
            body=tar.extractfile(member).read()
            if member.name=='export-manifest.json':
                if body!=raw:raise ValueError('consumer_manifest_member')
                continue
            expected=meta['files'][member.name]
            if len(body)!=expected['bytes'] or digest(body)!=expected['sha256'] or body!=(original/member.name).read_bytes():raise ValueError('consumer_original_identity')
            data[member.name]=body
    rb=[]
    for name,body in data.items():
        if not re.fullmatch(r'old-rb/journal/attempt-[a-f0-9]{64}\.json',name):continue
        identity=Path(name).stem[8:];packet=json.loads(body)
        if digest(body)!=identity:raise ValueError('consumer_attempt_name')
        rb.append(rb_packet(body,json.loads(data['old-rb/journal/receipt-'+identity+'.json']),json.loads(data['old-rb/journal/seal-'+str(packet['Day'])+'.json'])))
    rb.sort(key=lambda r:r['market_day'])
    if len(rb)!=7:raise ValueError('consumer_expected_seven_packets')
    chain=1
    for a,b in zip(reversed(rb[:-1]),reversed(rb[1:])):
        if a['execution_day']!=b['market_day']:break
        chain+=1
    main=json.loads(data['rm-input/main.json']);last=main['MainDays'][-1];observed=dt.datetime.now(dt.timezone.utc)
    previews=[]
    for name,body in data.items():
        if not re.fullmatch(r'rm-input/.+-preview-[a-f0-9]{64}\.json',name):continue
        value=json.loads(body);packet=value['Packet'];receipt=json.loads(data[name[:-5]+'.receipt.json'])
        if receipt['sha256']!=digest(body) or value['Status']!='complete':raise ValueError('consumer_preview_receipt')
        if packet['Day']!=last['Day'] or packet['CreatedAt']!='0001-01-01T00:00:00Z':raise ValueError('consumer_preview_day_or_clock')
        target=packet['Forecast']['NextContract'] or packet['Forecast']['ActualContract']
        quote=next(q['Bar'] for q in last['Quotes'] if q['Contract']==target)
        o,h,l,c=(Decimal(str(quote[k])) for k in ('Open','High','Low','Close'))
        if target!='CZCE.RM2701' or quote['Day']!=packet['Day'] or not 0<l<=min(o,c)<=max(o,c)<=h:raise ValueError('consumer_preview_daily_input')
        if observed>=instant(packet['NextOpenAt']):raise ValueError('consumer_observation_after_open')
        previews.append(dict(market_day=packet['Day'],execution_day=packet['NextDay'],first_open=packet['NextOpenAt'],contract=target,preview_sha256=digest(body)))
    if len(previews)!=2:raise ValueError('consumer_expected_two_previews')
    return dict(schema='consumer-premarket-independent-readback-v1',archive_sha256=archive_sha,manifest_sha256=manifest_sha,
        original_members_verified=len(data),observed_at_utc=observed.isoformat(),rb_packets=rb,rm_previews=previews,
        products={'SHFE.rb':dict(contract='SHFE.rb2701',successful_market_dates=len(rb),latest_next_day_chain=chain,layer='sealed_consumption',independently_verified=True),
                  'CZCE.RM':dict(contract='CZCE.RM2701',successful_market_dates=1,latest_next_day_chain=None,layer='unapproved_model_preview',independently_verified=True)},
        limits='选择性原件的身份/日期/合约/时限及RM目标OHLC结构复核；不是完整可运行输入。RB旧7包未含全部原日表，不复算其全部行情；HTTP首次取得未知。RB含失败后正常重试，不代表首次调度连胜。RM零CreatedAt不作时钟，两预览仅算一个品种/日；独立观察UTC仅本机墙钟，非独立NTP授时。无账户/订单/金融准入认定。',financial_admission_added=False)


if __name__=='__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    p=argparse.ArgumentParser(description=__doc__);p.add_argument('--root',type=Path,required=True);p.add_argument('--archive-sha',required=True);p.add_argument('--manifest-sha',required=True);p.add_argument('--out',type=Path,required=True);a=p.parse_args()
    result=review(a.root,a.archive_sha,a.manifest_sha)
    with a.out.open('x',encoding='utf8',newline='\n') as f:f.write(json.dumps(result,ensure_ascii=False,indent=2)+'\n')
    print(json.dumps(result['products'],ensure_ascii=False))
