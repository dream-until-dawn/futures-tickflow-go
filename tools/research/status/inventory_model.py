"""全项目资料按来源、品种、字段统计；观察跨度不推导为完整历史。"""
import datetime as dt
import hashlib
from decimal import Decimal, InvalidOperation
import io
import re
import sys


FIELDS = {
    'ohlc': ('open', 'high', 'low', 'close'),
    'volume': ('volume',), 'open_interest': ('open_interest',),
    'settlement': ('settle',), 'pre_settlement': ('pre_settle',),
    'turnover': ('turnover',),
    'margin': ('spec_long_margin', 'spec_short_margin'),
    'fees': ('trade_fee_rate', 'trade_fee_fixed'),
    'limits': ('upper_limit_ratio', 'lower_limit_ratio'),
    'directory': ('listing_day', 'expiry_day'),
}
LABELS = {'ohlc': '日线OHLC', 'volume': '成交量', 'open_interest': '持仓量',
          'settlement': '结算价', 'pre_settlement': '前结算价', 'turnover': '成交额',
          'calendar': '历史交易日历', 'minute': '分钟历史/现场',
          'directory': '合约目录/生命周期', 'rules': '规则/业务参数'}
LABELS.update(margin='交易所保证金表', fees='交易所手续费表', limits='涨跌停参数表')
LABELS['live_frames'] = '现场原始帧'


def utc(value):
    if not value:
        return None
    parsed = dt.datetime.fromisoformat(value.replace('Z', '+00:00'))
    if parsed.tzinfo is None:
        raise ValueError('inventory_naive_timestamp')
    return parsed.astimezone(dt.timezone.utc).isoformat()


def product(exchange, native):
    # 只提取品种字母，绝不由三位年月猜测具体合约年份。
    match = re.fullmatch(r'([A-Za-z]+)\d{3,4}', native)
    if not match or exchange not in {'SHFE', 'INE', 'CZCE', 'DCE', 'GFEX', 'CFFEX'}:
        raise ValueError('inventory_product_identity')
    code = match[1].upper() if exchange in {'CZCE', 'CFFEX'} else match[1].lower()
    return exchange + '.' + code


def numeric(value):
    try:
        result = Decimal(str(value).replace(',', ''))
        return result if result.is_finite() else None
    except InvalidOperation:
        return None


class Accumulator:
    """同一路线严格去重，按各列实际有值的日期单独统计。"""

    def __init__(self):
        self.groups = {}
        self.seen = set()

    def add(self, route, source, record, indexed_at=None):
        key = product(record['exchange'], record['native'])
        day = dt.datetime.strptime(str(record['day']), '%Y%m%d').date().isoformat()
        identity = (route, record['exchange'], record['native'], day)
        if identity in self.seen:
            raise ValueError('inventory_duplicate_record')
        self.seen.add(identity)
        evidence = record.get('evidence', {})
        received = utc(evidence.get('retrieved_at'))
        # 旧年包保留的是本地留存事件，不能认证最初HTTP接收时刻。
        uncertain = 'first original HTTP UTC unresolved' in evidence.get('version', '')
        values = {field: numeric(value) for field, value in record['fields'].items()}
        for kind, fields in FIELDS.items():
            group = self.groups.setdefault((key, route, kind), dict(
                product=key, route=route, source=source, kind=kind,
                rows=0, present_rows=0, valid_rows=0, first=None, last=None,
                valid_first=None, valid_last=None, last_acquired_at=None,
                last_receipt_at=None, last_indexed_at=utc(indexed_at),
                original_acquisition_unknown=False, dates=set(), contracts=set(),
                evidence_shas=set(), completeness='observed_only',
                expected_contract_days=None,
                limitations='已观察字段范围，非挂牌全集或连续覆盖证明；零值/异常与缺失另列；不扩大回测准入。'))
            group['rows'] += 1
            group['original_acquisition_unknown'] |= uncertain
            if received:
                group['last_receipt_at'] = max(group['last_receipt_at'] or received, received)
                if not uncertain:
                    group['last_acquired_at'] = max(group['last_acquired_at'] or received, received)
            sha = evidence.get('sha256')
            if sha:
                group['evidence_shas'].add(sha)
            if not all(values.get(field) is not None for field in fields):
                continue
            group['present_rows'] += 1
            group['first'] = min(group['first'] or day, day)
            group['last'] = max(group['last'] or day, day)
            group['dates'].add(day)
            group['contracts'].add(record['native'])
            valid = all(values[field] >= 0 for field in fields)
            if kind in {'settlement', 'pre_settlement'}:
                valid = all(values[field] > 0 for field in fields)
            if kind == 'ohlc':
                o, h, l, c = (values[field] for field in fields)
                valid = 0 < l <= min(o, c) <= max(o, c) <= h
            if valid:
                group['valid_rows'] += 1
                group['valid_first'] = min(group['valid_first'] or day, day)
                group['valid_last'] = max(group['valid_last'] or day, day)

    def finish(self):
        result = []
        for _, raw in sorted(self.groups.items()):
            group = dict(raw)
            group['observed_days'] = len(group.pop('dates'))
            group['contracts'] = len(group['contracts'])
            group['evidence_shas'] = sorted(group['evidence_shas'])
            group['evidence_count'] = len(group['evidence_shas'])
            group['evidence_set_sha256'] = hashlib.sha256('\n'.join(group['evidence_shas']).encode()).hexdigest()
            group['evidence_shas'] = group['evidence_shas'][:3]
            group['missing_rows'] = group['rows'] - group['present_rows']
            group['invalid_or_zero_price_rows'] = group['present_rows'] - group['valid_rows']
            if group['present_rows']:
                result.append(group)
        return result


