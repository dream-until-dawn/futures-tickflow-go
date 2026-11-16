"""使用条件仅从本表证据分级；取数事实不升级为持续交付或金融资格。"""
import io
import sys


def assess(product):
    prices=[r for r in product.get('datasets',[]) if r.get('kind')=='ohlc' and (r.get('valid_rows') or 0)>0]
    latest=[r for r in product.get('datasets',[]) if r.get('route','').startswith('official-day-') and r.get('last_acquired_at') and r.get('last')]
    latest.sort(key=lambda r:(r['last'],r['last_acquired_at']))
    if latest:latest=[r for r in latest if r['last']==latest[-1]['last']]
    row=next((r for r in reversed(latest) if r['kind']=='ohlc'),latest[-1] if latest else None)
    fields={r['kind']:dict(present=r['present_rows'],valid=r.get('valid_rows')) for r in latest if row and r['last']==row['last']}
    consumer=product.get('consumer_premarket')
    observed=bool(consumer and consumer.get('independently_verified'))
    consumption=('已独立核 '+consumer['contract']+' 的'+str(consumer['successful_market_dates'])+'个行情日消费事实；'+
                 ('最新按NextDay相连'+str(consumer['latest_next_day_chain'])+'日；' if consumer.get('latest_next_day_chain') else '仅单日未审核模型预览；')+
                 '不推至其他合约或未来；原HTTP取得时刻未知，消费/观察上界另注。') if observed else ''
    return dict(research_backtest='partial_price_research' if prices else 'no_verified_price_rows',
        research_conditions='仅限实际有值且通过基本OHLC检查的合约/日期；待核差异行不得视为已对齐输入，另核所需窗口、缺日、版本及策略口径。不是逐品种全历史认证。',
        full_backtest='not_certified_by_this_inventory',
        full_backtest_gaps='全历史具体合约全集、生命周期/有效日历、价格版本与PIT、量额/零成交语义及所需制度参数未闭合；既有独立包的获批范围另核。',
        latest_official=(dict(day=row['last'],rows=row['rows'],valid_rows=fields.get('ohlc',{}).get('valid'),acquired_at=row['last_acquired_at'],source=row['source'],fields=fields) if row else None),
        preopen_previous_day='independent_consumption_observed' if observed else 'single_postclose_snapshot' if row else 'not_demonstrated_here',
        preopen_conditions=consumption+'盘后原件或有限消费记录不保证未来每日交付、所有合约窗口齐备或全部策略均可运行。',
        stable_preopen_delivery_certified=False,financial_admission_added=False)


