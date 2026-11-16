"""郑商所日结算参数表的独立只读解析；日期不等于次日生效证明。"""
import io
import sys
import datetime as dt
from decimal import Decimal, InvalidOperation
import re
import hashlib
import json
from pathlib import Path


def parse(raw, day):
    date = dt.datetime.strptime(str(day), '%Y%m%d').strftime('%Y-%m-%d')
    lines = [line.strip() for line in raw.decode('utf-8-sig').splitlines() if line.strip()]
    if not lines or lines[0] != '郑州商品交易所期货结算参数表(' + date + ')':
        raise ValueError('clear_title_or_day')
    if len(lines) < 3:
        raise ValueError('clear_empty')
    headers = [v.strip() for v in lines[1].split('|')]
    required = {'合约代码', '当日结算价', '交易保证金率(%)'}
    if len(headers) != len(set(headers)) or not required <= set(headers):
        raise ValueError('clear_headers')
    rows = []
    seen = set()
    for line in lines[2:]:
        values = [v.strip() for v in line.split('|')]
        if len(values) != len(headers):
            raise ValueError('clear_columns')
        row = dict(zip(headers, values))
        symbol = row['合约代码']
        if not re.fullmatch(r'[A-Z]+\d{3}', symbol) or symbol in seen:
            raise ValueError('clear_contract_identity')
        seen.add(symbol)
        try:
            rate = Decimal(row['交易保证金率(%)'].replace(',', '')) / 100
            settle = Decimal(row['当日结算价'].replace(',', ''))
            if not rate.is_finite() or not settle.is_finite() or not 0 < rate <= 1 or settle <= 0:
                raise ValueError('clear_invalid_numeric')
        except InvalidOperation as error:
            raise ValueError('clear_invalid_numeric') from error
        rows.append(dict(source_native=symbol, source_snapshot_day=day,
                         margin_ratio=str(rate), settlement=str(settle),
                         as_of_trade_day=None, effective_min_open_lots=None))
    return rows


def append_inventory(collector, spec):
    """只读成功原件；将单列保证金当作参考比例，明确不认证下一日实际参数。"""
    root = Path(spec['root'])
    review = json.loads(collector.read(Path(spec['review'])))
    receipt = json.loads(collector.read(root / 'receipt.json'))
    raw = collector.read(root / 'original.body')
    if (receipt != review['source'] or receipt['state'] != 'parsed' or
            len(raw) != receipt['bytes'] or hashlib.sha256(raw).hexdigest() != receipt['sha256']):
        raise ValueError('clear_inventory_source_binding')
    rows = parse(raw, review['source_snapshot_day'])
    if len(rows) != review['rows'] or review['margin_difference'] or review['settlement_difference']:
        raise ValueError('clear_inventory_review')
    for row in rows:
        collector.stats.add('czce-clear-parameters', '郑商所日结算参数原表', dict(
            exchange='CZCE', native=row['source_native'], day=row['source_snapshot_day'],
            fields=dict(spec_long_margin=row['margin_ratio'], spec_short_margin=row['margin_ratio'],
                        settle=row['settlement']),
            evidence=dict(sha256=receipt['sha256'], retrieved_at=receipt['retrieved_at'])))
    for (_, route, _), group in collector.stats.groups.items():
        if route == 'czce-clear-parameters':
            group['limitations'] += ' 单列参考比例，非多空分列或公司实付；未证次日生效，实际最低开仓未知。'


if __name__ == "__main__":
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    raise SystemExit("由数据维护工具导入，不联网")
