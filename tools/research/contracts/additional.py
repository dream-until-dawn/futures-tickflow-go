"""只读解码已取得的郑商所日表/中金所明文月包，来源置信度不可提升。"""
import argparse
import csv
import datetime as dt
import hashlib
import io
import json
import re
import sys
from pathlib import Path

sys.path.insert(0,str(Path(__file__).resolve().parents[3]))
from tools.archive.decode_annual import safe_archive

FIELD_COLUMNS = dict(open='今开盘', high='最高价', low='最低价', close='今收盘', settle='今结算',
                     pre_settle='昨结算', volume='成交量(手)', open_interest='持仓量', turnover='成交额(万元)')


def mapped(rows, known, day, member='', member_sha=''):
    result,unresolved=[],[]
    for index,row in rows:
        native=row['native']
        if native not in known or len(known[native])!=1:
            unresolved.append(dict(native=native,day=day,member=member,row=index,reason='missing_or_ambiguous_snapshot_identity'))
            continue
        symbol,product=known[native][0]
        exchange,canonical=symbol.split('.')
        result.append(dict(exchange=exchange,native=canonical,source_native=native,day=day,fields=row['fields'],
                           row=index,member=member,member_sha256=member_sha,product=product))
    return result,unresolved


def decode_czce(raw,known,stats=None):
    lines=[line.strip() for line in raw.decode('utf-8').splitlines() if line.strip()]
    if not lines or '郑州商品交易所期货每日行情表(2021-01-04)' not in lines[0]:
        raise ValueError('czce_date_heading')
    header=[v.strip() for v in lines[1].split('|')]
    if header[0] not in ['品种月份','合约代码'] or any(c not in header for c in FIELD_COLUMNS.values()):
        raise ValueError('czce_columns')
    rows=[]
    for index,line in enumerate(lines[2:],3):
        values=[v.strip() for v in line.split('|')]
        native=values[0]
        if any(x in native for x in ['小计','总计','合计']):continue
        if len(values)!=len(header) or not re.fullmatch(r'[A-Z]{1,3}[0-9]{3,4}',native):raise ValueError('czce_row_shape')
        rows.append((index,dict(native=native,fields={k:values[header.index(c)] for k,c in FIELD_COLUMNS.items()})))
    return mapped(rows,known,20210104)


def decode_cffex(raw,known,stats=None):
    stats=stats if stats is not None else {}
    stats['excluded_options']=0
    stats['excluded_aggregate_rows']=0
    records,unresolved,seen=[],[],set()
    with safe_archive(raw,64<<20) as z:
        for name in sorted(z.namelist()):
            m=re.fullmatch(r'(202101[0-9]{2})_1\.csv',name)
            if not m:raise ValueError('cffex_unknown_member')
            day=int(m[1]);data=z.read(name);digest=hashlib.sha256(data).hexdigest()
            dt.datetime.strptime(str(day),'%Y%m%d')
            reader=csv.DictReader(io.StringIO(data.decode('gb18030')))
            columns={**FIELD_COLUMNS,'pre_settle':'前结算','volume':'成交量','turnover':'成交金额'}
            if reader.fieldnames is None or any(c not in reader.fieldnames for c in columns.values()):raise ValueError('cffex_columns')
            rows=[]
            for index,row in enumerate(reader,2):
                native=row['合约代码'].strip()
                if any(x in native for x in ['小计','总计','合计']):
                    stats['excluded_aggregate_rows']+=1
                    continue
                if re.fullmatch(r'[A-Z]{1,3}[0-9]{4}-[CP]-[0-9]+(?:\.[0-9]+)?',native):
                    stats['excluded_options']+=1
                    continue
                if not re.fullmatch(r'[A-Z]{1,3}[0-9]{4}',native) or None in row:raise ValueError('cffex_row_shape')
                if (native,day) in seen:raise ValueError('cffex_duplicate_day')
                seen.add((native,day));rows.append((index,dict(native=native,fields={k:row[c].strip() for k,c in columns.items()})))
            accepted,rejected=mapped(rows,known,day,name,digest);records.extend(accepted);unresolved.extend(rejected)
    return records,unresolved


