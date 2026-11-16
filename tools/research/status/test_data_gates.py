"""硬门槛正反向、范围交集及实际最低开仓的边界控制。"""
import io
import sys
import unittest
import hashlib
import json
from pathlib import Path
from data_gates import current_margin, effective_minimum, evaluate, item, assess


class DataGatesTest(unittest.TestCase):
    def test_post_2021_listing_uses_direct_listing_witness(self):
        facts=dict(current_trade_day=20261117,latest_completed_day=20261116,current_contracts=[],
                   settlement_scopes={},daily_update_exchanges=[])
        product=dict(key='SHFE.ao',datasets=[dict(kind='ohlc',valid_rows=600,valid_first='2023-06-19',
                     valid_last='2026-11-16',valid_days_by_year={str(y):100 for y in range(2023,2027)})])
        self.assertEqual(assess(product,facts)['backtest']['hard_items']['history_ohlc']['status'],'pending')
        facts['listing_days']={'SHFE.ao':dict(day=20230619)}
        self.assertEqual(assess(product,facts)['backtest']['hard_items']['history_ohlc']['status'],'pass')
        product['datasets'][0]['valid_first']='2023-06-20'
        self.assertEqual(assess(product,facts)['backtest']['hard_items']['history_ohlc']['status'],'pending')

    def test_report_gates_ignore_legacy_flag_but_require_actual_hard_facts(self):
        contract=dict(product_key='SHFE.rb',symbol='SHFE.rb2701',as_of_trade_day=20261117,
                      margin=dict(long='0.07',short='0.07',unit='ratio',basis='exchange_spec',evidence_ids=['rates']),
                      effective_min_open_lots=4,minimum_lots_kind='effective_min_open',
                      special_adjustments_reviewed=True,minimum_evidence_ids=['special'])
        facts=dict(current_trade_day=20261117,latest_completed_day=20261116,current_contracts=[contract],
                   settlement_scopes={'SHFE.rb':['SHFE.rb2701']},daily_update_exchanges=['SHFE'])
        product=dict(key='SHFE.rb',strict_backtest_admission_expanded=False,datasets=[dict(
            kind='ohlc',valid_rows=1200,valid_first='2021-01-04',valid_last='2026-11-16',
            valid_days_by_year={str(y):200 for y in range(2021,2027)})])
        result=assess(product,facts)
        self.assertEqual(result['backtest']['status'],'pass')
        self.assertEqual(result['daily_signal']['status'],'pass')
        contract['effective_min_open_lots']=None
        self.assertEqual(assess(product,facts)['backtest']['status'],'pending')
        self.assertEqual(assess(product,facts)['daily_signal']['status'],'pass')
        contract['effective_min_open_lots']=4
        del product['datasets'][0]['valid_days_by_year']['2023']
        self.assertEqual(assess(product,facts)['backtest']['hard_items']['history_ohlc']['status'],'pending')
        facts['settlement_scopes']={}
        self.assertEqual(assess(product,facts)['daily_signal']['status'],'pending')

    def test_interface_example_is_digest_bound_and_never_real_admission(self):
        root = Path(__file__).parent / 'examples/user-data-gates-v1'
        manifest = json.loads((root / 'manifest.json').read_bytes())
        self.assertIs(manifest['synthetic'], True)
        for artifact in manifest['artifacts']:
            path = (root / artifact['path']).resolve()
            self.assertTrue(path.is_relative_to(root.resolve()))
            raw = path.read_bytes()
            self.assertEqual(len(raw), artifact['bytes'])
            self.assertEqual(hashlib.sha256(raw).hexdigest(), artifact['sha256'])
            self.assertNotEqual(hashlib.sha256(raw+b' ').hexdigest(), artifact['sha256'])
        product = manifest['products'][0]
        for function, gate in product['gates'].items():
            self.assertEqual(gate, evaluate(gate['hard_items'], function))
            self.assertEqual(gate['status'], 'pending')
            self.assertFalse(gate['eligible_contracts'])
        row = manifest['current_contracts'][0]
        self.assertFalse(effective_minimum(row, manifest['policy']['current_trade_day']))

    def test_signal_is_independent_of_margin_minimum_and_bonus(self):
        hard = {k: item('pass', ['CZCE.RM2701'], ['original-16'], '已核')
                for k in ('settlement', 'daily_update')}
        self.assertEqual(evaluate(hard, 'daily_signal')['status'], 'pass')
        hard['daily_update'] = item(reason='仅有离线历史，尚无按日更新入口')
        self.assertEqual(evaluate(hard, 'daily_signal')['status'], 'pending')
        hard['daily_update'] = item('not_met', reason='该来源不提供逐日更新')
        self.assertEqual(evaluate(hard, 'daily_signal')['status'], 'not_met')

    def test_effective_minimum_does_not_fall_back_to_ordinary_or_block(self):
        r = dict(as_of_trade_day=20261117, ordinary_min_order_lots=1, MinBlckTrdSz=10, MaxOrdSz=500)
        self.assertFalse(effective_minimum(r, 20261117))
        r.update(effective_min_open_lots=4, minimum_lots_kind='effective_min_open',
                 special_adjustments_reviewed=True, minimum_evidence_ids=['specific-notice'])
        self.assertTrue(effective_minimum(r, 20261117))
        self.assertFalse(effective_minimum(r, 20261118))
        for value in (True, 0, -1, 1.5, '1', None):
            self.assertFalse(effective_minimum(dict(r, effective_min_open_lots=value), 20261117))
        self.assertFalse(effective_minimum(dict(r, special_adjustments_reviewed=False), 20261117))
        self.assertFalse(effective_minimum(dict(r, minimum_evidence_ids=[]), 20261117))

    def test_margin_units_and_effective_day(self):
        r = dict(as_of_trade_day=20261117, margin=dict(long='0.07', short='0.08', unit='ratio',
                 basis='exchange_spec', evidence_ids=['table-17']))
        self.assertTrue(current_margin(r, 20261117))
        self.assertFalse(current_margin(r, 20261118))
        for value in ('7%', 7, 0, 'NaN', 'Infinity', '-0.1'):
            self.assertFalse(current_margin(dict(r, margin=dict(r['margin'], long=value)), 20261117))
        self.assertFalse(current_margin(dict(r, margin=dict(r['margin'], basis='reference')), 20261117))

    def test_scope_intersection_and_history_is_not_current_contract_age(self):
        hard = dict(history_ohlc=item('pass', ['SHFE.rb2101'], ['history'], '2021年至今序列'),
                    current_margin=item('pass', ['SHFE.rb2701'], ['rates'], '已核'),
                    effective_min_open=item('pass', ['SHFE.rb2701'], ['review'], '已核'))
        self.assertEqual(evaluate(hard, 'backtest')['eligible_contracts'], ['SHFE.rb2701'])
        hard['effective_min_open'] = item('pass', ['SHFE.rb2702'], ['review'], '已核')
        self.assertEqual(evaluate(hard, 'backtest')['status'], 'pending')
        hard['effective_min_open'] = item(reason='只有普通1手基线')
        self.assertEqual(evaluate(hard, 'backtest')['status'], 'pending')
        hard['calendar'] = item()
        with self.assertRaisesRegex(ValueError, 'hard_item_set'):
            evaluate(hard, 'backtest')


if __name__ == "__main__":
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
