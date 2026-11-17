"""用户两道数据硬门槛；不以加分项或旧严格金融包资格代替判断。"""
import io
import sys
from decimal import Decimal, InvalidOperation


STATES = {'pass', 'pending', 'not_met'}
REQUIRED = {
    'backtest': ('history_ohlc', 'current_margin', 'effective_min_open'),
    'daily_signal': ('settlement', 'daily_update'),
}


def item(status='pending', scope=(), evidence_ids=(), reason='尚未取得该硬项的充分依据'):
    if status not in STATES:
        raise ValueError('data_gate_item_status')
    return dict(status=status, scope=sorted(set(scope)), evidence_ids=sorted(set(evidence_ids)), reason=reason)


def effective_minimum(row, trade_day):
    """普通最小报单、最大报单和大宗交易阈值均不能补实际最低开仓空值。"""
    value = row.get('effective_min_open_lots')
    if type(value) is not int or value <= 0:
        return False
    return (row.get('as_of_trade_day') == trade_day and
            row.get('minimum_lots_kind') == 'effective_min_open' and
            row.get('special_adjustments_reviewed') is True and
            bool(row.get('minimum_evidence_ids')))


def current_margin(row, trade_day):
    margin = row.get('margin') or {}
    if row.get('as_of_trade_day') != trade_day or margin.get('unit') != 'ratio':
        return False
    if margin.get('basis') not in {'exchange_spec', 'company'} or not margin.get('evidence_ids'):
        return False
    try:
        return all(Decimal(str(margin[k])).is_finite() and 0 < Decimal(str(margin[k])) <= 1
                   for k in ('long', 'short'))
    except (KeyError, InvalidOperation):
        return False


def evaluate(hard_items, function):
    """每项先独立判断，再取适用合约交集，避免不同合约拼成虚假的通过。"""
    names = REQUIRED[function]
    if set(hard_items) != set(names):
        raise ValueError('data_gate_hard_item_set')
    if any(v.get('status') not in STATES for v in hard_items.values()):
        raise ValueError('data_gate_item_status')
    reasons = [name + ': ' + hard_items[name]['reason'] for name in names
               if hard_items[name]['status'] != 'pass']
    status = 'not_met' if any(v['status'] == 'not_met' for v in hard_items.values()) else 'pending' if reasons else 'pass'
    # 长历史是品种跨到期合约序列，不要求当前合约本身在2021年已经存在。
    scoped = [set(hard_items[n]['scope']) for n in names if n != 'history_ohlc']
    contracts = sorted(set.intersection(*scoped))
    if status == 'pass' and not contracts:
        status = 'pending'
        reasons.append('适用具体合约没有交集，不能拼接不同范围的条件')
    return dict(status=status, hard_items=hard_items,
                eligible_contracts=contracts if status == 'pass' else [], blocking_reasons=reasons)


def assess(product, facts):
    """已重数的年度观察与当前合约事实分开；不拿全市场旧准入标记否决新门槛。"""
    key = product['key']
    day = facts['current_trade_day']
    quotes = [r for r in product.get('datasets', []) if r['kind'] == 'ohlc' and r.get('valid_rows', 0)]
    years = {}
    for row in quotes:
        for year, count in row.get('valid_days_by_year', {}).items():
            # 同源/异源日期可能重叠，取单路线最大值，不相加冒充覆盖。
            years[year] = max(years.get(year, 0), count)
    first = min((r['valid_first'] for r in quotes if r.get('valid_first')), default=None)
    last = max((r['valid_last'] for r in quotes if r.get('valid_last')), default=None)
    listing = facts.get('listing_days', {}).get(key)
    baseline = max(2021, listing['day'] // 10000) if listing else 2021
    expected = set(map(str, range(baseline, facts['latest_completed_day'] // 10000 + 1)))
    listing_text = (str(listing['day'])[:4]+'-'+str(listing['day'])[4:6]+'-'+str(listing['day'])[6:]) if listing else None
    starts_in_time = first <= listing_text if first and listing_text and baseline > 2021 else bool(first and first[:4] <= '2021')
    history_pass = bool(starts_in_time and expected <= set(years))
    reason = ('已有自2021年前后或已证上市日起逐年有效OHLC观察；缺日和版本差异另列，不认证连续全集'
              if history_pass else '尚未核得2021年前后起的逐年有效OHLC，或上市日起点直接依据仍待绑定')
    history = item('pass' if history_pass else 'pending', evidence_ids=['project-inventory'], reason=reason)
    history['coverage'] = dict(first=first, last=last, listing_day=listing['day'] if listing else None, valid_days_by_year=years)
    current = [r for r in facts['current_contracts'] if r['product_key'] == key]
    rates = [r['symbol'] for r in current if current_margin(r, day)]
    minimum = [r['symbol'] for r in current if effective_minimum(r, day)]
    rate_evidence = sorted({e for r in current if r['symbol'] in rates for e in r['margin']['evidence_ids']})
    minimum_evidence = sorted({e for r in current if r['symbol'] in minimum for e in r['minimum_evidence_ids']})
    back = dict(history_ohlc=history,
                current_margin=item('pass' if rates else 'pending', rates,
                    rate_evidence,
                    '已核指定日交易所比例或明确发布者的公司公开标准情景；来源与账户实收分开' if rates else '实际当前有效比例待补证；参考值或旧日值不冒充'),
                effective_min_open=item('pass' if minimum else 'pending', minimum,
                    minimum_evidence,
                    '指定日具体合约限价/市价的具名模型审核，含已审专项；实际客户适用性另核' if minimum else '实际最低开仓及专项适用链待补证，不默认普通1手'))
    settlement = sorted(facts['settlement_scopes'].get(key, []))
    can_update = key.split('.')[0] in facts['daily_update_exchanges'] and bool(settlement)
    signal = dict(settlement=item('pass' if settlement else 'pending', settlement,
                                 ['official-day-'+str(facts['latest_completed_day'])] if settlement else [],
                                 '已核目标已收盘日有效结算原件' if settlement else '目标已收盘日有效结算待取得'),
                  daily_update=item('pass' if can_update else 'pending', settlement if can_update else [],
                                    ['archive-batch-daily'] if can_update else [],
                                    '既有按日HTTPS入口及原件校验已实取；非全市场自动部署承诺' if can_update else '本品种逐日结算更新能力待核'))
    return dict(backtest=evaluate(back, 'backtest'), daily_signal=evaluate(signal, 'daily_signal'))


if __name__ == "__main__":
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    raise SystemExit("由数据维护工具导入，不联网")
