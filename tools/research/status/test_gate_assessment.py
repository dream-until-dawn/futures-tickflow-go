"""版本、实际空值和商品范围的定向正反验证。"""
import copy
import hashlib
import io
import json
from pathlib import Path
import sys
import unittest

from data_gates import assess
from gate_assessment import build


class GateAssessmentTest(unittest.TestCase):
    def test_published_assessment_recomputes_and_binds_every_input(self):
        repo = Path(__file__).resolve().parents[3]
        pointer = json.loads((repo / 'docs/data-gate-assessment-current.json').read_bytes())
        path = (repo / pointer['file']).resolve()
        self.assertTrue(path.is_relative_to(repo))
        raw = path.read_bytes()
        self.assertEqual(hashlib.sha256(raw).hexdigest(), pointer['sha256'])
        report = json.loads(raw)
        for entry in report['inputs']:
            path = (repo / entry['path']).resolve()
            self.assertTrue(path.is_relative_to(repo))
            raw = path.read_bytes()
            self.assertEqual(len(raw), entry['bytes'])
            self.assertEqual(hashlib.sha256(raw).hexdigest(), entry['sha256'])
        model = json.loads((repo / 'docs/data-status.json').read_bytes())
        facts = json.loads((repo / 'docs/data-gate-facts.json').read_bytes())
        expected = build(model, facts)
        self.assertEqual({k: v for k, v in report.items() if k != 'inputs'}, expected)
        self.assertEqual(len(report['products']), 81)
        self.assertTrue(all(not r['key'].startswith('CFFEX.') for r in report['products']))

    def test_unknown_minimum_passes_v2_but_margin_and_bound_model_remain_required(self):
        facts = dict(current_trade_day=20261117, latest_completed_day=20261116,
                     settlement_scopes={'SHFE.rb': ['SHFE.rb2701']}, daily_update_exchanges=['SHFE'],
                     current_contracts=[dict(product_key='SHFE.rb', symbol='SHFE.rb2701',
                         as_of_trade_day=20261117, effective_min_open_lots=None,
                         margin=dict(long='0.07', short='0.07', unit='ratio',
                                     basis='exchange_spec', evidence_ids=['table']))])
        product = dict(key='SHFE.rb', name='螺纹钢', exchange='SHFE', datasets=[dict(
            kind='ohlc', valid_rows=1000, valid_first='2021-01-04', valid_last='2026-11-16',
            valid_days_by_year={str(y): 100 for y in range(2021, 2027)})])
        product['user_data_gates'] = assess(product, facts)
        model = dict(products=[product, dict(key='CFFEX.IF', exchange='CFFEX')])
        result = build(model, facts)
        self.assertEqual(result['summary'], dict(products=1, backtest=1, daily_signal=1, both=1))
        self.assertEqual(result['products'][0]['next_stage'], 'supplementary')
        self.assertFalse(result['engine_acceptance'])
        self.assertIsNone(facts['current_contracts'][0]['effective_min_open_lots'])
        changed = copy.deepcopy(facts)
        changed['current_contracts'][0]['as_of_trade_day'] = 20261116
        with self.assertRaisesRegex(ValueError, 'model_binding:SHFE.rb'):
            build(model, changed)
        product['user_data_gates'] = assess(product, changed)
        result = build(model, changed)
        self.assertEqual(result['summary']['backtest'], 0)
        self.assertEqual(result['products'][0]['next_stage'], 'backtest')
        self.assertTrue(result['products'][0]['existing_signal_capability'])
        with self.assertRaisesRegex(ValueError, 'duplicate_product'):
            build(dict(products=[product, product]), changed)


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
