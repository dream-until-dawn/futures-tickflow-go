"""独立回读两类官方日表，与Go Records逐个九字段比较；不调用Go解析器。"""
import csv
from decimal import Decimal
import io
import json
import re
import sys


def inspect(raw, kind, day):
    names={'open':'OPENPRICE','high':'HIGHESTPRICE','low':'LOWESTPRICE','close':'CLOSEPRICE','settle':'SETTLEMENTPRICE','pre_settle':'PRESETTLEMENTPRICE','volume':'VOLUME','open_interest':'OPENINTEREST','turnover':'TURNOVER'}
    result={}
    if kind=='shfe-json':
        data=json.loads(raw,parse_float=Decimal)
        if str(data['report_date'])!=str(day) or data['o_code']!='0000':raise ValueError('increment_day_or_business')
        for row in data['o_curinstrument']:
            code=str(row['PRODUCTID']).strip();month=str(row['DELIVERYMONTH']).strip()
            if not re.fullmatch('[a-z]{1,3}_f',code) or not re.fullmatch('[0-9]{4}',month):continue
            product=code[:-2];exchange='INE' if product in {'sc','nr','bc','lu','ec'} else 'SHFE'
            fields={field:'' if row.get(column) is None else str(row[column]).strip() for field,column in names.items()}
            key=(exchange,product+month,day)
            if key in result:raise ValueError('increment_duplicate')
            result[key]=fields
    else:
        text=raw.decode('utf-8').strip();lines=text.splitlines()
        if lines[0].strip()!='郑州商品交易所期货每日行情表('+str(day)[:4]+'-'+str(day)[4:6]+'-'+str(day)[6:]+')':raise ValueError('increment_day_or_business')
        mapping={'open':'今开盘','high':'最高价','low':'最低价','close':'今收盘','settle':'今结算','pre_settle':'昨结算','volume':'成交量(手)','open_interest':'持仓量','turnover':'成交额(万元)'}
        reader=csv.DictReader(io.StringIO('\n'.join(lines[1:])),delimiter='|')
        for raw_row in reader:
            row={key.strip():value.strip() for key,value in raw_row.items() if key is not None and value is not None}
            native=row.get('合约代码',row.get('品种月份',''))
            if not re.fullmatch('[A-Z]{1,3}[0-9]{3,4}',native):continue
            key=('CZCE',native,day)
            if key in result:raise ValueError('increment_duplicate')
            result[key]={field:row[column] for field,column in mapping.items()}
    return result


def compare(raw, kind, day, decoded):
    expected=inspect(raw,kind,day)
    observed={(r['exchange'],r['native'],r['day']):r['fields'] for r in decoded}
    if len(observed)!=len(decoded) or observed!=expected:raise ValueError('increment_fields_or_set')
    return dict(rows=len(observed),fields=len(observed)*9,days=[day],comparison='原表字面字段与解码一致；不认证价格真值、挂牌全集或历史PIT')


if __name__=='__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    raise SystemExit('由离线复核调用，不联网')
