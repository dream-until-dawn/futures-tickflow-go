"""从受信研究包与冻结补充元数据生成可离线维护的数据总览，不联网。"""
import argparse
import hashlib
import io
import json
from pathlib import Path
import sys

from page_model import model
from extract_names import FrozenInputs, PACKS
from inventory_model import attach, LABELS
from usage_readiness import markdown as readiness_markdown


def locked_repo(root, item):
    path=root/item['file']
    if not path.resolve().is_relative_to(root.resolve()):
        raise ValueError('status_repo_path')
    raw=path.read_bytes()
    if hashlib.sha256(raw).hexdigest()!=item['sha256']:
        raise ValueError('status_repo_sha:' + item['file'])
    return json.loads(raw)


def source_cells(p, exchanges):
    """列来源只依据实际取得资料；空响应、未请求和未知范围保持空白。"""
    if 'datasets' in p:
        kinds=[{'ohlc'},{'volume'},{'open_interest'},{'settlement'},{'turnover'},{'calendar'},
               {'minute','live_frames'},{'directory'},{'margin','fees','limits'}]
        def short_source(row):
            route=row['route']
            if route.startswith('official-'):return exchanges[p['exchange']]['short']+'（官方）'
            if route.startswith('cffex-'):return '中金所（明文）'
            if route.startswith('parameters'):return '上期所（业务表）'
            if route=='czce-clear-parameters':return '郑商所（结算参数表）'
            if route.startswith('engine-rb-public-'):return '上期所（业务表）'
            if route.startswith('sina-'):return '新浪（样本）'
            if route.startswith('edb-'):return '天勤 EDB'
            if route=='verified-calendar':return '官方公告／规则'
            return row['source']
        return [list(dict.fromkeys(short_source(r) for r in p['datasets'] if r['kind'] in group and r['present_rows'] and
                (r['kind']!='settlement' or r.get('valid_rows') is None or r['valid_rows']>0))) for group in kinds]
    quotes=[]
    if p['nonempty']:quotes.append('天勤 EDB')
    if any(s['source']=='EDB旧有限日线样本' for s in p['supplementary']):quotes.append('天勤 EDB（样本）')
    if any(s['source']=='新浪当期列表日线样本' for s in p['supplementary']):quotes.append('新浪（样本）')
    official=('中金所（明文原表列）' if p['exchange']=='CFFEX' else exchanges[p['exchange']]['short']+'（官方原表列）') if p['reference_days'] else ''
    if official:quotes.append(official)
    settlement=[official] if official else []
    # 未记录字段有效计数时不推定有结算值；零结算不作为有效结算价。
    if any(s['source']=='新浪当期列表日线样本' and 'absent_settlement' in s and 'zero_settlement' in s and s['rows']-s['absent_settlement']-s['zero_settlement']>0 for s in p['supplementary']):
        settlement.append('新浪（样本）')
    return [quotes,quotes,quotes,settlement,[official] if official else [],['官方公告／规则'] if p['calendar_ranges'] else []]


