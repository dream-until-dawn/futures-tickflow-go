"""补EDB首观察前缀或整个缺失年份；与默认序列严格日期不重叠。"""
import hashlib
from decimal import Decimal
import io
import json
from pathlib import Path
import re
import sys

from backtest_v2_bundle import valid_official_ohlc
from commodity_prices import coverage, official_fields


def czce_rows(raw, day):
    try:
        text=raw.decode('utf-8-sig')
    except UnicodeDecodeError:
        text=raw.decode('gb18030')
    date=re.search(r'(\d{4})-(\d{1,2})-(\d{1,2})',text.splitlines()[0])
    if not date or int(''.join([date[1],date[2].zfill(2),date[3].zfill(2)]))!=day:
        raise ValueError('official_prefix_czce_day')
    lines=[line for line in text.splitlines() if '|' in line]
    header=[s.strip() for s in lines[0].split('|')]
    return {v[0]:dict(zip(header,v)) for line in lines[1:] if (v:=[s.strip() for s in line.split('|')]) and re.fullmatch(r'[A-Z]+\d{3,4}',v[0])}


def extend(rows, selected, facts, inventory, config, shards, aliases):
    boundaries = {}
    missing_years = {}
    for key in selected:
        history = rows.product(key)
        try:
            coverage(history, facts.get('listing_days', {}).get(key), facts['latest_completed_day'])
        except ValueError:
            if not history:
                raise ValueError('official_prefix_no_edb_anchor:' + key)
            boundaries[key] = min(r['day'] for r in history)
            listing=facts.get('listing_days',{}).get(key)
            baseline=max(2021,listing['day']//10000) if listing else 2021
            missing_years[key]=set(range(baseline,facts['latest_completed_day']//10000+1))-{r['day']//10000 for r in history}
    if not boundaries:
        return {}
    pinned = {str(Path(r['path']).resolve()).casefold(): r for r in inventory['input_files']}
    def read(path):
        path=Path(path); raw=path.read_bytes(); entry=pinned.get(str(path.resolve()).casefold())
        if not entry or len(raw)!=entry['bytes'] or hashlib.sha256(raw).hexdigest()!=entry['sha256']:
            raise ValueError('official_prefix_catalog_binding')
        return raw
    manifest=Path(config['official_catalog_manifest']); meta=json.loads(read(manifest))
    records=read(manifest.parent/'records.jsonl')
    if hashlib.sha256(records).hexdigest()!=meta['records_sha256']:
        raise ValueError('official_prefix_catalog_sha')
    plan_path=manifest.parent.parent/'parse-plan.json'; plan_raw=plan_path.read_bytes()
    documents={r['id']:r for r in json.loads(plan_raw)['documents']}
    repair_path=manifest.parent.parent/'recovered-20210728/parse-plan.json'
    repair_raw=repair_path.read_bytes()
    for r in json.loads(repair_raw)['documents']:
        if r['id'] in documents:
            raise ValueError('official_prefix_duplicate_document')
        documents[r['id']]=r
    installed=set(); raw_rows={}; counts={k:0 for k in boundaries}
    for line in records.splitlines():
        r=json.loads(line)['record'];native=r['native']
        match=re.fullmatch(r'([A-Za-z]+)([0-9]{3,4})',native)
        if not match:
            continue
        key=r['exchange']+'.'+match[1]
        if key not in boundaries or not 20210101<=r['day']<=facts['latest_completed_day'] or not (
                r['day']<boundaries[key] or r['day']//10000 in missing_years[key]):
            continue
        fields=official_fields(r['fields'])
        if fields is None:
            continue
        symbol=aliases.get(r['exchange']+'.'+native) if len(match[2])==3 else r['exchange']+'.'+native
        if not symbol:
            raise ValueError('official_prefix_explicit_year_binding:'+native)
        source=r['evidence']; identity='official-prefix-'+source['id']; pack=shards[r['exchange']]
        if (r['exchange'], identity) not in installed:
            document=documents[json.loads(line)['document_id']]
            raw=Path(document.get('original_file',document['file'])).read_bytes()
            if hashlib.sha256(raw).hexdigest()!=source['sha256']:
                raise ValueError('official_prefix_original_sha')
            receipt_path=Path(document['receipt_file']) if document.get('receipt_file') else Path(document['file']).with_suffix('.json')
            embedded=None
            if not receipt_path.exists() and (receipt_path.parent/'export-manifest.json').exists():
                receipt_path=receipt_path.parent/'export-manifest.json'
                receipt_raw=receipt_path.read_bytes()
                entries=[v['receipt'] for v in json.loads(receipt_raw)['records'] if v['id']==source['id']]
                if len(entries)!=1:
                    raise ValueError('official_prefix_embedded_receipt')
                embedded=entries[0]
            else:
                receipt_raw=receipt_path.read_bytes()
            if document.get('receipt_sha256') and hashlib.sha256(receipt_raw).hexdigest()!=document['receipt_sha256']:
                raise ValueError('official_prefix_receipt_sha')
            if not document.get('receipt_sha256'):
                receipt=embedded or json.loads(receipt_raw)
                if receipt.get('state')!='received' or receipt.get('evidence',{}).get('sha256')!=source['sha256']:
                    raise ValueError('official_prefix_recent_receipt_identity')
            pack.add(identity,'source-original',raw,source['url'],received=source.get('retrieved_at'))
            pack.add(identity+'-receipt.json','acquisition-receipt',receipt_raw)
            pack.values[identity][0]['retained_source_version']=source.get('version')
            if document['format']=='shfe-json':
                original=json.loads(raw,parse_float=Decimal)
                if str(original['report_date']).replace('-','')[:8]!=str(r['day']):
                    raise ValueError('official_prefix_original_day')
                raw_rows[identity]={str(v['PRODUCTID']).strip().removesuffix('_f')+str(v['DELIVERYMONTH']).strip():v
                    for v in original['o_curinstrument'] if str(v['DELIVERYMONTH']).strip().isdigit()}
            elif document['format']=='czce-txt':
                raw_rows[identity]=czce_rows(raw,r['day'])
            installed.add((r['exchange'], identity))
        if identity in raw_rows:
            original=raw_rows[identity][native]
            names=dict(open='OPENPRICE',high='HIGHESTPRICE',low='LOWESTPRICE',close='CLOSEPRICE',
                       volume='VOLUME',open_interest='OPENINTEREST',settle='SETTLEMENTPRICE',turnover='TURNOVER')
            if r['exchange']=='CZCE':
                names=dict(open='今开盘',high='最高价',low='最低价',close='今收盘',volume='成交量(手)',
                           open_interest='持仓量',settle='今结算',turnover='成交额(万元)')
            for name,field in names.items():
                actual=original.get(field); expected=fields.get(name)
                if actual in (None,'') and expected in (None,''):
                    continue
                if actual in (None,'') or expected in (None,'') or Decimal(str(actual).strip().replace(',',''))!=Decimal(expected):
                    raise ValueError('official_prefix_raw_field_mismatch:'+identity+':'+native+':'+name)
        # 前缀不覆盖任何已有日期；保留原字段原精度、原件行/工作表位置。
        record=dict(product_key=key,symbol=symbol,source_native=native,day=r['day'],
            fields=fields,
            source_id=identity,source_member=r.get('member'),source_row=r.get('row'))
        record['source_turnover_unit']=r.get('turnover_unit','unknown')
        rows.add(record);counts[key]+=1
    for key in boundaries:
        shards[key.split('.')[0]].add('official-prefix-parse-plan.json','review-evidence',plan_raw)
        shards[key.split('.')[0]].add('official-prefix-repair-plan.json','review-evidence',repair_raw)
    return {key:dict(before_exclusive=boundaries[key],missing_years=sorted(missing_years[key]),added_rows=n,
                     rule='official earlier prefix or entirely absent years; no overlapping date') for key,n in counts.items()}


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    raise SystemExit('仅从冻结官方目录补日期严格不重叠的历史前缀')
