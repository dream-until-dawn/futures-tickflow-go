"""提取源站明确的当日逐合约最小开仓字段；不读普通最小下单、评论或SDK默认值。"""
import io
import sys
import hashlib
import re
from broker_margin import Table
from current_constraints import visible


def parse(raw,receipt,key,day,bindings):
    if (receipt['state']!='received_scope_unreviewed' or receipt['http']['status']!=200 or
            not receipt['http']['complete'] or hashlib.sha256(raw).hexdigest()!=receipt['sha256']):
        raise ValueError('opening_quote_transport_identity')
    text=visible(raw)
    dates=set(re.findall(r'依据交易所公告每日自动整理[，,]更新至(\d{4}-\d{2}-\d{2})',text))
    expected=str(day)[:4]+'-'+str(day)[4:6]+'-'+str(day)[6:]
    if dates!={expected}:raise ValueError('opening_quote_reported_update_day')
    p=Table();p.feed(raw.decode('utf-8'));headers=0;result={};unbound=[];missing=[]
    code=key.split('.')[1]
    for cells in p.rows:
        row=[value for _,value in cells]
        if row and row[0]=='合约品种':
            if row[:5]!=['合约品种','现价','交易单位','涨/跌停板','最小开仓(手)']:
                raise ValueError('opening_quote_actual_open_header')
            headers+=1;continue
        if not headers or len(row)!=15:continue
        match=re.search(r'[（(]([A-Za-z]+\d{3,4})[）)]',row[0])
        if not match:continue
        native=match[1]
        if not re.fullmatch(re.escape(code)+r'\d{3,4}',native,re.I):
            raise ValueError('opening_quote_wrong_product')
        symbol=bindings.get(native.upper())
        if not symbol:unbound.append(native);continue
        if not re.fullmatch('[1-9][0-9]*',row[4]):missing.append(native);continue
        if symbol in result:raise ValueError('opening_quote_duplicate_contract')
        result[symbol]=dict(symbol=symbol,native=native,effective_min_open_lots=int(row[4]),
            source_value=row[4],reported_update_day=day,
            ancillary_raw=dict(unit=row[2],limits=row[3],margin_buy=row[5],margin_sell=row[6],
                               open_fee=row[8],close_fee=row[9],close_today_fee=row[10]))
    if headers!=1 or not result:raise ValueError('opening_quote_no_usable_rows')
    return dict(product=key,contracts=result,unbound_native=unbound,missing_actual_open=missing,
                confidence='中：数据商公告汇编，非交易所原始指令；字段和源站标称更新日已核，汇编及时性未独立认证',
                source_sha256=receipt['sha256'],retrieved_at=receipt['finished_at'],source_url=receipt['url'])


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    raise SystemExit('仅离线提取明确字段；实际适用范围与冲突须另审')