def attach(data, inventory):
    if inventory['strict_backtest_admission_expanded'] is not False:
        raise ValueError('inventory_admission_promotion')
    keys = {p['key'] for p in data['products']}
    unexpected = {r['product'] for r in inventory['datasets']} - keys
    if unexpected:
        raise ValueError('inventory_unmapped_products:' + ','.join(sorted(unexpected)))
    data = dict(data, project_inventory=inventory)
    from usage_readiness import assess
    data['products'] = [dict(p, datasets=[r for r in inventory['datasets'] if r['product'] == p['key']]) for p in data['products']]
    data['products'] = [dict(p,consumer_premarket=inventory.get('consumer_premarket',{}).get('products',{}).get(p['key'])) for p in data['products']]
    quality=inventory.get('history_quality',{}).get('by_product',{})
    data['products']=[dict(p,extension_quality=quality.get(p['key'])) for p in data['products']]
    data['products'] = [dict(p,usage_readiness=assess(p)) for p in data['products']]
    extension=inventory.get('history_extension')
    if extension:
        if extension['strict_backtest_admission_expanded'] is not False:raise ValueError('extension_scope_promotion')
        base=extension['base_summary']
        if any(base[k]!=data['summary'][k] for k in ('candidates','rows','states')):raise ValueError('extension_base_totals')
        symbols=set();states=dict(data['summary']['states']);new_rows=0;by_product={}
        for entry in extension['entries']:
            key=entry['exchange']+'.'+entry['product']
            if entry['symbol'] in symbols or key not in keys or entry['previous_state']!='not_requested_on_directory_route' or entry['financial_eligible'] is not False:raise ValueError('extension_identity_or_prior_state')
            symbols.add(entry['symbol']);state=entry['state']
            if state not in {'nonempty','empty_unavailable','failed','not_run'}:raise ValueError('extension_state')
            if state=='empty_unavailable' and (entry['rows']!=0 or entry['counts_as_success'] is not False or entry['counts_as_coverage'] is not False):raise ValueError('extension_empty_promotion')
            if state!='not_run':
                states['not_requested_on_directory_route']-=1
                states[state]=states.get(state,0)+1
            new_rows+=entry['rows'];group=by_product.setdefault(key,{})
            group[state]=group.get(state,0)+1
        if new_rows!=extension['rows'] or states['not_requested_on_directory_route']<0:raise ValueError('extension_row_totals')
        data['research_union']=dict(candidates=base['candidates'],rows=base['rows']+new_rows,states=states,base_preserved=True)
        data['products']=[dict(p,extension_availability=by_product.get(p['key'])) for p in data['products']]
    return data


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    raise SystemExit('此模块由全项目资料采集器导入')
