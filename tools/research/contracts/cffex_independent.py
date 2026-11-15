"""独立回读月包逐字段核对解码输出，不调用主解码器，不联网。"""
import argparse
import csv
import datetime
import hashlib
import io
import json
import re
import sys
import zipfile
from pathlib import Path


def audit(source,decoded,month):
    raw=(source/'month.body').read_bytes()
    receipt=json.loads((source/'receipt.json').read_bytes())
    if hashlib.sha256(raw).hexdigest()!=receipt['sha256']:raise ValueError('audit_raw_sha')
    actual={};options=aggregates=0
    # 直接使用CSV列名，不导入主解码器的列常量或映射函数。
    names={'open':'今开盘','high':'最高价','low':'最低价','close':'今收盘','settle':'今结算',
           'pre_settle':'前结算','volume':'成交量','open_interest':'持仓量','turnover':'成交金额'}
    with zipfile.ZipFile(io.BytesIO(raw)) as z:
        if z.testzip() is not None:raise ValueError('audit_crc')
        for member in z.infolist():
            if not re.fullmatch(month+r'[0-9]{2}_1\.csv',member.filename):raise ValueError('audit_month')
            date=member.filename[:8];datetime.datetime.strptime(date,'%Y%m%d')
            text=z.read(member).decode('gb18030')
            for row in csv.DictReader(io.StringIO(text)):
                code=row['合约代码'].strip()
                if any(word in code for word in ('小计','总计','合计')):
                    aggregates+=1;continue
                if re.fullmatch(r'[A-Z]{1,3}[0-9]{4}-[CP]-[0-9]+(?:\.[0-9]+)?',code):
                    options+=1;continue
                if not re.fullmatch(r'[A-Z]{1,3}[0-9]{4}',code):raise ValueError('audit_contract')
                key=(code,int(date))
                if key in actual:raise ValueError('audit_duplicate')
                actual[key]={field:row[column].strip() for field,column in names.items()}
    parsed=json.loads(decoded.read_bytes())
    observed={(r['native'],r['day']):r['fields'] for r in parsed['records']}
    if len(observed)!=len(parsed['records']) or observed!=actual:raise ValueError('audit_fields')
    s=parsed['summary']
    if options!=s['excluded_options'] or aggregates!=s['excluded_aggregate_rows']:raise ValueError('audit_exclusions')
    return dict(month=month,records=len(actual),fields=len(actual)*len(names),
                excluded_options=options,excluded_aggregate_rows=aggregates,
                raw_sha256=receipt['sha256'],decoded_sha256=hashlib.sha256(decoded.read_bytes()).hexdigest())


def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--root',required=True,type=Path)
    p.add_argument('--decoded',required=True,type=Path)
    p.add_argument('--out',required=True,type=Path)
    a=p.parse_args()
    months=json.loads((a.root/'results.json').read_bytes())
    if len(months)!=69 or any(v['state']!='completed' for v in months):raise ValueError('audit_batch_incomplete')
    results=[audit(a.root/v['month'],a.decoded/(v['month']+'.json'),v['month']) for v in months]
    report=dict(schema='cffex-month-independent-fields-v1',state='passed',market_requests=0,
                months=len(results),records=sum(r['records'] for r in results),
                fields=sum(r['fields'] for r in results),items=results,
                limits='仅证明解码与明文原件逐字段相同，不认证发布者、单位或行情真值')
    a.out.write_text(json.dumps(report,ensure_ascii=False,indent=2)+'\n',encoding='utf-8',newline='\n')
    print(json.dumps({k:v for k,v in report.items() if k!='items'},ensure_ascii=False))


if __name__=='__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    main()
