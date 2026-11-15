"""离线核公开接入文档的具体示例；不使用API Key、不取行情、不判断法律许可成立。"""
import argparse
import datetime as dt
import io
import json
import re
import sys
from html.parser import HTMLParser
from pathlib import Path

from review import document, load_locked


class Links(HTMLParser):
    def __init__(self):
        super().__init__()
        self.targets = set()

    def handle_starttag(self, tag, attrs):
        if tag == 'a':
            self.targets.add(dict(attrs).get('href', ''))


def example_time(current):
    """只算某个文档示例的时间差，不据此认证供应商所有市场/时期的时间语义。"""
    stamp = dt.datetime.fromtimestamp(current['timestamp'], dt.timezone.utc)
    displayed = dt.datetime.fromisoformat(current['datetime'])
    if displayed.tzinfo is not None:
        raise ValueError('expected_naive_document_datetime')
    offset = int((displayed - stamp.replace(tzinfo=None)).total_seconds())
    bucket = dt.datetime.combine(displayed.date(), dt.time.fromisoformat(current['minute']))
    return dict(epoch_utc=stamp.isoformat(), displayed=current['datetime'],
                implied_offset_seconds=offset, consistent_with_utc8=(offset == 28800),
                minute_label=current['minute'], label_minus_event_seconds=int((bucket - displayed).total_seconds()),
                timestamp_semantics_certified=False, closed_bar_certified=False)


def access_review(home, historical, realtime):
    docs = [document(raw) for raw in (home, historical, realtime)]
    text = [re.sub(r'\s+', '', ''.join(doc.parts)) for doc in docs]
    if 'x-api-key' not in text[1] or 'POST' not in text[1] or '/future-contract/minute' not in text[1]:
        raise ValueError('historical_access_schema')
    examples = []
    for block in docs[2].blocks:
        try:
            obj = json.loads(block)
        except (ValueError, TypeError):
            continue  # 非JSON代码块保留为文档，不执行它。
        if isinstance(obj, dict) and isinstance(obj.get('data'), list):
            examples.extend(row for row in obj['data'] if isinstance(row, dict) and row.get('symbol') == 'stock.sz.000001' and 'current' in row)
    if len(examples) != 1:
        raise ValueError('realtime_document_example')
    current = examples[0]['current']
    times = example_time(current)
    field_table = text[2].split('K线数据字段说明', 1)
    if len(field_table) != 2:
        raise ValueError('realtime_field_table')
    field_table = field_table[1].split('响应压缩', 1)[0]
    links = Links()
    for raw in (home, historical, realtime):
        links.feed(raw.decode('utf-8', errors='strict'))
    terms = sorted(h for h in links.targets if re.search(r'license|terms|privacy|agreement|copyright|许可|协议|条款', h, re.I))
    return dict(schema='vendor-access-document-review-v1',
                historical_api=dict(key_required=True, method='POST', actual_queries=0, access_grant_verified=False),
                inspected_legal_link_candidates=terms, data_use_grant_verified=False,
                redistribution_grant_verified=False, license_conclusion='not_established_by_inspected_public_documents',
                realtime_example=dict(asset='stock.sz.000001', origin='documentation_example_not_market_capture',
                                      current_field=True, time=times,
                                      example_money_key=('money' in current),
                                      table_amount_key=('amountfloat当前成交额' in field_table)),
                futures_time_units=dict(timezone=None, minute_start_or_end=None,
                                        volume_unit=None, turnover_currency_and_scale=None,
                                        increment_or_cumulative=None),
                concrete_contract_sample_obtained=False, official_reconciliation='not_performed_no_licensed_raw_sample',
                new_verified_profiles=0, financial_admission_expanded=False)


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--lock', required=True, type=Path)
    p.add_argument('--root', required=True, type=Path)
    p.add_argument('--out', required=True, type=Path)
    args = p.parse_args()
    lock = json.loads(args.lock.read_text(encoding='utf-8'))
    raw = {entry['id']: load_locked(args.root, entry) for entry in lock['sources']}
    result = access_review(raw['vendor-home'], raw['vendor-history-api'], raw['vendor-realtime-api'])
    args.out.write_text(json.dumps(result, ensure_ascii=False, indent=2) + '\n', encoding='utf-8', newline='\n')


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    main()
