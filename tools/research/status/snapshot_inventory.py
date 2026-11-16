"""只读消费者冻结快照；先核整包及成员，再统计行情/公开参数，不执行账户。"""
import hashlib
import io
import json
from pathlib import Path, PurePosixPath
import re
import sys
import tarfile

from inventory_model import product, utc


def verified_members(raw, manifest_raw, archive_sha, manifest_sha):
    if hashlib.sha256(raw).hexdigest()!=archive_sha:
        raise ValueError('inventory_snapshot_archive_sha')
    if hashlib.sha256(manifest_raw).hexdigest()!=manifest_sha:
        raise ValueError('inventory_snapshot_manifest_sha')
    manifest=json.loads(manifest_raw)
    expected={'state/'+name:info for name,info in manifest['files'].items()}
    values={}
    with tarfile.open(fileobj=io.BytesIO(raw),mode='r:gz') as archive:
        members=archive.getmembers()
        if len({m.name for m in members})!=len(members):raise ValueError('inventory_snapshot_duplicate')
        if {m.name for m in members}!=set(expected)|{'export-manifest.json'}:
            raise ValueError('inventory_snapshot_file_set')
        for member in members:
            if not member.isfile() or '..' in PurePosixPath(member.name).parts or PurePosixPath(member.name).is_absolute():
                raise ValueError('inventory_snapshot_path')
            body=archive.extractfile(member).read()
            if member.name=='export-manifest.json':
                if body!=manifest_raw:raise ValueError('inventory_snapshot_embedded_manifest')
                continue
            info=expected[member.name]
            if len(body)!=info['bytes'] or hashlib.sha256(body).hexdigest()!=info['sha256']:
                raise ValueError('inventory_snapshot_file_sha')
            # 核所有成员身份，但只返回行情入口和公开参数；不导出计划或公司情景。
            if member.name.endswith('/main.json') or '/preparation/parameters/' in member.name:
                values[member.name]=body
    return manifest,values


def append_snapshot(collector, spec, known_products):
    raw=collector.read(Path(spec['archive']))
    manifest_raw=collector.read(Path(spec['manifest']))
    manifest,files=verified_members(raw,manifest_raw,spec['archive_sha256'],spec['manifest_sha256'])
    exported=utc(manifest['captured_at'])
    for name,body in files.items():
        data=json.loads(body);sha=hashlib.sha256(body).hexdigest()
        if name.endswith('/main.json'):
            for day in data['MainDays']:
                quotes={q['Contract']:q['Bar'] for q in day['Quotes']}
                candidates={c['Contract']:c for c in day['Candidates']}
                for canonical in sorted(set(quotes)|set(candidates)):
                    exchange,native=canonical.split('.')
                    if product(exchange,native)!='SHFE.rb':raise ValueError('inventory_snapshot_product_scope')
                    bar=quotes.get(canonical,{});quantity=candidates.get(canonical,{})
                    fields={k:bar.get(v) for k,v in [('open','Open'),('high','High'),('low','Low'),('close','Close'),('settle','Settle'),('pre_settle','PreSettle')]}
                    fields.update(volume=quantity.get('Volume'),open_interest=quantity.get('OI'))
                    collector.stats.add('engine-rb-snapshot','引擎RB冻结行情（派生）',dict(exchange=exchange,native=native,day=day['Day'],fields=fields,evidence={'sha256':sha}))
        else:
            kind=re.search(r'/([^/]+)-\d{8}\.json$',name)[1]
            mapping={'spec_long_margin':'SPEC_LONGMARGINRATIO','spec_short_margin':'SPEC_SHORTMARGINRATIO','trade_fee_rate':'TRADEFEERATION','trade_fee_fixed':'TRADEFEEUNIT','upper_limit_ratio':'UPPER_VALUE','lower_limit_ratio':'LOWER_VALUE','listing_day':'OPENDATE','expiry_day':'EXPIREDATE','settle':'SETTLEMENTPRICE'}
            lookup={k.split('.')[1]:k.split('.')[0] for k in known_products if k.split('.')[0] in {'SHFE','INE'}}
            for row in data[kind]:
                native=row['INSTRUMENTID'].strip();match=re.fullmatch(r'([a-z]+)\d{4}',native)
                if not match:continue
                if match[1] not in lookup:raise ValueError('inventory_snapshot_parameter_product')
                fields={key:row.get(column) for key,column in mapping.items()}
                collector.stats.add('engine-rb-public-'+kind,'上期所业务参数（冻结原件）',dict(exchange=lookup[match[1]],native=native,day=row['TRADINGDAY'],fields=fields,evidence={'sha256':sha}))
    for (_,route,_),group in collector.stats.groups.items():
        if route.startswith('engine-rb-'):
            group['last_exported_at']=exported
            group['read_level']='已核导出包/1434成员身份；行情为引擎派生，五份公开参数为原始表；HTTP采集时刻未记录。'


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    raise SystemExit('此模块由全项目资料采集器导入')
