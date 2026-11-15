"""数据总览模型：将主目录、其他样本、制度资料与金融准入分层，禁止跨路线相加。"""
from collections import Counter
import datetime as dt
import io
import re
import sys


def product_key(exchange, code, keys):
    m = re.fullmatch(r'([A-Za-z]+)[0-9]{3,4}', code)
    matches = [k for k in keys if m and k.split('.')[0]==exchange and k.split('.')[1].upper()==m[1].upper()]
    if len(matches)!=1:
        raise ValueError('status_unmapped_sample:' + exchange+'.'+code)
    return matches[0]


def date_text(value):
    return dt.datetime.strptime(str(value),'%Y%m%d').strftime('%Y-%m-%d')


def profile_ranges(profiles):
    result = {}
    for file, profile in profiles:
        for series in profile['series']:
            key = series['product']['Exchange']+'.'+series['product']['Product']
            records = series['records']
            dates = [dt.datetime.strptime(str(r['day']),'%Y%m%d').date() for r in records]
            if not dates or any(b-a!=dt.timedelta(days=1) for a,b in zip(dates,dates[1:])):
                raise ValueError('status_profile_dates')
            result.setdefault(key,[]).append(dict(first=dates[0].isoformat(),last=dates[-1].isoformat(),
                                                open_records=sum(bool(r['sessions']) for r in records),file=file))
    for ranges in result.values():
        ranges.sort(key=lambda r:r['first'])
        if any(a['last']>=b['first'] for a,b in zip(ranges,ranges[1:])):
            raise ValueError('status_profile_overlap')
    return result


