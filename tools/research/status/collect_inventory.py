"""只读盘点已有全项目资料，输出公开字段汇总；不联网，不写原件。"""
import argparse
import csv
import datetime as dt
import hashlib
import io
import json
from pathlib import Path
import re
import sys
from urllib.parse import parse_qs, urlparse
import xml.etree.ElementTree as ET
import zipfile

from inventory_model import Accumulator, product, utc


class Collector:
    def __init__(self, repo):
        self.repo = repo
        self.stats = Accumulator()
        self.inputs = []
        self.extra = []
        self.seen_bodies = set()
        self.body_routes = {}
        self.not_located = []

    def read(self, path):
        raw = path.read_bytes()
        self.inputs.append(dict(path=str(path), bytes=len(raw), sha256=hashlib.sha256(raw).hexdigest()))
        return raw

    def catalog(self, manifest, route='official-catalog'):
        meta = json.loads(self.read(manifest))
        path = manifest.parent / 'records.jsonl'
        raw = self.read(path)
        if hashlib.sha256(raw).hexdigest() != meta['records_sha256']:
            raise ValueError('inventory_catalog_sha')
        count = 0
        for line in raw.splitlines():
            row = json.loads(line)
            record = row['record']
            self.stats.add(route, record['exchange']+' 官方日行情', record, meta['at_utc'])
            count += 1
        if count != meta['rows']:
            raise ValueError('inventory_catalog_count')

    def edb(self, root, route):
        if not root.is_dir() or not any(root.rglob('*.body')):
            raise ValueError('inventory_edb_root_missing')
        for body in sorted(root.rglob('*.body')):
            # 路线根严格限于旧封存的EDB正文，不扫描测试/负例副本。
            raw = body.read_bytes()
            if not raw.startswith(b'datetime_nano,'):
                continue
            receipt_path = body.with_suffix('.json')
            if not receipt_path.exists():
                raise ValueError('inventory_edb_receipt_missing')
            receipt = json.loads(self.read(receipt_path))
            evidence = receipt['evidence']
            digest = hashlib.sha256(raw).hexdigest()
            if digest != evidence['sha256'] or receipt['state'] != 'received':
                # 原空响应/失败不填进已有数据格。
                if receipt['state'] == 'failed':
                    continue
                raise ValueError('inventory_edb_sha_or_state')
            query = parse_qs(urlparse(evidence['url']).query)
            exchange, native = query['symbol'][0].split('.')
            identity=(digest,exchange,native,query.get('period',[None])[0])
            if identity in self.seen_bodies:
                previous=self.body_routes[identity]
                for (key,existing_route,_), group in self.stats.groups.items():
                    if key==product(exchange,native) and existing_route==previous and evidence.get('retrieved_at'):
                        time=utc(evidence['retrieved_at'])
                        group['last_acquired_at']=max(group['last_acquired_at'] or time,time)
                        group['last_receipt_at']=max(group['last_receipt_at'] or time,time)
                continue
            self.seen_bodies.add(identity)
            self.body_routes[identity]=route
            self.inputs.append(dict(path=str(body), bytes=len(raw), sha256=digest))
            rows = list(csv.DictReader(io.StringIO(raw.decode('utf-8'))))
            if query.get('period') == ['60']:
                dates = [dt.datetime.fromtimestamp(int(r['datetime_nano'])//1_000_000_000,dt.timezone.utc).astimezone(dt.timezone(dt.timedelta(hours=8))).isoformat() for r in rows]
                if dates:
                    self.extra.append(dict(product=product(exchange,native),route=route+'-minute',source='天勤 EDB（分钟样本）',kind='minute',first=min(dates),last=max(dates),rows=len(rows),present_rows=len(rows),valid_rows=None,last_acquired_at=utc(evidence.get('retrieved_at')),last_receipt_at=None,last_indexed_at=None,completeness='observed_only',expected_contract_days=None,limitations='实际分钟样本；时间戳按UTC+8显示，不替代交易日归属；不表示分钟历史完整。',evidence_shas=[digest]))
                continue
            for row in rows:
                day = dt.datetime.fromtimestamp(int(row['datetime_nano'])//1_000_000_000,dt.timezone.utc).astimezone(dt.timezone(dt.timedelta(hours=8))).strftime('%Y%m%d')
                fields = {k:row[k] for k in ('open','high','low','close','volume')}
                fields['open_interest'] = row['close_oi']
                self.stats.add(route,'天勤 EDB',dict(exchange=exchange,native=native,day=day,fields=fields,evidence=evidence))

    def sina(self, body, exchange, native, evidence, route):
        raw = self.read(body)
        digest = hashlib.sha256(raw).hexdigest()
        if evidence.get('sha256') and digest != evidence['sha256']:
            raise ValueError('inventory_sina_sha')
        identity=(digest,exchange,native)
        if identity in self.seen_bodies:
            return
        self.seen_bodies.add(identity)
        # 只解析受信原字节中的JSON数组，绝不执行JSONP或页面脚本。
        text = raw.decode('utf-8')
        rows = json.loads(text[text.index('['):text.rindex(']')+1])
        for row in rows:
            fields = {target:row.get(source) for target,source in [('open','o'),('high','h'),('low','l'),('close','c'),('volume','v'),('open_interest','p'),('settle','s')]}
            self.stats.add(route,'新浪（日线样本）',dict(exchange=exchange,native=native,day=row['d'].replace('-',''),fields=fields,evidence=dict(evidence,sha256=digest)))

    def profiles(self):
        for path in sorted((self.repo/'calendar/verified/profiles').glob('*.json')):
            data = json.loads(self.read(path))
            for series in data['series']:
                records = series['records']
                dates = [str(r['day']) for r in records]
                if not dates:
                    continue
                parsed = [dt.datetime.strptime(d,'%Y%m%d').date() for d in dates]
                if any(b-a != dt.timedelta(days=1) for a,b in zip(parsed,parsed[1:])):
                    raise ValueError('inventory_calendar_gap')
                times = [utc(e.get('retrieved_at')) for r in records for e in r.get('evidence',[]) if e.get('retrieved_at')]
                self.extra.append(dict(product=series['product']['Exchange']+'.'+series['product']['Product'],route='verified-calendar',source='官方公告／规则（逐日历）',kind='calendar',first=min(parsed).isoformat(),last=max(parsed).isoformat(),rows=len(records),present_rows=len(records),valid_rows=len(records),last_acquired_at=max(times) if times else None,last_receipt_at=None,last_indexed_at=None,completeness='verified_calendar_range',expected_contract_days=None,limitations='自然日记录连续；只核所列公布日程，不含未知临停、集合竞价、全合约价格和金融准入。',evidence_shas=[hashlib.sha256(path.read_bytes()).hexdigest()],file=str(path.relative_to(self.repo))))

    def cffex(self, root):
        for receipt_path in sorted(root.rglob('receipt.json')):
            body=receipt_path.parent/'month.body'
            if not body.exists():continue
            receipt=json.loads(self.read(receipt_path));raw=self.read(body)
            evidence=receipt['evidence']
            if hashlib.sha256(raw).hexdigest()!=evidence['sha256']:
                raise ValueError('inventory_cffex_sha')
            if receipt.get('publisher_authentication') is not False:
                raise ValueError('inventory_cffex_authentication')
            with zipfile.ZipFile(io.BytesIO(raw)) as archive:
                if archive.testzip() is not None:raise ValueError('inventory_cffex_crc')
                for member in archive.infolist():
                    if not re.fullmatch(r'\d{8}_1\.csv',member.filename):raise ValueError('inventory_cffex_member')
                    for row in csv.DictReader(io.StringIO(archive.read(member).decode('gb18030'))):
                        code=row['合约代码'].strip()
                        if not re.fullmatch(r'[A-Z]{1,3}\d{4}',code):continue
                        mapping={'open':'今开盘','high':'最高价','low':'最低价','close':'今收盘','settle':'今结算','pre_settle':'前结算','volume':'成交量','open_interest':'持仓量','turnover':'成交金额'}
                        fields={key:row[value].strip() for key,value in mapping.items()}
                        self.stats.add('cffex-monthly','中金所（明文未认证月包）',dict(exchange='CFFEX',native=code,day=member.filename[:8],fields=fields,evidence=evidence))

    def cffex_fixture(self):
        path=self.repo/'source/cffexsource/testdata/daily_20260908.xml'
        root=ET.fromstring(self.read(path))
        for node in root.iter('dailydata'):
            native=(node.findtext('instrumentid') or '').strip()
            if not re.fullmatch(r'[A-Z]{1,3}\d{4}',native):continue
            mapping={'open':'openprice','high':'highestprice','low':'lowestprice','close':'closeprice','settle':'settlementprice','pre_settle':'presettlementprice','volume':'volume','open_interest':'openinterest','turnover':'turnover'}
            fields={key:node.findtext(value) for key,value in mapping.items()}
            self.stats.add('cffex-real-fixture','中金所（早期真实HTTP样本）',dict(exchange='CFFEX',native=native,day=20260908,fields=fields,evidence={'sha256':hashlib.sha256(path.read_bytes()).hexdigest()}))

    def parameters(self, root, route, known_products):
        ledger=json.loads(self.read(root/'ledger.json'))
        lookup={}
        for key in known_products:
            exchange,code=key.split('.')
            if exchange in {'SHFE','INE'}:lookup.setdefault(code.lower(),[]).append(exchange)
        for item in ledger:
            if item.get('state')!='dated_table_validated':continue
            path=root/item['file']
            if not path.resolve().is_relative_to(root.resolve()):raise ValueError('inventory_parameters_path')
            raw=self.read(path);digest=hashlib.sha256(raw).hexdigest()
            if digest!=item['sha256']:raise ValueError('inventory_parameters_sha')
            if digest in self.seen_bodies:continue
            self.seen_bodies.add(digest)
            data=json.loads(raw)
            for row in data[item['kind']]:
                native=row.get('INSTRUMENTID','').strip();match=re.fullmatch(r'([a-z]+)\d{4}',native)
                if not match:continue
                exchanges=lookup.get(match[1],[])
                if len(exchanges)!=1:raise ValueError('inventory_parameters_exchange')
                mapping={'spec_long_margin':'SPEC_LONGMARGINRATIO','spec_short_margin':'SPEC_SHORTMARGINRATIO','trade_fee_rate':'TRADEFEERATION','trade_fee_fixed':'TRADEFEEUNIT','upper_limit_ratio':'UPPER_VALUE','lower_limit_ratio':'LOWER_VALUE','listing_day':'OPENDATE','expiry_day':'EXPIREDATE','settle':'SETTLEMENTPRICE'}
                fields={k:row.get(v) for k,v in mapping.items()}
                # UPDATE_DATE是表内业务更新时间，不是HTTP采集时刻。
                evidence={'sha256':digest}
                if item.get('retrieved_at'):evidence['retrieved_at']=item['retrieved_at']
                self.stats.add(route+'-'+item['kind'],'上期所业务参数（'+item['kind']+'）',dict(exchange=exchanges[0],native=native,day=item['day'],fields=fields,evidence=evidence))

    def evidence_index(self):
        """全仓证据JSON逐件登记，不能识别的数据不静默消失，也不推为行情。"""
        files=[]
        for path in sorted((self.repo/'docs/evidence').rglob('*.json')):
            if 'project-data-inventory' in path.name:
                continue
            raw=path.read_bytes()
            try:
                data=json.loads(raw)
            except (ValueError,UnicodeError):
                continue
            files.append(dict(file=str(path.relative_to(self.repo)),sha256=hashlib.sha256(raw).hexdigest(),bytes=len(raw),schema=data.get('schema') if isinstance(data,dict) else None))
        return files

    def finish(self, when):
        datasets=self.stats.finish()+self.extra
        for row in datasets:
            row['read_level']=row.get('read_level') or ('旧文档登记；本轮原件未定位' if row['completeness']=='recorded_original_not_located' else
                               '派生目录逐字段重数及manifest核对；未重读全部原始年包/日表' if row['route']=='official-catalog' else
                               '供应商候选目录登记；不认证官方生命周期' if row['route']=='supplier-directory' else
                               '已读取冻结文件并核字节身份/字段；不认证全市场完整性')
            shas=sorted(row['evidence_shas'])
            if 'evidence_count' not in row:
                row['evidence_count']=len(shas)
                row['evidence_set_sha256']=hashlib.sha256('\n'.join(shas).encode()).hexdigest()
                row['evidence_shas']=shas[:3]
            dates={'sina-20260909-fixture':'2026-09-09','cffex-real-fixture':'2026-09-09','sina-20260918-fixture':'2026-09-18'}
            if row['route'] in dates:
                row['last_acquired_date']=dates[row['route']]
                row['limitations']+=' 原始取得仅有同目录说明中的日期；精确时间未知。'
            if row['kind'] in {'directory','margin','fees','limits'} and row['route']!='supplier-directory':
                row['limitations']='日期是实际取得的业务参数/生命周期快照日，不是政策有效区间；公司实收、账户/用途差别及完整版本链未统一认证；不扩大准入。'
            if row['kind']=='turnover':row['limitations']+=' 成交额原值单位尚未统一认证，不直接跨源相加。'
            if row['route']=='cffex-monthly':row['limitations']+=' 明文HTTP不能认证发布者，CSV量额单位尚未核清。'
        datasets.sort(key=lambda r:(r['product'],r['kind'],r['route'],r.get('first') or ''))
        return dict(schema='project-data-inventory-v1',registered_at_utc=utc(when),datasets=datasets,
                    input_files=sorted(self.inputs,key=lambda r:(r['path'],r['sha256'])),evidence_files=self.evidence_index(),not_located=self.not_located,
                    strict_backtest_admission_expanded=False,new_market_requests=0,
                    completeness_scope='已登记真实路线：官方累计目录、EDB封存、新浪真实样本、历史日历和现场记录；原件当前不可读的资料另列。不是宣称已取得全市场全集，测试合成及迁移重复不计行情。')


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config',type=Path,required=True)
    parser.add_argument('--out',type=Path,required=True)
    args=parser.parse_args()
    if args.out.exists():raise ValueError('inventory_output_exists')
    config=json.loads(args.config.read_bytes())
    collector=Collector(Path(config['repo']))
    print('只读核官方累计目录',flush=True)
    collector.catalog(Path(config['official_catalog_manifest']))
    for entry in config.get('official_increments',[]):collector.catalog(Path(entry['manifest']),entry['route'])
    from reference_inventory import append_reference
    for entry in config.get('reference_snapshots',[]):append_reference(collector,entry)
    print('只读核中金所月包及早期样本',flush=True)
    for root in config.get('cffex_roots',[]):collector.cffex(Path(root))
    collector.cffex_fixture()
    print('只读核业务参数归档',flush=True)
    for entry in config.get('parameter_roots',[]):collector.parameters(Path(entry['path']),entry['route'],config['known_products'])
    print('只读核EDB与新浪原件',flush=True)
    for entry in config['edb_roots']:collector.edb(Path(entry['path']),entry['route'])
    for entry in config['sina_bodies']:collector.sina(Path(entry['path']),entry['exchange'],entry['native'],entry['evidence'],entry['route'])
    collector.profiles()
    if config.get('engine_snapshot'):
        from snapshot_inventory import append_snapshot
        append_snapshot(collector,config['engine_snapshot'],config['known_products'])
    for row in config.get('documented_datasets',[]):
        if row.get('file'):
            path=collector.repo/row['file'];raw=collector.read(path)
            if row['completeness']!='recorded_original_not_located' and hashlib.sha256(raw).hexdigest() not in row['evidence_shas']:
                raise ValueError('inventory_documented_sha')
    for path in config.get('additional_inputs',[]):collector.read(Path(path))
    collector.extra.extend(config.get('documented_datasets',[]))
    collector.not_located.extend(config.get('not_located',[]))
    result=collector.finish(config['registered_at_utc'])
    result['scope_review']=config.get('scope_review',{})
    if config.get('gap_review'):
        review=json.loads(collector.read(Path(config['gap_review'])))
        result['shared_documents']=review['notice_originals']
        # 补证报告是实际读取输入，也纳入本次集合；不把原件恢复失败改成成功。
        result['input_files']=sorted(collector.inputs,key=lambda r:(r['path'],r['sha256']))
    args.out.parent.mkdir(parents=True,exist_ok=True)
    args.out.write_text(json.dumps(result,ensure_ascii=False,indent=2)+'\n',encoding='utf-8',newline='\n')
    print(json.dumps(dict(datasets=len(result['datasets']),inputs=len(result['input_files']),evidence=len(result['evidence_files'])),ensure_ascii=False))


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    main()
