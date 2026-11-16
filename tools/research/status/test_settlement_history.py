"""结算预热的身份、日期、冲突和字节门禁正反向测试。"""
import io
import sys
import hashlib
import json
from pathlib import Path
import tempfile
import unittest

from settlement_history import build, current_symbol, locked, select_records


class SettlementHistoryTest(unittest.TestCase):
    def test_plan_scope_and_existing_output_refused_before_input_reads(self):
        with tempfile.TemporaryDirectory() as d:
            path=Path(d)
            with self.assertRaisesRegex(ValueError,'^warmup_plan_scope$'):
                build(dict(schema='official-settlement-warmup-plan-v1',network_requests=1),path,path)
            with self.assertRaisesRegex(ValueError,'^warmup_requires_full_existing_sequence$'):
                build(dict(schema='official-settlement-warmup-plan-v1',network_requests=0,lookback_observations=64),path,path)
            with self.assertRaisesRegex(ValueError,'^warmup_output_exists$'):
                build(dict(schema='official-settlement-warmup-plan-v1',network_requests=0,lookback_observations=None),path,path)

    def row(self, day=20261113, settle='1,000.00'):
        return dict(exchange='CZCE', native='TA701', day=day, fields=dict(settle=settle),
                    evidence=dict(id='czce-20261113', sha256='a'*64))

    def lines(self, *rows):
        return [(json.dumps(dict(record=r))+'\n').encode() for r in rows]

    def test_explicit_lifecycle_never_maps_the_old_decade_or_unlisted_day(self):
        bounds={'TA701':('CZCE.TA2701',20260116,20270115)}
        self.assertEqual(current_symbol(self.row(),bounds),'CZCE.TA2701')
        self.assertIsNone(current_symbol(self.row(day=20210104),bounds))
        self.assertIsNone(current_symbol(self.row(day=20260115),bounds))
        self.assertIsNone(current_symbol(self.row(),{'TA701':('CZCE.TA2701',0,20270115)}))
        self.assertIsNone(current_symbol(self.row(),{}))

    def test_complete_sequence_zero_and_overlap_are_not_fabricated(self):
        bounds={'TA701':('CZCE.TA2701',20260116,20270115)}
        lines=self.lines(self.row(day=20260116),self.row(day=20261113),
                         self.row(day=20261113,settle='1000'),self.row(day=20261112,settle='0'),
                         self.row(day=20261117))
        rows,stats=select_records(lines,{'CZCE.TA2701'},bounds,20210101,20261113)
        self.assertEqual(set(rows),{('CZCE.TA2701',20260116),('CZCE.TA2701',20261113)})
        self.assertEqual(stats['invalid_settlement_rows'],1)
        self.assertEqual(stats['same_value_overlap'],1)
        self.assertEqual(stats['rows_checked'],5)
        self.assertEqual(stats['records_sha256'],hashlib.sha256(b''.join(lines)).hexdigest())

    def test_source_conflict_and_line_budget_stop_instead_of_overwriting(self):
        bounds={'TA701':('CZCE.TA2701',20260116,20270115)}
        with self.assertRaisesRegex(ValueError,'^warmup_conflicting_settlement:CZCE.TA2701:20261113$'):
            select_records(self.lines(self.row(),self.row(settle='1001')),{'CZCE.TA2701'},bounds,20210101,20261113)
        with self.assertRaisesRegex(ValueError,'^warmup_record_line_budget$'):
            select_records([b' '*65537],set(),{},20210101,20261113)

    def test_original_bytes_are_bound_before_parsing(self):
        with tempfile.TemporaryDirectory() as d:
            p=Path(d)/'original.body';p.write_bytes(b'raw1');sha=hashlib.sha256(p.read_bytes()).hexdigest()
            self.assertEqual(locked(p,sha),b'raw1')
            p.write_bytes(b'raw2')
            with self.assertRaisesRegex(ValueError,'^warmup_input_sha:'):
                locked(p,sha)


if __name__=='__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
