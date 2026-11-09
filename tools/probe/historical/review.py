"""独立复核Go真实样本的原件、十进制附件与Feed值；不导入Go/下游解析器。"""
import argparse
from decimal import Decimal
import hashlib
import io
import json
from pathlib import Path
import sys


def main():
    p = argparse.ArgumentParser(description=__doc__); p.add_argument('pipeline', type=Path); p.add_argument('output', type=Path); args = p.parse_args()
    report = json.loads((args.pipeline/'report.json').read_text(encoding='utf-8')); checks, bar_checks, days = 0, 0, 0
    mapping = dict(open='OPENPRICE', high='HIGHESTPRICE', low='LOWESTPRICE', close='CLOSEPRICE', settle='SETTLEMENTPRICE', pre_settle='PRESETTLEMENTPRICE', volume='VOLUME', open_interest='OPENINTEREST')
    chinese = dict(open='今开盘', high='最高价', low='最低价', close='今收盘', settle='今结算', pre_settle='昨结算', volume='成交量(手)', open_interest='持仓量')
    for sample in report['samples']:
        for record, bar in zip(sample['records'], sample['bars'], strict=True):
            ref = record['evidence']; raw = (args.pipeline/'archive'/(ref['id']+'.body')).read_bytes()
            assert hashlib.sha256(raw).hexdigest() == ref['sha256']
            if record['exchange'] == 'CZCE':
                lines = raw.decode('utf-8-sig').splitlines(); header = [v.strip() for v in lines[1].split('|')]
                line = next(l for l in lines[2:] if l.split('|')[0].strip() == record['native'])
                source = dict(zip(header, (v.strip() for v in line.split('|')), strict=True)); names = chinese
            else:
                doc = json.loads(raw); contract = record['native']
                assert doc['report_date'] == str(record['day'])
                source = next(r for r in doc['o_curinstrument'] if str(r.get('PRODUCTID','')).strip() == contract[:-4]+'_f' and str(r.get('DELIVERYMONTH','')).strip() == contract[-4:]); names = mapping
            for field, name in names.items():
                expected = Decimal(str(source[name]).replace(',', ''))
                assert Decimal(record['fields'][field].replace(',', '')) == expected
                checks += 1
                if field != 'pre_settle':
                    assert Decimal(str(bar[field])) == expected
                    bar_checks += 1
            assert int(bar['day']) == record['day'] and bar['flags'] == 16
            if record['exchange'] != 'CZCE':
                assert bar['turnover'] is None
            days += 1
        assert sample['rounds'][0]['Bars'] == len(sample['bars']) and sample['rounds'][1]['Bars'] == 0
    result = dict(status='passed', contract_days=days, exact_decimal_field_checks=checks, feed_numeric_checks=bar_checks,
                  network_requests=report['network_requests'], scope='raw numeric/source identity reconciliation; not calendar/roster/exchange timing certification')
    args.output.write_text(json.dumps(result, indent=2)+'\n', encoding='utf-8', newline='\n'); print(json.dumps(result))


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    main()
