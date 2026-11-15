"""从已完整解析并人工核图的官方PDF提取有限事实，不生成年度日历。"""
import argparse
import hashlib
import io
import json
import re
import sys
from pathlib import Path

SOURCES = {'pk-original':'83db54939066d9aed186f5c8557ee118b8773f7c3e988bbeafb794f2127660ed',
           'czce-rules':'08df1b911d52a73ffcde874600345bc264f95de8c1f29fa16d2989b6ff7d3b0f',
           'pk-journal-reused':'d452c93c7411fa1ece5646d985a70e8e99054aac5153120345f23bc8b38e4966'}
EXPECTED_PAGES = {'pk-original':158,'czce-rules':11,'pk-journal-reused':47}


def normalized(text):
    return re.sub(r'\s+', '', text).replace('：',':').replace('－','-').replace('–','-').replace('—','-')


def facts(contract_pages, rule_pages, journal_pages):
    contract = normalized(''.join(contract_pages[:2]))
    rules = normalized(''.join(rule_pages))
    journal = normalized(journal_pages[42])
    for marker in ['〔2021〕8号','2021年1月25日','自2021年2月1日起施行','5吨/手','2元/吨','1、3、4、10、11、12月','第10个交易日']:
        if marker not in contract:
            raise ValueError('pk_contract_marker:'+marker)
    for marker in ['〔2021〕9号','2021年2月1日','PK2110','PK2111','PK2112','PK2201','暂不开展夜盘交易']:
        if marker not in journal:
            raise ValueError('pk_listing_marker:'+marker)
    for marker in ['2021年1月28日','2021年2月22日','2021年2月22日起施行','9:00-11:30','10:15-10:30为休息时间','13:30-15:00','单边数量','每个交易日结束后发布']:
        if marker not in rules:
            raise ValueError('rule_marker:'+marker)
    # 标题和附则都可能写生效日；不能只找到一处正确值就忽略另一处矛盾。
    effective_days = {int(y)*10000+int(m)*100+int(d) for y,m,d in re.findall(r'自(\d{4})年(\d{1,2})月(\d{1,2})日起施行',rules)}
    if effective_days != {20210222}:
        raise ValueError('rule_effective_conflict')
    return dict(schema='czce-2021-limited-official-facts-v1',
                pk_contract_notice=8, pk_contract_published_day=20210125, pk_contract_effective_day=20210201,
                first_listing_day=20210201, first_contracts=['CZCE.PK2110','CZCE.PK2111','CZCE.PK2112','CZCE.PK2201'],
                volume_multiple_tonnes=5, price_tick_yuan_per_tonne=2, delivery_months=[1,3,4,10,11,12],
                last_trading_day_rule='10th trading day of delivery month; actual dates not derived',
                initial_no_night=True, full_year_no_night_proven=False,
                rule_board_approved_day=20210128, rule_published_day=20210222, rule_effective_day=20210222,
                source_day_sections=2, source_intraday_break=['10:15','10:30'],
                derived_open_day_segments=[['09:00','10:15'],['10:30','11:30'],['13:30','15:00']],
                general_rule_applies_to_sample_20210104=False,
                missing_pk_initial_rule_interval=[20210201,20210221],
                source_daily_volume_and_oi_counting='single-sided in this rule version',
                source_daily_information_publication='after each trading day; exact cutoff/revisions not specified here',
                official_actual_expiry_dates_derived=False, historical_PIT_proven=False,
                verified_calendar_generated=False, financial_eligible=False)


def main():
    p=argparse.ArgumentParser(description=__doc__);p.add_argument('--extracted-root',type=Path,required=True);p.add_argument('--out',type=Path,required=True)
    p.add_argument('--source-root',type=Path,help='迁移时按原SHA命名的PDF目录，避免回读原机路径')
    a=p.parse_args();loaded={};identities={}
    for name,expected in SOURCES.items():
        path=a.extracted_root/(name+'.json');raw=path.read_bytes();data=json.loads(raw)
        source_raw=(a.source_root/(expected+'.pdf') if a.source_root else Path(data['source'])).read_bytes()
        if hashlib.sha256(source_raw).hexdigest()!=expected or data['sha256']!=expected or data['pages']!=EXPECTED_PAGES[name] or len(data['texts'])!=data['pages']:
            raise ValueError('official_source_sha_or_pages')
        loaded[name]=data['texts'];identities[name]=dict(sha256=expected,pages=data['pages'],extraction_sha256=hashlib.sha256(raw).hexdigest())
    report=facts(loaded['pk-original'],loaded['czce-rules'],loaded['pk-journal-reused']);report['sources']=identities
    a.out.write_text(json.dumps(report,ensure_ascii=False,indent=2)+'\n',encoding='utf-8',newline='\n')
    print('official facts checked; PK initial rule gap and annual calendar qualification remain uncovered')


if __name__=='__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    main()
