"""商品价格交付的纯校验：显式年月、原精度、冲突隔离和真实覆盖。"""
import datetime as dt
from decimal import Decimal, InvalidOperation
import io
import re
import sys


def canonical(source, aliases):
    value = aliases.get(source)
    if not value or not re.fullmatch(r'(SHFE|INE|CZCE|DCE|GFEX)\.[A-Za-z]+[0-9]{4}', value):
        raise ValueError('commodity_explicit_contract_binding:' + source)
    match=re.fullmatch(r'([A-Z]+\.[A-Za-z]+)([0-9]{3,4})',source)
    if not match or value[:-4]!=match[1] or not value.endswith(match[2]):
        raise ValueError('commodity_explicit_contract_binding:' + source)
    return value


def normalize(row):
    """仅基本无效OHLC隔离；数量畸形、时间或非有限值直接拒绝。"""
    try:
        values = {k: Decimal(row[k].replace(',', '')) for k in ('open', 'high', 'low', 'close', 'volume', 'close_oi')}
        if not all(v.is_finite() for v in values.values()):
            raise ValueError('commodity_nonfinite')
        if any(values[k] < 0 or values[k] != values[k].to_integral_value() for k in ('volume', 'close_oi')):
            raise ValueError('commodity_quantity')
        stamp = int(row['datetime_nano'])
        if stamp % 1_000_000_000:
            raise ValueError('commodity_timestamp_precision')
        day = int(dt.datetime.fromtimestamp(stamp // 1_000_000_000, dt.timezone.utc).astimezone(
            dt.timezone(dt.timedelta(hours=8))).strftime('%Y%m%d'))
    except (KeyError, InvalidOperation, OverflowError) as error:
        raise ValueError('commodity_csv_numeric') from error
    o, h, l, c = (values[k] for k in ('open', 'high', 'low', 'close'))
    if not 0 < l <= min(o, c) <= max(o, c) <= h:
        return None
    fields = {k: format(values[k], 'f') for k in ('open', 'high', 'low', 'close', 'volume')}
    fields.update(open_interest=format(values['close_oi'], 'f'), settle=None, turnover=None)
    return day, fields


def official_fields(raw):
    """仅移除官方展示用千分位，不舍入或把未知字段补零。"""
    fields={}
    for key in ('open','high','low','close','volume','open_interest','settle','turnover'):
        literal=raw.get(key)
        if literal in (None,''):
            fields[key]=None
            continue
        value=Decimal(str(literal).replace(',',''))
        if not value.is_finite():
            raise ValueError('commodity_official_nonfinite')
        fields[key]=format(value,'f')
    try:
        o,h,l,c=(Decimal(fields[k]) for k in ('open','high','low','close'))
    except (TypeError,InvalidOperation):
        return None
    return fields if 0<l<=min(o,c)<=max(o,c)<=h else None


class UniquePrices:
    """同值明确计数去重；异值保留全部来源并隔离该日，不静默选版本。"""
    def __init__(self):
        self.rows = {}
        self.conflicts = {}
        self.duplicates = 0

    def add(self, record):
        key = record['symbol'], record['day']
        if key in self.conflicts:
            self.conflicts[key].append(record)
        elif key not in self.rows:
            self.rows[key] = record
        elif self.rows[key]['fields'] == record['fields']:
            self.duplicates += 1
        else:
            self.conflicts[key] = [self.rows.pop(key), record]

    def product(self, key):
        return sorted((r for r in self.rows.values() if r['product_key'] == key), key=lambda r: (r['day'], r['symbol']))


def coverage(rows, listing, target):
    dates = {r['day'] for r in rows}
    baseline = max(2021, listing['day'] // 10000) if listing else 2021
    years = {str(y): len({d for d in dates if d // 10000 == y}) for y in range(baseline, target // 10000 + 1)}
    start = listing['day'] if listing and baseline > 2021 else 20211231
    if not dates or min(dates) > start or max(dates) > target or any(n == 0 for n in years.values()):
        raise ValueError('commodity_history_year_or_listing')
    return dict(first=min(dates), last=max(dates), listing_day=listing['day'] if listing else None,
                valid_days_by_year=years)


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    raise SystemExit('由真实交付生成器调用；不联网')
