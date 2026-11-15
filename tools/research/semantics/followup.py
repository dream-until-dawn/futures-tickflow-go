"""只从固定旧细则与SDK版本原件复核有限事实，不替供应商填历史版本或事件流水。"""
import argparse
import hashlib
import io
import json
from pathlib import Path
import re
import sys

RULE_SHA = '7db358acc1123c4cad4d71ee5133d4a184a3480bd5b12e8ad6fc9b576014ea9b'
SDK_BLOB = 'cb271caffa01166045314e88a3d9e95ca4a0901f'


def prior_rule_facts(pages):
    if len(pages) != 11:
        raise ValueError('prior_rule_pages')
    text = re.sub(r'\s+', '', '\n'.join(pages)).replace('：', ':')
    for marker in ['2019年10月16日', '2019年10月29日', '87号文件发布',
                   '第二十六条', '最后一笔成交价格', '最高成交价格', '最低成交价格',
                   '单边数量', '第四十七条', '第一笔成交价', '撮合原则']:
        if marker not in text:
            raise ValueError('prior_rule_marker:' + marker)
    days = re.findall(r'自(\d{4})年(\d+)月(\d+)日起施行', text)
    if len(days) != 2 or {tuple(map(int, d)) for d in days} != {(2020, 1, 1)}:
        raise ValueError('prior_rule_effective_conflict')
    if '10:15' in text or '10:30' in text:
        raise ValueError('unexpected_day_session_clause')
    return dict(board_day=20191016, published_day=20191029, effective_day=20200101,
                rule_notice=87, ohlc_defined_from_trades=True,
                volume_and_oi_single_sided_in_this_version=True,
                previous_close_context='matching reference for first trade, not zero-trade OHLC fill',
                day_session_break_clause_found=False,
                continuity_to_20210104_proven=False, pk_initial_rule_gap_closed=False,
                annual_calendar_qualified=False)


def review(root):
    rule = (root/'rule2019.pdf').read_bytes()
    if hashlib.sha256(rule).hexdigest() != RULE_SHA:
        raise ValueError('prior_rule_sha')
    data = json.loads((root/'rule2019-extracted.json').read_bytes())
    if data['sha256'] != RULE_SHA or data['pages'] != len(data['texts']):
        raise ValueError('prior_extraction_identity')
    sdk = (root/'sdk-version.rst').read_bytes()
    if hashlib.sha1(b'blob '+str(len(sdk)).encode()+b'\0'+sdk).hexdigest() != SDK_BLOB:
        raise ValueError('sdk_blob_identity')
    text = sdk.decode('utf-8')
    # 客户端非量价接口和小数位修复都不能升级为EDB行情服务的历史处理版本。
    for marker in ['3.9.0 (2026/01/16)', 'query_edb_data', '获取非量价数据',
                   '2.8.1 (2021/08/12)', '某些数据未按照最小价格变动单位保留相应小数位数',
                   '2.3.5 (2021/03/19)', '超时（30s）之后跳过']:
        if marker not in text:
            raise ValueError('sdk_version_marker:' + marker)
    return dict(schema='edb-pk-direct-followup-v1', rule_sha256=RULE_SHA,
                sdk_git_blob=SDK_BLOB, sdk_sha256=hashlib.sha256(sdk).hexdigest(),
                rule=prior_rule_facts(data['texts']),
                edb_daily_historical_version_identified=False,
                post_close_event_ledger_obtained=False,
                all_118_differences_remain_unresolved=True, price_requests=0,
                calendar_generated=False, financial_eligible=False)


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--root', type=Path, required=True)
    p.add_argument('--out', type=Path, required=True)
    a = p.parse_args()
    result = review(a.root)
    a.out.write_text(json.dumps(result, ensure_ascii=False, indent=2)+'\n', encoding='utf-8', newline='\n')
    print('limited prior rule checked; EDB version, PK continuity and calendar remain blocked')