def markdown(data):
    s=data['summary'];states=s['states']
    lines=['# 各交易所与品种数据情况','',
        '资料快照：'+data['as_of']+'。本页与[可筛选HTML总览](data-status.html)由同一冻结模型生成。', '',
        '**读法：**非空/空/未请求是日线主目录的合约状态；其他样本另列，不重复加总。观察期是品种内合约并集起止，不是连续覆盖。无对照不等于一致，制度日历不等于价格或生命周期获证。用户指定的两道数据门槛见[独立结果表](data-usage-readiness.md)，当前参数回放历史属于情景；旧严格金融包协议和生产任务未改。','',
        f"主目录{s['candidates']:,}候选、{states['nonempty']:,}非空、{states['empty_unavailable']:,}完整空响应、{states['not_requested_on_directory_route']:,}本路线未请求；{s['rows']:,}行。六交易所{s['products']}品种含{s['financial_products']}个中金所金融品种，不能冒充全市场官方挂牌全集。",'']
    lines += ['格内只列已取得资料的来源；没有确认的留空。原表列存在不保证逐行有值或完成统一交付。点击HTML品种名查看覆盖日期和质量详情。','']
    if data.get('research_union'):
        union=data['research_union'];states=union['states']
        lines += [f"原封存目录不改写；与本轮独立补充路线取并集后，同一{union['candidates']}候选中{states['nonempty']}非空、{states['empty_unavailable']}完整空、{states['not_requested_on_directory_route']}本路线未请求，共{union['rows']}行。其他来源重叠不相加；新差异另列待核。",'']
    for exchange, label in data['exchanges'].items():
        lines += ['## '+label['name']+'（'+exchange+'）','',
            '| 品种 | OHLC（日线） | 成交量 | 持仓量 | 结算价 | 成交额 | 历史交易日历 | 分钟/现场 | 目录/生命周期 | 保证金/手续费/涨跌停 |',
            '| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |']
        for p in data['products']:
            if p['exchange']!=exchange:continue
            cells=['；'.join(s) for s in source_cells(p,data['exchanges'])]
            if len(cells)==6:cells+=['','','']
            lines.append('| '+p['name'].replace('|','\\|')+' `'+p['code']+'` | '+' | '.join(cells)+' |')
        lines.append('')
    if 'project_inventory' in data:
        lines += ['## 各类数据的范围、完整性和更新时间','',
                  '下表按来源分列，不相加。起止为该字段实际有值的观察日期；有效范围另注。原件取得、归档回执及派生索引时间不同；未知不取文件修改时间补齐。','']
        for p in data['products']:
            if not p.get('datasets'):continue
            lines += ['### '+p['name']+' '+p['key'],'',
                      '| 类型 / 来源 | 观察起止 | 完整性 | 最后取得 / 其他更新时间 |',
                      '| --- | --- | --- | --- |']
            for r in p['datasets']:
                span=(r.get('first') or '未知')+' → '+(r.get('last') or '未知')
                count=str(r['present_rows'])+'有值 / '+str(r['rows'])+'原记录；有效'+str(r.get('valid_rows') if r.get('valid_rows') is not None else '未知')
                time=r.get('last_acquired_at') or r.get('last_acquired_date') or '未知'
                if r.get('last_receipt_at'):time+='；归档回执 '+r['last_receipt_at']
                if r.get('last_indexed_at'):time+='；索引 '+r['last_indexed_at']
                if r.get('last_exported_at'):time+='；快照导出 '+r['last_exported_at']
                lines.append('| '+LABELS.get(r['kind'],r['kind'])+' / '+r['source']+' | '+span+' | '+count+'；'+r['completeness']+' | '+time+' |')
            lines.append('')
    shared=data.get('project_inventory',{}).get('shared_documents',[])
    if shared:
        lines += ['## 共享官方网页原件（适用性另核）','','这组资料是公告或当前网页，不自动填为所有品种的历史规则、日历或交易许可。','']
        for item in shared:
            lines.append('- ['+item['title']+']('+item['url']+')；取得 '+item['requested_at']+'；SHA256 '+item['sha256']+'。'+item['readback'])
        lines.append('')
    lines += ['## 字段、置信度与不足','',
        '- 日线主源为Shinny EDB；其历史版本、零成交OHLC和盘后量仓语义仍有待核，不能用收盘价代替结算价。已有参考原表归档了结算/成交额列（范围同参考，不保证每行有值、单位或资格已证）；主EDB没有这两列，未完成对账/统一Source交付。',
        '- SHFE/INE参考为2021—2025官方HTTPS年度原表；CZCE仅核2021-01-04；CFFEX为封存明文HTTP月包及有限参考，发布者未认证。对照仅覆盖实际参考合约日。',
        f"- 主目录{s['unresolved_fields']}个数值字段仍待核，另有旧供应商间{s['legacy_vendor_distinct_fields']}项差异不重复合并；参考未报价、无参考日期、完整空响应、日历未知和缺日分别保留。",
        '- DCE/GFEX主目录零行不等于项目无数据：新浪当期列表样本和旧EDB样本单列，尚未认证官方历史全集、制度和全生命周期。',
        '- 字节身份/计数复现可信；价格语义、官方生命周期、历史PIT、许可再分发、账户费用和所有品种逐日完整性仍不足，不给虚构置信百分比。',
        '- 引擎已通过限定范围的独立CSV研究消费验收及反例；未接入任务Feed，未核价格/制度语义或放宽金融协议。', '',
        '按品种的[回测与盘前判断条件](data-usage-readiness.md)另列；来源锁、维护步骤及验收记录见[维护计划](data-status-maintenance-plan.md)；使用边界见[使用者维护手册](consumer-maintenance.md)。']
    return '\n'.join(lines)+'\n'


def render_html(data, assets):
    # 防止源名称中的HTML结束标签突破内嵌JSON脚本；前端只用textContent写数据。
    data=dict(data,products=[dict(p,source_cells=source_cells(p,data['exchanges'])) for p in data['products']])
    payload=json.dumps(data,ensure_ascii=False,separators=(',',':')).replace('<','\\u003c').replace('>','\\u003e').replace('&','\\u0026')
    return (assets/'page.html').read_text(encoding='utf-8').replace('@@STYLE@@',(assets/'style.css').read_text(encoding='utf-8')).replace('@@SCRIPT@@',(assets/'page.js').read_text(encoding='utf-8')).replace('@@DATA@@',payload)


