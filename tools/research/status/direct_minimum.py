"""读取供应商原始JSON明确开仓字段；不使用SDK默认值或普通最小报单补空。"""
import io
import sys
import datetime as dt
import gzip
import hashlib
import json
import re


def unique(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError('direct_minimum_duplicate_key')
        result[key] = value
    return result


def extract(raw, receipt, current_day, symbols):
    if (receipt.get('status') != 200 or receipt.get('body_complete') is not True or
            receipt.get('state') != 'received_raw_unqualified' or receipt.get('content_encoding') != 'gzip' or
            hashlib.sha256(raw).hexdigest() != receipt['sha256']):
        raise ValueError('direct_minimum_transport_or_identity')
    date = dt.datetime.fromisoformat(receipt['received_at']).astimezone(dt.timezone(dt.timedelta(hours=8)))
    if int(date.strftime('%Y%m%d')) != current_day or not (9 <= date.hour < 15):
        raise ValueError('direct_minimum_stale_snapshot')
    # 只在有界展开、完整JSON和原始键实际存在时读取，不实例化Quote对象。
    with gzip.GzipFile(fileobj=io.BytesIO(raw)) as stream:
        body = stream.read((768 << 20) + 1)
    if len(body) > 768 << 20:
        raise ValueError('direct_minimum_expanded_budget')
    rows = json.loads(body, object_pairs_hook=unique)
    result = {}
    for native, row in rows.items():
        if not isinstance(row, dict) or row.get('class') != 'FUTURE' or row.get('expired') is not False:
            continue
        exchange, product = row.get('exchange_id'), row.get('product_id')
        year, month = row.get('delivery_year'), row.get('delivery_month')
        if exchange not in {'SHFE','INE','CZCE','DCE','GFEX','CFFEX'} or not re.fullmatch('[A-Za-z]+',product or ''):
            continue
        if type(year) is not int or type(month) is not int or not (2000 <= year <= 2099 and 1 <= month <= 12):
            continue
        suffix = (str(year)[-1:] if exchange == 'CZCE' else str(year)[-2:]) + f'{month:02}'
        if native != exchange + '.' + product + suffix or row.get('instrument_id') != native:
            continue
        canonical = exchange + '.' + product + str(year)[-2:] + f'{month:02}'
        if canonical not in symbols:
            continue
        limit = row.get('open_min_limit_order_volume')
        market = row.get('open_min_market_order_volume')
        if type(limit) is not int or limit <= 0:
            continue
        if canonical in result:
            raise ValueError('direct_minimum_duplicate_contract')
        result[canonical] = dict(symbol=canonical, source_native=native, limit_min_open=limit,
                                 market_min_open=market if type(market) is int and market > 0 else None,
                                 ordinary_limit_min=row.get('min_limit_order_volume'),
                                 observation_day=current_day, retrieved_at=receipt['received_at'],
                                 source_sha256=receipt['sha256'])
    return result


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    raise SystemExit('仅供离线提取明确字段，不发起采集或升级数据门槛')
