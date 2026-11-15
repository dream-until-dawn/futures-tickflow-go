"""只读冻结网页/收据，区分转载、供应商示例与获证资料；不联网、不生成日历。"""
import argparse
import csv
import hashlib
import io
import json
import re
import sys
from html.parser import HTMLParser
from pathlib import Path


class Document(HTMLParser):
    """保留文档代码块原文，忽略脚本/样式；不执行网页代码。"""
    def __init__(self):
        super().__init__()
        self.ignored = 0
        self.pre = False
        self.parts, self.blocks, self.current = [], [], []

    def handle_starttag(self, tag, attrs):
        if tag in ('script', 'style'):
            self.ignored += 1
        if tag == 'pre':
            self.pre, self.current = True, []

    def handle_endtag(self, tag):
        if tag in ('script', 'style'):
            self.ignored = max(0, self.ignored - 1)
        if tag == 'pre' and self.pre:
            self.blocks.append(''.join(self.current))
            self.pre = False

    def handle_data(self, data):
        if not self.ignored:
            self.parts.append(data)
            if self.pre:
                self.current.append(data)


def document(raw, encoding='utf-8'):
    reader = Document()
    reader.feed(raw.decode(encoding, errors='strict'))
    return reader


# 条款固定为本轮实际核读的转载正文，不从报价日期或政府调休反推。
HOLIDAYS = [
    ('元旦', '2023年12月30日（星期六）至2024年1月1日（星期一）休市', '1月2日（星期二）起照常开市', '2023年12月29日（星期五）晚上不进行夜盘交易'),
    ('春节', '2月9日（星期五）至2月17日（星期六）休市', '2月19日（星期一）起照常开市', '2月8日（星期四）晚上不进行夜盘交易'),
    ('清明', '4月4日（星期四）至4月6日（星期六）休市', '4月8日（星期一）起照常开市', '4月3日（星期三）晚上不进行夜盘交易'),
    ('劳动', '5月1日（星期三）至5月5日（星期日）休市', '5月6日（星期一）起照常开市', '4月30日（星期二）晚上不进行夜盘交易'),
    ('端午', '6月8日（星期六）至6月10日（星期一）休市', '6月11日（星期二）起照常开市', '6月7日（星期五）晚上不进行夜盘交易'),
    ('中秋', '9月15日（星期日）至9月17日（星期二）休市', '9月18日（星期三）起照常开市', '9月13日（星期五）晚上不进行夜盘交易'),
    ('国庆', '10月1日（星期二）至10月7日（星期一）休市', '10月8日（星期二）起照常开市', '9月30日（星期一）晚上不进行夜盘交易'),
]


def annual_review(raw):
    text = re.sub(r'\s+', '', ''.join(document(raw).parts))
    if '2023-12-27' not in text or '2023年12月26日' not in text:
        raise ValueError('annual_publication_dates')
    for name, *clauses in HOLIDAYS:
        if any(clause not in text for clause in clauses):
            raise ValueError('annual_clause:' + name)
    return dict(identity='broker_reprint', page_date='2023-12-27', notice_date='2023-12-26',
                holidays_checked=7, calendar_qualified=False,
                pending=['exchange-original', 'applicable-rule-continuity', 'detailed-resumption-notices', '2025-year-boundary'])


MINUTE_HEADER = 'exchange,code,symbol,trading_date,datetime,pre_close,open,high,low,close,volume,total_turnover,open_interest'


