"""离线复算商品逐项办理账本；只交资料评估，不包含价格或注册引擎任务。"""
import argparse
import hashlib
import io
import json
from pathlib import Path
import sys

from data_gates import assess


def build(model, facts):
    """所有商品先核第一道，再决定后续阶段；已有独立信号事实不抹去。"""
    rows = []
    for product in model['products']:
        if product['exchange'] == 'CFFEX':
            continue
        gates = assess(product, facts)
        if product['user_data_gates'] != gates:
            raise ValueError('gate_assessment_model_binding:' + product['key'])
        back = gates['backtest']['status'] == 'pass'
        signal = gates['daily_signal']['status'] == 'pass'
        rows.append(dict(key=product['key'], name=product['name'], gates=gates,
                         next_stage='supplementary' if back and signal else 'daily_signal' if back else 'backtest',
                         reviewed_inputs='冻结项目逐年OHLC、具名当日保证金及日更结算事实，非逐品种新网络查询',
                         existing_signal_capability=signal,
                         actual_minimum_required_for_backtest=False))
    keys = [r['key'] for r in rows]
    if len(keys) != len(set(keys)):
        raise ValueError('gate_assessment_duplicate_product')
    return dict(schema='commodity-gate-assessment-v2', synthetic=False,
                policy=dict(gate_schema='user-data-gates-v2', baseline_year=2021,
                            current_trade_day=facts['current_trade_day'],
                            latest_completed_day=facts['latest_completed_day'],
                            backtest_hard_items=['history_ohlc', 'current_margin'],
                            daily_signal_hard_items=['settlement', 'daily_update']),
                executable_bundle=False, engine_acceptance=False,
                price_payload_included=False, tasks_registered=0,
                summary=dict(products=len(rows),
                             backtest=sum(r['gates']['backtest']['status'] == 'pass' for r in rows),
                             daily_signal=sum(r['existing_signal_capability'] for r in rows),
                             both=sum(r['next_stage'] == 'supplementary' for r in rows)),
                products=rows)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--repo', type=Path, default=Path(__file__).resolve().parents[3])
    parser.add_argument('--out', type=Path, required=True)
    args = parser.parse_args()
    paths = ['docs/data-status.json', 'docs/data-gate-facts.json',
             'tools/research/status/data_gates.py', 'tools/research/status/gate_assessment.py',
             'tools/research/status/sources.json']
    raw = {name: (args.repo / name).read_bytes() for name in paths}
    report = build(json.loads(raw[paths[0]]), json.loads(raw[paths[1]]))
    report['inputs'] = [dict(path=name, bytes=len(value), sha256=hashlib.sha256(value).hexdigest())
                        for name, value in raw.items()]
    with args.out.open('x', encoding='utf-8', newline='\n') as file:
        json.dump(report, file, ensure_ascii=False, indent=2)
        file.write('\n')
    print(json.dumps(report['summary'], ensure_ascii=False))


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    main()
