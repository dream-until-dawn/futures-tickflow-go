"""独立回读日期绑定的Contracts原件；只登记快照，不推定完整生命周期。"""
import datetime as dt
import hashlib
import io
import json
from pathlib import Path
import re
import sys
import xml.etree.ElementTree as ET


def compare(raw, review):
    if hashlib.sha256(raw).hexdigest() != review['body_sha256']:
        raise ValueError('reference_raw_sha')
    root = ET.fromstring(raw)
    if root.tag != 'Contracts':
        raise ValueError('reference_root')
    actual = {}
    for node in root.findall('Contract'):
        if node.findtext('PrdTp') != '期货':
            continue
        code = node.findtext('PrdCd')
        year, month = int(node.findtext('YrCd')), int(node.findtext('MnthCd'))
        # 显式年月绑定三位原生码；不采用最近十年猜测。
        dt.date(year, month, 1)
        native = node.findtext('CtrCd')
        if native != code + str(year % 10) + f'{month:02d}':
            raise ValueError('reference_native_year')
        canonical = 'CZCE.' + code + f'{year % 100:02d}{month:02d}'
        def day(tag):
            value = node.findtext(tag)
            return int(dt.date.fromisoformat(value).strftime('%Y%m%d')) if value and value != 'n.a.' else 0
        if canonical in actual:
            raise ValueError('reference_duplicate')
        actual[canonical] = (native, day('FrstTrdDt'), day('LstTrdDt'))
    expected = {r['canonical']: (r['native'], r['listed_declared'], r['last_trading_declared']) for r in review['entries']}
    if not actual or len(expected) != len(review['entries']) or actual != expected:
        raise ValueError('reference_independent_mismatch')
    if review['financial_admission_added'] != 0:
        raise ValueError('reference_admission')
    return actual


def append_reference(collector, entry):
    root = Path(entry['root'])
    raw = collector.read(root / 'reference.body')
    receipt_raw = collector.read(root / 'receipt.json')
    review = json.loads(collector.read(Path(entry['review'])))
    receipt = json.loads(receipt_raw)
    if hashlib.sha256(receipt_raw).hexdigest() != review['receipt_sha256']:
        raise ValueError('reference_receipt_sha')
    if not receipt['http']['complete'] or receipt['http']['status'] != 200 or receipt['bytes'] != len(raw):
        raise ValueError('reference_incomplete')
    match = re.fullmatch(r'https://www\.czce\.com\.cn/cn/DFSStaticFiles/Future/(\d{4})/(\d{8})/FutureDataReferenceData\.xml', receipt['url'])
    if (not match or match[1] != match[2][:4] or int(match[2]) != review['observed_day']
            or review['requested_at'] != receipt['http']['requested_at']
            or receipt['sha256'] != review['body_sha256']
            or any(row['observed_day'] != review['observed_day'] for row in review['entries'])):
        raise ValueError('reference_date_or_identity')
    if receipt['state'] != 'received_original_scope_unreviewed' and not (
            receipt['state'] == 'failed' and receipt['error'] == '不是已知FIXML参考快照'):
        raise ValueError('reference_failure_not_promotable')
    compare(raw, review)
    for row in review['entries']:
        fields = {key: value or None for key, value in
                  [('listing_day', row['listed_declared']), ('expiry_day', row['last_trading_declared'])]}
        collector.stats.add('czce-reference-snapshots', '郑商所（日期绑定参考原件）',
            dict(exchange='CZCE', native=row['native'], day=review['observed_day'], fields=fields,
                 evidence=dict(sha256=review['body_sha256'], retrieved_at=review['requested_at'])))


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    raise SystemExit('由只读盘点入口调用，不联网')
