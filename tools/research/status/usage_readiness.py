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
           '**2026-11-16起采用用户指定的两道独立数据门槛。**','',
           '| 门槛 | 硬性条件 |','| --- | --- |',
           '| 回测 | 2021年前后至近期的OHLC日线；2021年后上市的取上市以来；当前保证金率；实际最低开仓手数（包含已生效专项调整） |',
           '| 逐日输出信号 | 适用具体合约的有效结算价；按交易日抓取、校验、更新能力 |','',
           '普通报单1手、大宗交易门槛和最大报单量不能代替实际最低开仓。专项必须核适用合约、有效交易日及范围。日历、历史费率、生命周期、PIT、对账和多日运行实测均为补充项，不额外抬高这两道数据门槛。','',
           '新门槛按当前冻结事实逐品种核算，限定具体合约和有效日期；后面的旧严格包观察表仅为补充。不能把旧“本表未认证”读成新数据门槛不通过，也不能把部分OHLC当完整硬项。接口见[两门槛方案](two-data-gates-20261116.md)与[机器接口](user-data-gates-format.md)。','',
           '资料日期：'+data['as_of']+'。这里的“执行”仅指开盘前取得上一交易日行情供策略判断，不指订单或资金操作。','',
           '研究可使用实际已收、通过基本检查且适用的行；旧严格金融包和实际生产部署授权单独保留，不用它们替代新的两门槛判断。','',
           '“有当日盘后原件”只证明一次取得；不能据此声称已实现所有品种每日开盘前稳定供数。新批EDB只补历史至2026-11-13，不是新的每日更新机制。','',
           '| 功能 | 当前数据或实测 | 范围限制 |','| --- | --- | --- |',
           '| 价格研究回测 | '+str(prices)+'个品种有部分基本有效OHLC观察 | 按具体合约/日期选择；待核差异不视为已对齐，非全历史认证 |',
           '| 历史制度日历配套 | '+str(len(calendars))+'个品种：'+ '、'.join(calendars)+' | 仅下表明确起止；不是行情、生命周期或金融资格已齐 |',
           '| 上一交易日价格准备 | '+str(len(snapshots))+'个品种有最新官方日表，其中'+str(valid)+'个有基本有效OHLC | 其余有别的有效字段时按所需字段判断；一次取得不等于每日自动交付 |',
           '| 多日盘前策略消费 | '+rb_text+' | 仅该合约；含失败后正常重试，不承诺未来持续成功 |',
           '| 单日盘前策略消费 | '+rm_text+' | 不是多日稳定实测，预览尚未审核 |',
           '| 旧严格金融包 / 生产操作 | 本轮未改旧包及生产任务 | 不作为用户两道数据门槛的附加条件 |','',
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
    if data.get('user_data_gate_facts'):
        labels={'pass':'通过（限所列合约/日期）','pending':'待补证','not_met':'未达到'}
        gated=[p for p in data['products'] if p.get('user_data_gates')]
        counts={name:sum(p['user_data_gates'][name]['status']=='pass' for p in gated) for name in ('backtest','daily_signal')}
        current=data['user_data_gate_facts']['current_trade_day'];completed=data['user_data_gate_facts']['latest_completed_day']
        gate_lines=['## 当前两门槛结果','',f'最新完整行情日 {completed}，当前参数交易日 {current}。回测通过{counts["backtest"]}个品种；逐日信号通过{counts["daily_signal"]}个品种。两者独立，不相加为全市场完成数。','',
                    '回测长历史按自2021年前后的逐年有效OHLC观察核，逐年实际日期数在详情；不将起止跨度冒充逐日完整。2021年后新上市品种的上市日起点尚未绑定到本轮机器事实时保留待补证。当前RB最低开仓依据是指定日、指定12合约的具名模型审核，实际客户适用性另核；不是同交易所的普遍声明。','',
                    '逐日信号通过表示已有目标日有效结算和既有按日采集/校验入口；不代表全部合约、不代表后台自动服务已部署，也不承诺未来每次更新成功。郑商所三位原生码用同日XML显式年月绑定。','',
                    '| 品种 | 回测门槛 | 逐日信号门槛 | 硬项缺口 / 通过范围 |','| --- | --- | --- | --- |']
        readback=data.get('user_data_gate_readback')
        if readback:
            gate_lines[2:2]=[f'**策略实测另列：**已读回{readback["result_files_checked"]}份真实信号消费结果并逐约核结算；单日MACD均预热不足，实际就绪信号{readback["ready_signals"]}。这是策略样本窗口限制，不把52个品种的数据门槛重新判失败。该首包不含保证金/最低开仓，通用假设文案的误示已反馈；旧结果留存，回测消费另核。见[独立读回](data-gates-consumer-readback-20261116.json)。','']
        history=data.get('history_consumer_readback')
        if history:
            ready='、'.join(history['ready_contracts']) or '无'
            freeze='该版按原冻结预算通过，仅所列有限约实测' if history['whole_bundle_queue_ready'] is True else '该版整包尚未通过冻结，仅直接消费实测'
            gate_lines[2:2]=['**补充完整结算历史后的最新有限实测：**'+ready+'达到当前CN MACD目标日列就绪判定（实际需第607条）；其余所测RB/SO仍不足。'+freeze+'。价格条件可算不是订单或真实前向，其他未测合约不自动外推。详见[预热维护与实际证据](signal-history-warmup-20261116.md)。','']
        for product in gated:
            b=product['user_data_gates']['backtest'];s=product['user_data_gates']['daily_signal']
            detail='；'.join(b['blocking_reasons']+s['blocking_reasons']) or ('回测当前约束情景：'+','.join(b['eligible_contracts']))
            for name,label in [('history_ohlc','长期日线OHLC'),('current_margin','当前保证金率'),('effective_min_open','实际最低开仓手数'),('settlement','有效结算价'),('daily_update','按交易日抓取更新')]:
                detail=detail.replace(name+':',label+'：')
            if s['status']=='pass':detail+='；日更结算'+str(len(s['eligible_contracts']))+'个合约'
            gate_lines.append('| '+product['key']+' '+product['name']+' | '+labels[b['status']]+' | '+labels[s['status']]+' | '+detail+' |')
        # 主结论放在旧观察清单之前，避免旧严格条件覆盖新用户口径。
        at=lines.index('资料日期：'+data['as_of']+'。这里的“执行”仅指开盘前取得上一交易日行情供策略判断，不指订单或资金操作。')
        lines[at:at]=gate_lines+['','## 取得资料和旧严格包的补充观察','']
    return '\n'.join(lines)+'\n'


if __name__=='__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    raise SystemExit('由文档生成器调用，不联网')
