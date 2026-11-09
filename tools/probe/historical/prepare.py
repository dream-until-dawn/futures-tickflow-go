"""只读既有下游原件，生成冻结探针输入；不导入下游解析器、不发网络请求。"""
import argparse
import hashlib
import json
import io
import sys
from datetime import datetime
from decimal import Decimal
from pathlib import Path


def sha(path):
    h = hashlib.sha256()
    with path.open('rb') as f:
        for chunk in iter(lambda: f.read(1024 * 1024), b''):
            h.update(chunk)
    return h.hexdigest()


def read(path):
    return json.loads(path.read_text(encoding='utf-8-sig'))


def checked(root, receipt):
    h = receipt['sha256']
    path = root / 'raw' / h[:2] / (h + '.body')
    assert sha(path) == h, str(path)
    assert path.stat().st_size == receipt['bytes'], str(path)
    return path


def ref(receipt):
    return dict(id=receipt['id'], url=receipt['url'], sha256=receipt['sha256'],
                retrieved_at=receipt.get('at_utc') or receipt['at'])


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--downstream', type=Path, required=True)
    parser.add_argument('--out', type=Path, required=True)
    args = parser.parse_args()
    root, out = args.downstream, args.out
    out.mkdir(parents=True, exist_ok=False)
    archive = root / 'results/all-market-daily/archive-v1'
    review_path = root / 'results/all-market-daily/catalog-v1/review.json'
    review = read(review_path)
    verified = []
    # 流式核验所有声称已收到字节的收据，不把failed状态升级为解析成功。
    for p in sorted((archive / 'receipts').glob('*.json')):
        r = read(p)
        if r.get('sha256') and r.get('bytes', 0):
            raw = checked(archive, r)
            verified.append(dict(id=r['id'], sha256=r['sha256'], bytes=r['bytes'], state=r['state']))
    total = 0
    partitions = []
    for part in review['partitions']:
        p = review_path.parent / part['path']
        assert sha(p) == part['sha256']
        n = sum(1 for _ in p.open(encoding='utf-8'))
        assert n == part['rows']
        total += n
        partitions.append(dict(path=part['path'], rows=n, sha256=part['sha256']))
    assert total == review['contract_days']
    docs, xmls = [], []
    for day in ['20261102', '20261103', '20261104']:
        for prefix, fmt in [('shfe', 'shfe-json'), ('czce', 'czce-txt')]:
            r = read(archive / 'receipts' / f'{prefix}-daily-{day}.json')
            path = checked(archive, r)
            docs.append(dict(path=str(path), format=fmt, day=int(day), evidence=ref(r)))
    binding = read(root / 'results/upstream-dataflow-6d3e4f2/official-crosscheck.json')['reference_binding']
    for day, b in binding.items():
        p = root / b['file']
        assert sha(p) == b['sha256']
        # 取得时刻只能从原始收据读，不能用本次文件修改时刻替代。
        if day == '20261102':
            records = [json.loads(l) for l in (root / 'results/phase8-market-complete/collection.jsonl').read_text(encoding='utf-8').splitlines() if l.strip()]
        else:
            records = read(root / 'results/phase9-probes/rm-latest/requests.json')
        r = next(r for r in records if r['id'] == f'reference-{day}' and r.get('sha256') == b['sha256'])
        if not r.get('at') and not r.get('at_utc'):
            # 只沿收据明确的父关联追溯；父原件逐字节一致才继承真实取得时刻。
            parent_root = root / r['reused_from_directory'].replace('\\', '/')
            parent = next(x for x in read(parent_root / 'requests.json') if x['id'] == r['reused_from_id'])
            assert parent['url'] == r['url'] and parent['sha256'] == r['sha256'] and parent['bytes'] == r['bytes']
            parent_path = parent_root / parent['file']
            assert parent_path.read_bytes() == p.read_bytes() and sha(parent_path) == r['sha256']
            r = parent
        xmls.append(dict(path=str(p), day=int(day), evidence=ref(r)))
    calendar = root / 'results/phase3-data/calendar-extended.json'
    cal = read(calendar)
    extension = archive / 'plan.json'
    assert sha(calendar) in read(extension)['calendar']['source_files'].values()
    data = dict(documents=docs, references=xmls, calendar_path=str(calendar), calendar_sha256=sha(calendar),
                extension_path=str(extension), extension_sha256=sha(extension),
                products=[dict(Exchange=x['exchange'], Product=x['product']) for x in review['products']],
                now='2026-11-05T00:00:00Z', from_day=20261102, to_day=20261104,
                calendar_status=cal['status'], symbols=['SHFE.rb2701', 'INE.sc2701', 'CZCE.RM2701'])
    (out / 'input.json').write_text(json.dumps(data, ensure_ascii=False, indent=2) + '\n', encoding='utf-8', newline='\n')
    coverage = read(root / 'results/upstream-dataflow-6d3e4f2/output/run/calendar-coverage.json')
    audit = dict(scope='local hashes and counts verified; not roster/calendar certification',
                 network_requests=0, receipts_with_bytes_verified=len(verified), receipt_states=verified,
                 partitions=partitions, contract_days=total,
                 products=[dict(product=x['exchange']+'.'+x['product'], observed_first=x['first'], observed_last=x['last'], historical_session_certified=False) for x in review['products']],
                 reported_embedded_rejections=[x['product'] for x in coverage if not x['covers']],
                 source_states=review['source_states'], calendar_sha256=sha(calendar),
                 gaps=[], acquisition_parent_chain='20261102 XML linked to phase8-discovery/requests.json; URL/SHA/bytes/body independently equal')
    # 独立Python逐字段对照，不导入下游或本仓Go解析器。
    differences, checks = [], 0
    columns = dict(open='OPENPRICE', high='HIGHESTPRICE', low='LOWESTPRICE', close='CLOSEPRICE',
                   settle='SETTLEMENTPRICE', volume='VOLUME', open_interest='OPENINTEREST')
    for market, contract, sample in [('SHFE', 'rb2701', 'rb-recent'), ('INE', 'sc2701', 'sc-recent'), ('CZCE', 'RM701', 'rm-recent')]:
        base = root / 'results/upstream-dataflow-6d3e4f2/output/run' / sample
        raw = (base / 'http-01.body').read_bytes()
        assert hashlib.sha256(raw).hexdigest() == read(base / 'http-01.json')['SHA256']
        text = raw.decode('utf-8'); payload = json.loads(text[text.index('(')+1:text.rindex(')')])
        for day in ['20261102', '20261103', '20261104']:
            vendor = next(r for r in payload if r['d'].replace('-', '') == day)
            prefix = 'czce' if market == 'CZCE' else 'shfe'
            receipt = read(archive/'receipts'/f'{prefix}-daily-{day}.json')
            body = checked(archive, receipt).read_bytes()
            if market == 'CZCE':
                lines = body.decode('utf-8-sig').splitlines()
                header = [v.strip() for v in lines[1].split('|')]
                values = next(l for l in lines[2:] if l.split('|')[0].strip() == contract)
                official = dict(zip(header, (v.strip() for v in values.split('|'))))
                mapping = dict(open='今开盘', high='最高价', low='最低价', close='今收盘', settle='今结算', volume='成交量(手)', open_interest='持仓量')
            else:
                source = json.loads(body)
                official = next(r for r in source['o_curinstrument'] if str(r.get('PRODUCTID', '')).strip() == contract[:-4]+'_f' and str(r.get('DELIVERYMONTH', '')).strip() == contract[-4:])
                mapping = columns
            for key, vk in dict(open='o', high='h', low='l', close='c', settle='s', volume='v', open_interest='p').items():
                a, b = Decimal(vendor[vk]), Decimal(str(official[mapping[key]]).replace(',', ''))
                checks += 1
                if a != b:
                    differences.append(dict(exchange=market, contract=contract, day=day, field=key, supplier=str(a), official=str(b)))
    audit['independent_supplier_comparison'] = dict(checks=checks, differences=differences, scope='raw native keys and decimal values; not lifecycle/calendar certification')
    (out / 'local-audit.json').write_text(json.dumps(audit, ensure_ascii=False, indent=2) + '\n', encoding='utf-8', newline='\n')
    print(json.dumps(dict(verified_files=len(verified), partitions=len(partitions), contract_days=total, input=str(out/'input.json')), ensure_ascii=False))


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    main()