def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--inventory-root',required=True,type=Path)
    p.add_argument('--out-root',required=True,type=Path)
    args=p.parse_args();args.out_root.mkdir()
    inventory_file=args.inventory_root/'inventory.json';raw=inventory_file.read_bytes()
    if hashlib.sha256(raw).hexdigest()!='c7ff4e26edda99e694149e16e4790050f4e847bebb4ad940b704340c64632545':raise ValueError('snapshot_sha')
    known={}
    for c in json.loads(raw)['contracts']:
        if float(c['expiry_seconds'])<1609430400:continue
        keys={c['canonical'].split('.')[1]}
        if c['exchange']=='CZCE':keys.add(c['product']+str(c['delivery_year']%10)+str(c['delivery_month']).zfill(2))
        for key in keys:known.setdefault(key,[]).append((c['canonical'],c['product']))
    cases=[('czce',Path('C:/Temp/tickflow-official-https-probe-20261115-v1/archive/czce-20210104-https-official.body'),Path('C:/Temp/tickflow-official-https-probe-20261115-v1/archive/czce-20210104-https-official.json'),'d9c133c18eba8f0ac00b3872f8f661c2628b917c578533a8b57c904f99586b12',decode_czce),
           ('cffex',Path('C:/Temp/tickflow-cffex-native-202101-20261115-v1/month.body'),Path('C:/Temp/tickflow-cffex-native-202101-20261115-v1/receipt.json'),'0a6263a64d32b0bd804288f896adfc25bfbb0ff205c7f65e101ccb0a1df91cad',decode_cffex)]
    summaries=[]
    for market,source,receipt_path,expected,decode in cases:
        raw=source.read_bytes();receipt=json.loads(receipt_path.read_bytes())
        if hashlib.sha256(raw).hexdigest()!=expected or receipt['state']!='received' or receipt['evidence']['sha256']!=expected:raise ValueError('original_sha_or_receipt')
        if market=='czce' and (receipt['evidence']['url']!='https://www.czce.com.cn/cn/DFSStaticFiles/Future/2021/20210104/FutureDataDaily.txt' or receipt['body_state']!='complete' or receipt['http']['status']!=200 or not receipt['http']['complete']):raise ValueError('czce_transport_identity')
        if market=='cffex' and (receipt['identity']!='native-http/cleartext/unauthenticated/unqualified' or receipt['publisher_authentication'] is not False or receipt['status']!=200 or not receipt['body_complete'] or receipt['url']!='http://www.cffex.com.cn/sj/historysj/202101/zip/202101.zip'):raise ValueError('cffex_native_identity')
        stats={}
        records,unresolved=decode(raw,known,stats)
        data=dict(schema='official-observed-daily-control-v1',year=2021,product_filter='',control_scope='exact-dates',
                  records=records,original_sha256=expected,source_receipt_sha256=hashlib.sha256(receipt_path.read_bytes()).hexdigest(),
                  control_identity='official HTTPS endpoint, TLS transport' if market=='czce' else 'native-http/cleartext/unauthenticated/unqualified',
                  turnover_unit='wanyuan explicit header' if market=='czce' else 'unknown in CSV header',
                  volume_unit='lots explicit header' if market=='czce' else 'not stated in CSV header',financial_eligible=False)
        target=args.out_root/(market+'-decoded.json');target.write_text(json.dumps(data,ensure_ascii=False,indent=2)+'\n',encoding='utf-8',newline='\n')
        summaries.append(dict(market=market,rows=len(records),contracts=len({r['native'] for r in records}),days=len({r['day'] for r in records}),
                              unresolved=unresolved,decoded_file=str(target),decoded_sha256=hashlib.sha256(target.read_bytes()).hexdigest(),
                              original_sha256=expected,control_identity=data['control_identity'],turnover_unit=data['turnover_unit'],volume_unit=data['volume_unit'],
                              original_retrieved_at=receipt['evidence']['retrieved_at'],financial_eligible=False))
        summaries[-1]['excluded_class_counts']=stats
    (args.out_root/'review.json').write_text(json.dumps(dict(schema='additional-observed-markets-v1',results=summaries,market_requests=0),ensure_ascii=False,indent=2)+'\n',encoding='utf-8',newline='\n')
    print(json.dumps(summaries,ensure_ascii=False))


if __name__=='__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    main()
