"""解析发布者自己的指定日公司保证金；不把转载交易所栏或公司标准当真实账户实收。"""
import io
import sys
from decimal import Decimal, InvalidOperation
from html.parser import HTMLParser
import re
import copy
import datetime as dt
import gzip
import hashlib
import json
import math

MARKETS={'上期所':'SHFE','能源交易所':'INE','大商所':'DCE','郑商所':'CZCE','广期所':'GFEX','中金所':'CFFEX'}


class Table(HTMLParser):
    def __init__(self):
        super().__init__();self.rows=[];self.row=None;self.cell=None

    def handle_starttag(self,tag,attrs):
        if tag=='tr':self.row=[]
        if tag in {'td','th'} and self.row is not None:self.cell=[dict(attrs),[]]

    def handle_endtag(self,tag):
        if tag in {'td','th'} and self.cell is not None:
            self.row.append((self.cell[0],''.join(''.join(self.cell[1]).split())));self.cell=None
        if tag=='tr' and self.row is not None:self.rows.append(self.row);self.row=None

    def handle_data(self,value):
        if self.cell is not None:self.cell[1].append(value)


def expand(rows):
    carry={};result=[]
    for cells in rows:
        row={key:value for key,(remaining,value) in carry.items()}
        carry={key:(remaining-1,value) for key,(remaining,value) in carry.items() if remaining>1}
        column=0
        for attrs,value in cells:
            while column in row:column+=1
            rs,cs=int(attrs.get('rowspan','1')),int(attrs.get('colspan','1'))
            if not (1<=rs<=200 and 1<=cs<=14):raise ValueError('broker_margin_span')
            for at in range(column,column+cs):
                row[at]=value
                if rs>1:carry[at]=(rs-1,value)
            column+=cs
        result.append([row.get(i,'') for i in range(max(row,default=-1)+1)])
    return result


def ratio(value):
    try:r=Decimal(value.rstrip('%'))/100
    except InvalidOperation:raise ValueError('broker_margin_ratio')
    if not r.is_finite() or not 0<r<=1:raise ValueError('broker_margin_ratio')
    return format(r,'f')


def parse(raw,day,known):
    text=raw.decode('utf-8');p=Table();p.feed(text)
    if f'{day}期货保证金率及涨跌停板' not in ''.join(re.sub('<[^>]*>','',text).split()):
        raise ValueError('broker_margin_day')
    rows=expand(p.rows)
    if len(rows)<3 or rows[0][3:7]!=['交易所保证金（%）']*2+['公司保证金（%）']*2:
        raise ValueError('broker_margin_header')
    policies={};current=None
    for index,row in enumerate(rows[2:],3):
        if len(row)!=14:continue  # 表尾声明等，不是合约数据行。
        if row[2]:
            key=MARKETS.get(row[0],'?')+'.'+row[2]
            current=key if key in known else None
            if current:
                if current in policies:raise ValueError('broker_margin_duplicate_product')
                policies[current]=dict(product=current,company_spec_ratio=ratio(row[5]),
                                       company_hedge_ratio=ratio(row[6]),special=[],source_rows=[index])
        if current and row[8]:
            policies[current]['special'].append(dict(symbols=row[8],company_spec_ratio=ratio(row[11]),
                                                     company_hedge_ratio=ratio(row[12]),source_row=index))
    return policies