def model(catalog, reports, names, profiles, sina, legacy, labels, as_of):
    if catalog['full_market_universe_certified'] is not False or catalog['strict_backtest_admission_expanded'] is not False:
        raise ValueError('status_scope_promotion')
    contracts = catalog['contracts']; seen = set(); states=Counter(); rows=0; products={}
    ranges = profile_ranges(profiles)
    reference_identity = {'shfe':'SHFE/INE官方HTTPS年度原表（2021—2025）',
                          'cffex':'CFFEX封存月包及有限参考（明文HTTP，发布者未认证）',
                          'czce':'CZCE官方HTTPS日表（仅2021-01-04）'}
    for c in contracts:
        symbol=c['symbol'];key=c['exchange']+'.'+c['product'];a=c['availability'];state=a['state']
        if symbol in seen or c['financial_eligible'] is not False or c['lifecycle_certified'] is not False or c['official_listing_day'] is not None or c['official_last_trading_day'] is not None:
            raise ValueError('status_contract_scope_or_duplicate')
        seen.add(symbol);states[state]+=1
        if state not in ['nonempty','empty_unavailable','not_requested_on_directory_route']:
            raise ValueError('status_state')
        display=names.get(key,{}).get('name',c['product'])
        p=products.setdefault(key,dict(key=key,exchange=c['exchange'],code=c['product'],
            name=labels['product_display_aliases'].get(key,display),vendor_name=display,
            category='金融期货' if c['exchange']=='CFFEX' else ('航运指数' if key=='INE.ec' else '商品期货'),
            candidates=0,nonempty=0,empty_unavailable=0,not_requested_on_directory_route=0,rows=0,first=None,last=None,
            reference_days=0,overlapping_days=0,missing_reference_days=0,unpriced_fields=0,distinct_fields=0,
            binary64_equivalent_fields=0,calendar_unknown_rows=0,calendar_ranges=ranges.get(key,[]),
            references=[],supplementary=[],financial_eligible=False))
        p['candidates']+=1;p[state]+=1
        if state=='empty_unavailable':
            if a['counts_as_success'] is not False or a['counts_as_coverage'] is not False or a['rows']!=0 or a['raw_receipt_state']!='failed':
                raise ValueError('status_empty_promotion')
        if state!='nonempty':continue
        n=a['rows'];rows+=n;p['rows']+=n
        first,last=date_text(a['first_observed_day']),date_text(a['last_observed_day'])
        p['first']=min(p['first'] or first,first);p['last']=max(p['last'] or last,last)
        source=a['source_id'];r=reports[source][symbol];cr=c['reference_review']
        if c['exchange'] not in {'shfe':{'SHFE','INE'},'czce':{'CZCE'},'cffex':{'CFFEX'}}[source]:
            raise ValueError('status_reference_exchange')
        if (n,a['first_observed_day'],a['last_observed_day'])!=(r['rows'],r['first_day'],r['last_day']) or r['financial_eligible'] is not False:
            raise ValueError('status_report_range_or_scope')
        if cr['reference_days']!=r['official_days'] or cr['unresolved_distinct_fields']!=r['binary64_distinct_differences'] or cr['calendar_unknown_actual_days']!=len(r['calendar_unknown_actual_days']):
            raise ValueError('status_report_totals')
        p['reference_days']+=r['official_days'];p['overlapping_days']+=r['overlapping_days']
        p['missing_reference_days']+=len(r['missing_official_days']);p['unpriced_fields']+=len(r['official_unpriced_fields'])
        p['distinct_fields']+=r['binary64_distinct_differences'];p['binary64_equivalent_fields']+=r['binary64_equivalent_differences']
        p['calendar_unknown_rows']+=len(r['calendar_unknown_actual_days'])
        if reference_identity[source] not in p['references']:p['references'].append(reference_identity[source])
    expected=catalog['summary']
    if len(seen)!=expected['candidates'] or rows!=expected['rows'] or dict(states)!=expected['states']:
        raise ValueError('status_catalog_totals')

    # 其他已取得样本不改变主目录分母或行数；同源重复观测也不相加宣称覆盖。
    supplement_seen=set()
    for item in sina:
        key=product_key(item['exchange'],item['code'],products);identity=(item['exchange'],item['code'])
        if identity in supplement_seen:raise ValueError('status_sina_duplicate')
        supplement_seen.add(identity);d=item.get('daily')
        if not d or not (d.get('raw_rows') or 0):continue
        if d.get('financial_eligible') is not False:raise ValueError('status_supplement_scope')
        products[key]['supplementary'].append(dict(source='新浪当期列表日线样本',symbol=item['code'],rows=d['raw_rows'],
            first=d.get('raw_first'),last=d.get('raw_last'),comparison='供应商样本；非官方全集/完整历史',
            zero_settlement=d.get('zero_settlement',0),absent_settlement=d.get('absent_settlement',0),
            invalid_days=len(d.get('invalid_ohlc_or_quantity_days',[])),financial_eligible=False))
    for symbol,r in legacy['results'].items():
        exchange,code=symbol.split('.');key=product_key(exchange,code,products)
        if r.get('financial_eligible',False) is not False:
            raise ValueError('status_supplement_scope')
        products[key]['supplementary'].append(dict(source='EDB旧有限日线样本',symbol=symbol,rows=r['rows'],
            first=r['first_day'],last=r['last_day'],comparison='仅供应商间对照，待核字段'+str(len(r['differences'])),
            unresolved_fields=len(r['differences']),financial_eligible=False))
    for p in products.values():
        p['state']='primary' if p['nonempty'] else ('sample_only' if p['supplementary'] else 'directory_only')
        p['source']='Shinny EDB日线' if p['nonempty'] else '主目录未取非空日线'
        p['calendar_gap']=not p['calendar_ranges'] or p['calendar_unknown_rows']>0
        p['date_span_is_completeness']=False
        if p['reference_days']:
            p['settlement']='参考原表已归档结算价列（范围同参考，不代表每行有值）；主EDB无此列，本批未作结算对账或统一Source交付'
            p['turnover']='参考原表已归档成交额列；值、单位和传输资格另核，主EDB无此列，本批未作金额对账或统一交付'
        else:
            p['settlement']='主EDB源无此列；其他样本未形成可信结算交付'
            p['turnover']='主EDB源无成交额列；未完成可信金额交付'
    normalized_states={state:states[state] for state in ['nonempty','empty_unavailable','not_requested_on_directory_route']}
    summary=dict(candidates=len(contracts),products=len(products),rows=rows,states=normalized_states,
                 commodity_and_shipping_products=sum(p['category']!='金融期货' for p in products.values()),
                 financial_products=sum(p['category']=='金融期货' for p in products.values()),
                 unresolved_fields=sum(p['distinct_fields'] for p in products.values()),
                 calendar_unknown_rows=sum(p['calendar_unknown_rows'] for p in products.values()),
                 missing_reference_days_nonempty=sum(p['missing_reference_days'] for p in products.values()),
                 supplementary_sina_nonempty=sum(s['source']=='新浪当期列表日线样本' for p in products.values() for s in p['supplementary']))
    summary['legacy_vendor_distinct_fields']=sum(len(r['differences']) for r in legacy['results'].values())
    return dict(schema='market-product-data-status-v1',as_of=as_of,summary=summary,
                exchanges={e:labels[e] for e in ['SHFE','INE','CZCE','DCE','GFEX','CFFEX']},
                products=sorted(products.values(),key=lambda p:(list(['SHFE','INE','CZCE','DCE','GFEX','CFFEX']).index(p['exchange']),p['code'])),
                strict_backtest_admission_expanded=False,new_market_requests=0)


if __name__=='__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    raise SystemExit('此模块由generate.py导入')