def markdown(data):
    prices=sum((p.get('usage_readiness') or assess(p))['research_backtest']=='partial_price_research' for p in data['products'])
    snapshots=[p for p in data['products'] if (p.get('usage_readiness') or assess(p))['latest_official']]
    valid=sum(((p.get('usage_readiness') or assess(p))['latest_official']['valid_rows'] or 0)>0 for p in snapshots)
    calendars=[p['key'] for p in data['products'] if p.get('calendar_ranges')]
    consumers={p['key']:p.get('consumer_premarket') for p in data['products'] if p.get('consumer_premarket')}
    rb=consumers.get('SHFE.rb');rm=consumers.get('CZCE.RM')
    rb_text=(rb['contract']+'：'+str(rb['successful_market_dates'])+'个成功行情日，最新执行日相连'+str(rb['latest_next_day_chain'])+'日') if rb else '未列独立复核'
    rm_text=(rm['contract']+'：'+str(rm['successful_market_dates'])+'日真实日线和完成的模型预览') if rm else '未列独立复核'
    lines=['# 数据用于回测和盘前判断的阶段情况','',
           '资料日期：'+data['as_of']+'。这里的“执行”仅指开盘前取得上一交易日行情供策略判断，不指订单或资金操作。','',
           '已有价格研究回测基础与完整回测条件分开：研究可使用已收、通过基本检查的行；完整性、缺日分母、有效制度、PIT及版本等仍须满足具体策略要求。本表不替代其他独立数据包的既有批准。','',
           '“有当日盘后原件”只证明一次取得；不能据此声称已实现所有品种每日开盘前稳定供数。新批EDB只补历史至2026-11-13，不是新的每日更新机制。','',
           '| 功能 | 当前数据或实测 | 范围限制 |','| --- | --- | --- |',
           '| 价格研究回测 | '+str(prices)+'个品种有部分基本有效OHLC观察 | 按具体合约/日期选择；待核差异不视为已对齐，非全历史认证 |',
           '| 历史制度日历配套 | '+str(len(calendars))+'个品种：'+ '、'.join(calendars)+' | 仅下表明确起止；不是行情、生命周期或金融资格已齐 |',
           '| 上一交易日价格准备 | '+str(len(snapshots))+'个品种有最新官方日表，其中'+str(valid)+'个有基本有效OHLC | 其余有别的有效字段时按所需字段判断；一次取得不等于每日自动交付 |',
           '| 多日盘前策略消费 | '+rb_text+' | 仅该合约；含失败后正常重试，不承诺未来持续成功 |',
           '| 单日盘前策略消费 | '+rm_text+' | 不是多日稳定实测，预览尚未审核 |',
           '| 完整回测 / 新金融准入 | 本表不新增认证 | 仍须闭合相应历史目录、有效制度、生命周期、价格版本与PIT等条件 |','',
           '夜盘属于下一交易日：以首个夜盘开盘为截止，不延至翌日日盘。RB原包消费时刻不是HTTP取得时刻；RM预览CreatedAt为零，使用独立读取UTC作最迟可用上界。相关选择性原件的范围和限制见[独立复核](evidence/history-capability-20261116/consumer-independent-readback.json)。','',
           '| 交易所 / 品种 | 价格研究回测基础 | 本表已验历史日历范围 | 最新官方盘后日表（字段有效性另列） | 盘前策略消费实测 |','| --- | --- | --- | --- | --- |']
    for p in data['products']:
        r=p.get('usage_readiness') or assess(p);latest=r['latest_official']
        snapshot=(latest['day']+'；OHLC有效'+str(latest['fields'].get('ohlc',{}).get('valid','未知'))+'/'+str(latest['rows'])+'；结算有效'+str(latest['fields'].get('settlement',{}).get('valid','未知'))+'；取得'+latest['acquired_at']) if latest else '本表未实测'
        calendars='；'.join(x['first']+'—'+x['last'] for x in p.get('calendar_ranges',[])) or '未获证'
        consumer=p.get('consumer_premarket');consumption=('已核 '+consumer['contract']+'，'+str(consumer['successful_market_dates'])+'个成功日；'+('最新NextDay链'+str(consumer['latest_next_day_chain'])+'日' if consumer.get('latest_next_day_chain') else '单日未审核预览')) if consumer else '未实测策略消费链'
        lines.append('| '+p['key']+' '+p['name']+' | '+('有部分可用观察，按合约/日期筛选' if r['research_backtest']=='partial_price_research' else '无已核有效OHLC行')+' | '+calendars+' | '+snapshot+' | '+consumption+' |')
    lines += ['','各来源的准确起止、缺失/异常和更新时间见[总表品种详情](data-status.html)。目录观察不等于挂牌生命周期，官方日历也不等于价格齐全。主目录144项和旧供应商37项差异保持待核，后续新对账差异另列。','',
              '下一阶段按品种建立盘前执行单：指定上一交易日、策略需要的具体合约与回看窗口、字段口径、采集截止、连续运行及失败即禁止输出信号。未完成这些验证之前，不能把“HTTP成功”当“可持续执行”。']
    return '\n'.join(lines)+'\n'


if __name__=='__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    raise SystemExit('由文档生成器调用，不联网')
