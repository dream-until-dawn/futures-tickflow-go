"""解析公开公司临交割比例参考；不把结算信息表标题当成存在结算价。"""
import hashlib
from html import unescape
import io
import json
import re
import sys

from broker_margin import Table, expand, ratio
from inventory_model import product


URL = 'https://www.xzfutures.com/article/10126701/index.html'
MARKETS = {'大商所': 'DCE', '能源中心': 'INE', '上期所': 'SHFE', '郑商所': 'CZCE',
           '广期所': 'GFEX', '中金所': 'CFFEX'}


def parse(raw, receipt, source_day):
    if (receipt.get('url') != URL or receipt.get('state') != 'received_scope_unreviewed' or
            receipt.get('sha256') != hashlib.sha256(raw).hexdigest() or
            receipt.get('http', {}).get('status') != 200 or receipt.get('http', {}).get('complete') is not True):
        raise ValueError('broker_reference_source_identity')
    text = raw.decode('utf-8')
    title = re.search(r'<title[^>]*>(.*?)</title>', text, re.S)
    expected = f'{source_day//10000}年{source_day//100%100}月{source_day%100}日交易结算信息表'
    if not title or unescape(title[1]).strip() != expected:
        raise ValueError('broker_reference_title_day')
    # 仅解码已保存的字面量，不执行网页脚本或跟随页面中的请求。
    literals = re.findall(r'F\.content=("(?:\\.|[^"\\])*")', text)
    if len(literals) != 1:
        raise ValueError('broker_reference_content_literal')
    table = Table()
    table.feed(json.loads(literals[0]))
    grid = expand(table.rows)
    if len(grid) < 3 or grid[:2] != [['交易所', '合约', '保证金比例', '保证金比例', '备注'],
                                    ['交易所', '合约', '投机', '套保', '备注']]:
        raise ValueError('broker_reference_header')
    rows, seen = [], set()
    for row in grid[2:]:
        if len(row) != 5 or row[0] not in MARKETS or not re.fullmatch(r'[A-Za-z]+[0-9]{3,4}', row[1]):
            raise ValueError('broker_reference_row')
        exchange, native = MARKETS[row[0]], row[1]
        if (exchange, native) in seen:
            raise ValueError('broker_reference_duplicate')
        seen.add((exchange, native))
        rows.append(dict(product_key=product(exchange, native), source_native=native,
                         company_spec_ratio=ratio(row[2]), company_hedge_ratio=ratio(row[3]),
                         remark=row[4], source_snapshot_day=source_day, effective_trade_day=None))
    return rows


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    raise SystemExit('由离线参考归档调用，不联网、不更新当前硬项')