def vendor_review(raw):
    doc = document(raw)
    # 只认实际代码块，不从搜索摘要/脚本里的同名词构造样本。
    minute_blocks = [b for b in doc.blocks if b.splitlines() and b.splitlines()[0] == MINUTE_HEADER]
    contract = [b for b in minute_blocks if ',A2605,' in b]
    main = [b for b in minute_blocks if ',A88,' in b]
    if len(contract) != 1 or len(main) != 1:
        raise ValueError('vendor_contract_and_main_examples')
    rows = list(csv.DictReader(io.StringIO(contract[0])))
    if len(rows) != 2 or any(r['exchange'] != 'DCE' or r['code'] != 'A2605' for r in rows):
        raise ValueError('vendor_concrete_contract')
    if [r['datetime'] for r in rows] != ['2026-03-12 21:01:00', '2026-03-12 21:02:00'] or any(r['trading_date'] != '2026-03-13' for r in rows):
        raise ValueError('vendor_night_trading_date')
    ticks = [b for b in doc.blocks if b.startswith('symbol,code,name,time,')]
    if len(ticks) != 1:
        raise ValueError('vendor_tick_example')
    tick_rows = list(csv.DictReader(io.StringIO(ticks[0])))
    if len(tick_rows) != 2 or tick_rows[0]['time'] != tick_rows[1]['time']:
        raise ValueError('vendor_tick_same_second')
    text = ''.join(doc.parts)
    if 'A99' not in text or '豆一指数连续' not in text or 'A888' not in text or '价差平滑' not in text:
        raise ValueError('vendor_synthetic_series_identity')
    return dict(identity='vendor_documentation_examples_only', market_dataset_obtained=False,
                contract_example_rows=rows, same_second_tick_rows=len(tick_rows),
                concrete_minute_example_code='A2605', synthetic_codes=['A99', 'A88', 'A888'],
                timestamp_convention_certified=False, volume_amount_units_certified=False,
                historical_roster_certified=False, license_certified=False,
                calendar_qualified=False, financial_eligible=False,
                pending=['raw-contract-samples-and-universe', 'timestamp-start-or-end-timezone',
                         'volume-increment-or-cumulative-and-units', 'license-and-redistribution',
                         'independent-exchange-reconciliation', 'real-calendar-and-lifecycle'])


def cy_review(raw):
    # 响应明确声明gb18030；严格解码既有字节，不重发请求、不改原失败收据。
    text = re.sub(r'\s+', '', ''.join(document(raw, 'gb18030').parts))
    for value in ('2019年12月18日交易提示', '2019-12-1716:30', '自2019年12月11日当晚起调整夜盘交易时间', '全部夜盘交易品种夜盘交易时间为21:00-23:00'):
        if value not in text:
            raise ValueError('cy_change_clause:' + value)
    return dict(identity='broker_client_reminder', page_date='2019-12-17 16:30',
                title_trading_day=20191218, asserted_effective_evening=20191211,
                night_hours='21:00-23:00', original_receipt_state='failed',
                offline_recovery='strict_gb18030_from_complete_archived_response',
                exchange_notice_obtained=False, full_product_scope_certified=False,
                calendar_qualified=False, financial_eligible=False)


def load_locked(root, entry):
    """源锁由独立Git索引提供；partial/错URL/原字节变化不能借重算收据过关。"""
    raw = (root / (entry['id'] + '.body')).read_bytes()
    receipt = json.loads((root / (entry['id'] + '.json')).read_text(encoding='utf-8'))
    http = receipt.get('http', {})
    ref = receipt['evidence']
    if len(raw) != entry['bytes'] or hashlib.sha256(raw).hexdigest() != entry['sha256']:
        raise ValueError('locked_source_bytes')
    if ref['sha256'] != entry['sha256'] or ref['url'] != entry['url'] or ref['id'] != entry['id'] or receipt['bytes'] != len(raw):
        raise ValueError('receipt_identity')
    if receipt['body_state'] != 'complete' or http.get('status') != 200 or not http.get('complete') or http.get('final_url') != entry['url']:
        raise ValueError('source_not_complete_200')
    if receipt['state'] != 'received' and not (entry['id'] == 'cy-night-change' and receipt['state'] == 'failed' and receipt.get('error') == '未取得预期业务正文，停止该主机' and http.get('content_type') == 'text/html;charset=gb18030'):
        raise ValueError('source_not_received')
    return raw


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--lock', required=True, type=Path)
    parser.add_argument('--first-root', required=True, type=Path)
    parser.add_argument('--cy-root', required=True, type=Path)
    parser.add_argument('--out', required=True, type=Path)
    args = parser.parse_args()
    lock = json.loads(args.lock.read_text(encoding='utf-8'))
    facts = {}
    for entry in lock['reviewable']:
        key = entry['id']
        root = args.cy_root if key == 'cy-night-change' else args.first_root
        raw = load_locked(root, entry)
        reviewer = {'czce-year2024-reprint': annual_review, 'zhunda-docs': vendor_review, 'cy-night-change': cy_review}[key]
        facts[key] = reviewer(raw)
    result = dict(schema='history-source-offline-review-v1', facts=facts, market_requests=0,
                  new_verified_profiles=0, financial_admission_expanded=False)
    args.out.write_text(json.dumps(result, ensure_ascii=False, indent=2) + '\n', encoding='utf-8', newline='\n')


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    main()