def special_symbols(expression,key,aliases):
    """三位年份只用已存在的显式年月绑定，不按近年猜；不能解释的整个产品停映射。"""
    exchange,product=key.split('.')
    result=set()
    def month(token):
        if len(token)==4:return 2000+int(token[:2]),int(token[2:])
        native=exchange+'.'+product+token
        if native not in aliases:raise ValueError('broker_margin_year_binding')
        canonical=aliases[native]
        return 2000+int(canonical[-4:-2]),int(canonical[-2:])
    for part in re.split('[、,，]',expression):
        found=re.fullmatch(r'(?:'+re.escape(product)+r')?(\d{3,4})(?:-(?:'+re.escape(product)+r')?(\d{3,4}))?',part,re.I)
        # 缺品种前缀的后续列表项也显式属于同一行品种。
        if not found:found=re.fullmatch(r'(\d{3,4})(?:-(\d{3,4}))?',part)
        if not found:raise ValueError('broker_margin_special_syntax')
        first=month(found[1]);last=month(found[2]) if found[2] else first
        if not (1<=first[1]<=12 and 1<=last[1]<=12):raise ValueError('broker_margin_special_month')
        start=first[0]*12+first[1]-1;end=last[0]*12+last[1]-1
        if not 0<=end-start<=36:raise ValueError('broker_margin_special_range')
        for item in range(start,end+1):result.add(exchange+'.'+product+str(item//12)[-2:]+f'{item%12+1:02}')
    return result


def bind(policy,scope,aliases):
    overrides={}
    for special in policy['special']:
        for symbol in special_symbols(special['symbols'],policy['product'],aliases):
            value=special['company_spec_ratio']
            if symbol in overrides and overrides[symbol]!=value:raise ValueError('broker_margin_conflicting_override')
            overrides[symbol]=value
    return {symbol:overrides.get(symbol,policy['company_spec_ratio']) for symbol in scope}


def build_current_facts(base,raw,receipt,scope_raw,known):
    """从同一组冻结输入复算；保留已有交易所参数和所有最低开仓原值。"""
    address='https://www.minfutures.com/main/a/20261116/86319.shtml'
    if (receipt['url']!=address or receipt['http']['status']!=200 or not receipt['http']['complete'] or
            receipt['sha256']!=hashlib.sha256(raw).hexdigest()):
        raise ValueError('broker_margin_source_identity')
    day=base['current_trade_day'];policies=parse(raw,day,known)
    observed=json.loads(gzip.decompress(scope_raw));aliases={};scopes={};native_keys={}
    end=dt.datetime.strptime(str(day),'%Y%m%d').replace(hour=15,tzinfo=dt.timezone(dt.timedelta(hours=8))).timestamp()
    for native,row in observed.items():
        ex,code,year,month=row.get('exchange_id'),row.get('product_id'),row.get('delivery_year'),row.get('delivery_month')
        key=str(ex)+'.'+str(code);expiry=row.get('expire_datetime')
        if (key not in known or row.get('class')!='FUTURE' or row.get('expired') is not False or
                type(expiry) not in [int,float] or not math.isfinite(expiry) or expiry<end or
                type(year) is not int or type(month) is not int or not 1<=month<=12):continue
        expected=key+(str(year)[-1:] if ex=='CZCE' else str(year)[-2:])+f'{month:02}'
        if native!=expected or row.get('instrument_id')!=native:continue
        canonical=key+str(year)[-2:]+f'{month:02}'
        aliases[native]=canonical;scopes.setdefault(key,[]).append(canonical);native_keys[canonical]=native
    for row in base['current_contracts']:
        scopes.setdefault(row['product_key'],[]).append(row['symbol']);native_keys[row['symbol']]=row['source_native']
    for key,symbols in base['settlement_scopes'].items():
        scopes.setdefault(key,[]).extend(symbols)
        for symbol in symbols:
            native_keys.setdefault(symbol,symbol.split('.')[0]+'.'+key.split('.')[1]+symbol[-3:])
    bound={};unmapped={}
    for key,policy in policies.items():
        try:bound.update(bind(policy,sorted(set(scopes.get(key,[]))),aliases))
        except ValueError as error:unmapped[key]=str(error)
    result=copy.deepcopy(base);existing={r['symbol'] for r in base['current_contracts']};added=[]
    for symbol,value in bound.items():
        if symbol in existing:continue
        key=next(k for k in known if symbol.startswith(k) and symbol[len(k):].isdigit())
        result['current_contracts'].append(dict(product_key=key,symbol=symbol,source_native=native_keys[symbol],
            source_snapshot_day=day,as_of_trade_day=day,
            margin=dict(long=value,short=value,unit='ratio',basis='company',publisher='五矿期货',
                        published_direction_scope='统一投机比例情景，并非分方向客户回执',evidence_ids=['broker-margin-20261117']),
            effective_min_open_lots=None,ordinary_min_order_lots=None,minimum_lots_kind='effective_min_open',
            special_adjustments_reviewed=False,minimum_evidence_ids=[]))
        added.append(symbol)
    result['evidence'].append(dict(id='broker-margin-20261117',sha256=receipt['sha256'],bytes=len(raw),
        source_url=address,source_day=day,retrieved_at=receipt['finished_at'],kind='publisher-own-company-margin-standard-not-exchange-original'))
    result['source_limits'].append('新公司比例仅为五矿期货17日公开投机标准及逐月份专项，作单比例公司参数情景；不代表本账户实收，不替换已核25品种交易所多空原件。新增合约最低开仓全部未知。')
    return result,dict(policies=policies,bound_ratios=bound,unmapped=unmapped,added_company_contracts=added)


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    raise SystemExit('仅离线解析公开公司标准；不变更账户或生产策略')