def check_rendered(repo, data):
    expected={'data-status.md':markdown(data),'data-status.html':render_html(data,Path(__file__).parent/'assets')}
    for name,text in expected.items():
        if (repo/'docs'/name).read_bytes()!=text.encode('utf-8'):
            raise ValueError('status_render_drift:' + name)
    if data.get('project_inventory') and (repo/'docs/data-usage-readiness.md').read_text(encoding='utf-8')!=readiness_markdown(data):
        raise ValueError('status_readiness_render_drift')


def check_docs(repo, config):
    """CI只用公开冻结模型复验渲染和来源锁；不冒称重新读取仓外原价CSV。"""
    raw=(repo/'docs/data-status.json').read_bytes()
    if hashlib.sha256(raw).hexdigest()!=config['published_model_sha256']:
        raise ValueError('status_published_model_sha')
    data=json.loads(raw)
    if data['provenance']['catalog_sha256']!=config['catalog_sha256'] or data['strict_backtest_admission_expanded'] is not False:
        raise ValueError('status_published_scope')
    for item in config['repo_inputs']+config['profiles']:
        locked_repo(repo,item)
    inventory=next((r for r in config['repo_inputs'] if r['id']=='project_inventory'),None)
    if inventory and data.get('project_inventory')!=locked_repo(repo,inventory):
        raise ValueError('status_inventory_binding')
    facts=next((r for r in config['repo_inputs'] if r['id']=='user_gate_facts'),None)
    if facts and data.get('user_data_gate_facts')!=locked_repo(repo,facts):
        raise ValueError('status_user_gate_facts_binding')
    extension=data.get('project_inventory',{}).get('history_extension')
    if extension and extension['parent_catalog_sha256']!=config['catalog_sha256']:raise ValueError('status_extension_parent_sha')
    if inventory and data['products']!=attach(data,data['project_inventory'])['products']:
        raise ValueError('status_product_inventory_binding')
    check_rendered(repo,data)
    return data['summary']


def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--bundle',type=Path)
    p.add_argument('--repo',type=Path,default=Path(__file__).resolve().parents[3])
    p.add_argument('--out-root',type=Path)
    p.add_argument('--check-docs',action='store_true',help='只用公开冻结模型检查已发布文档及来源锁，供离线CI')
    a=p.parse_args();config=json.loads((Path(__file__).parent/'sources.json').read_bytes())
    if a.check_docs:
        print(json.dumps(check_docs(a.repo,config),ensure_ascii=False));return
    if a.bundle is None or a.out_root is None:
        p.error('重建需要--bundle及全新--out-root；已有文档可用--check-docs只读核验')
    if a.out_root.exists() or a.out_root.resolve().is_relative_to(a.bundle.resolve()):
        raise ValueError('status_output_exists_or_inside_input')
    bundle=FrozenInputs(a.bundle,config['bundle_manifest_sha256'])
    raw=bundle.read('catalog.json')
    if hashlib.sha256(raw).hexdigest()!=config['catalog_sha256']:raise ValueError('status_catalog_sha')
    catalog=json.loads(raw);repo_data={item['id']:locked_repo(a.repo,item) for item in config['repo_inputs']}
    names=repo_data['names']
    if names['inventory_sha256']!=catalog['inventory_sha256']:
        raise ValueError('status_name_inventory')
    reports={key:bundle.data('inputs/'+key+'/'+value[2])['results'] for key,value in PACKS.items()}
    profiles=[(r['file'],locked_repo(a.repo,r)) for r in config['profiles']]
    labels=json.loads((Path(__file__).parent/'labels.json').read_bytes())
    data=model(catalog,reports,names['products'],profiles,repo_data['sina_current'],repo_data['edb_legacy'],labels,config['as_of'])
    engine=repo_data['engine_acceptance']
    if engine['report_byte_identical'] is not True or engine['financial_admission_added']!=0 or engine['price_semantics_accepted'] is not False or engine['feed_integrated'] is not False:
        raise ValueError('status_engine_scope')
    data['engine_acceptance']=engine
    if 'project_inventory' in repo_data:
        extension=repo_data['project_inventory'].get('history_extension')
        if extension and extension['parent_catalog_sha256']!=config['catalog_sha256']:raise ValueError('status_extension_parent_sha')
        data=attach(data,repo_data['project_inventory'],repo_data.get('user_gate_facts'))
    data['provenance']=dict(bundle_manifest_sha256=config['bundle_manifest_sha256'],catalog_sha256=config['catalog_sha256'],
                            repo_inputs=config['repo_inputs'],profiles=config['profiles'])
    a.out_root.mkdir(parents=True)
    for name,value in [('data-status.json',json.dumps(data,ensure_ascii=False,indent=2)+'\n'),
                       ('data-status.md',markdown(data)),('data-status.html',render_html(data,Path(__file__).parent/'assets')),
                       ('data-usage-readiness.md',readiness_markdown(data))]:
        (a.out_root/name).write_text(value,encoding='utf-8',newline='\n')
    print(json.dumps(data['summary'],ensure_ascii=False))


if __name__=='__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    main()
