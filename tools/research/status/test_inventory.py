"""逐列边界、时间来源、重复和身份失败必须实际被识别。"""
import copy
import hashlib
import io
import json
from pathlib import Path
import sys
import tempfile
import tarfile
import unittest

from collect_inventory import Collector
from inventory_model import Accumulator, attach
from snapshot_inventory import verified_members


class InventoryTest(unittest.TestCase):
    def record(self, day, **fields):
        return dict(exchange='SHFE',native='rb2610',day=day,fields=fields,
                    evidence=dict(retrieved_at='2026-11-15T08:00:00Z',sha256='a'*64))

    def test_each_field_has_its_own_dates_and_zero_settlement_is_not_usable(self):
        stats=Accumulator()
        stats.add('a','源',self.record(20260105,open=1,high=2,low=1,close=2,settle=0,volume=0))
        stats.add('a','源',self.record(20260106,open=2,high=3,low=1,close=2,settle=2,volume=4))
        stats.add('a','源',self.record(20260107,open=2,high=3,low=1,close=2,settle=None,volume=5))
        rows={r['kind']:r for r in stats.finish()}
        self.assertEqual(rows['ohlc']['last'],'2026-01-07')
        self.assertEqual(rows['settlement']['last'],'2026-01-06')
        self.assertEqual(rows['settlement']['valid_first'],'2026-01-06')
        self.assertEqual(rows['settlement']['invalid_or_zero_price_rows'],1)
        self.assertEqual(rows['settlement']['missing_rows'],1)
        self.assertEqual(rows['volume']['valid_rows'],3)
        self.assertNotIn('turnover',rows)
        self.assertIsNone(rows['ohlc']['expected_contract_days'])
        self.assertEqual(rows['ohlc']['valid_days_by_year'], {'2026':3})
        self.assertEqual(rows['settlement']['valid_days_by_year'], {})

    def test_duplicate_same_route_rejected_other_source_kept_separate(self):
        stats=Accumulator();row=self.record(20260105,volume=1)
        stats.add('a','源A',row)
        with self.assertRaisesRegex(ValueError,'^inventory_duplicate_record$'):stats.add('a','源A',row)
        stats.add('b','源B',row)
        self.assertEqual(len(stats.finish()),2)

    def test_retained_receipt_and_missing_time_never_become_download_time(self):
        stats=Accumulator();row=self.record(20260105,volume=1)
        row['evidence']['version']='retained local receipt event; first original HTTP UTC unresolved'
        stats.add('a','源',row,'2026-11-16T09:00:00Z')
        r=stats.finish()[0]
        self.assertIsNone(r['last_acquired_at'])
        self.assertTrue(r['original_acquisition_unknown'])
        self.assertEqual(r['last_receipt_at'],'2026-11-15T08:00:00+00:00')
        row=self.record(20260106,volume=1);row['evidence']={}
        stats.add('b','源',row)
        self.assertIsNone(stats.finish()[1]['last_receipt_at'])

    def test_tampered_catalog_and_mismatched_count_rejected(self):
        with tempfile.TemporaryDirectory() as d:
            root=Path(d);record=self.record(20260105,volume=1)
            raw=(json.dumps(dict(record=record))+'\n').encode()
            (root/'records.jsonl').write_bytes(raw)
            meta=dict(records_sha256=hashlib.sha256(raw).hexdigest(),rows=1,at_utc='2026-11-16T09:00:00Z')
            manifest=root/'manifest.json';manifest.write_text(json.dumps(meta))
            c=Collector(root);c.catalog(manifest)
            self.assertEqual(c.stats.finish()[0]['present_rows'],1)
            # 同长度且仍是合法JSON，确保真正在测SHA门禁而非解析器。
            tampered=raw.replace(b'"volume": 1',b'"volume": 2')
            self.assertNotEqual(raw,tampered)
            self.assertEqual(len(raw),len(tampered))
            (root/'records.jsonl').write_bytes(tampered)
            with self.assertRaisesRegex(ValueError,'^inventory_catalog_sha$'):Collector(root).catalog(manifest)
            (root/'records.jsonl').write_bytes(raw);meta['rows']=2;manifest.write_text(json.dumps(meta))
            with self.assertRaisesRegex(ValueError,'^inventory_catalog_count$'):Collector(root).catalog(manifest)

    def test_unknown_product_or_admission_promotion_cannot_disappear(self):
        data=dict(products=[dict(key='SHFE.rb')])
        inventory=dict(strict_backtest_admission_expanded=False,datasets=[dict(product='SHFE.rb')])
        self.assertEqual(len(attach(data,inventory)['products'][0]['datasets']),1)
        bad=copy.deepcopy(inventory);bad['datasets'][0]['product']='DCE.m'
        with self.assertRaisesRegex(ValueError,'^inventory_unmapped_products:'):attach(data,bad)
        bad=copy.deepcopy(inventory);bad['strict_backtest_admission_expanded']=True
        with self.assertRaisesRegex(ValueError,'^inventory_admission_promotion$'):attach(data,bad)

    def test_extension_union_keeps_base_and_rejects_empty_promotion(self):
        base=dict(candidates=5,rows=10,states=dict(nonempty=1,empty_unavailable=1,not_requested_on_directory_route=3))
        data=dict(summary=base,products=[dict(key='CZCE.AP')])
        entries=[dict(symbol='CZCE.AP2401',exchange='CZCE',product='AP',previous_state='not_requested_on_directory_route',financial_eligible=False,state='nonempty',rows=2),
                 dict(symbol='CZCE.AP2403',exchange='CZCE',product='AP',previous_state='not_requested_on_directory_route',financial_eligible=False,state='empty_unavailable',rows=0,counts_as_success=False,counts_as_coverage=False)]
        extension=dict(base_summary=base,entries=entries,rows=2,strict_backtest_admission_expanded=False)
        inventory=dict(strict_backtest_admission_expanded=False,datasets=[],history_extension=extension)
        result=attach(data,inventory)
        self.assertEqual(result['summary'],base)
        self.assertEqual(result['research_union']['states'],dict(nonempty=2,empty_unavailable=2,not_requested_on_directory_route=1))
        self.assertEqual(result['research_union']['rows'],12)
        bad=copy.deepcopy(inventory);bad['history_extension']['entries'][1]['counts_as_success']=True
        with self.assertRaisesRegex(ValueError,'extension_empty_promotion'):attach(data,bad)
        bad=copy.deepcopy(inventory);bad['history_extension']['entries'][0]['previous_state']='nonempty'
        with self.assertRaisesRegex(ValueError,'extension_identity_or_prior_state'):attach(data,bad)

    def test_snapshot_checks_external_identity_and_each_member(self):
        body=b'{"MainDays":[]}'
        manifest=json.dumps(dict(files={'inputs/main.json':dict(bytes=len(body),sha256=hashlib.sha256(body).hexdigest())})).encode()
        def archive(value):
            out=io.BytesIO()
            with tarfile.open(fileobj=out,mode='w:gz') as tar:
                for name,raw in [('state/inputs/main.json',value),('export-manifest.json',manifest)]:
                    info=tarfile.TarInfo(name);info.size=len(raw);tar.addfile(info,io.BytesIO(raw))
            return out.getvalue()
        raw=archive(body);sha=hashlib.sha256(raw).hexdigest();msha=hashlib.sha256(manifest).hexdigest()
        self.assertEqual(verified_members(raw,manifest,sha,msha)[1]['state/inputs/main.json'],body)
        with self.assertRaisesRegex(ValueError,'^inventory_snapshot_archive_sha$'):verified_members(raw,manifest,'0'*64,msha)
        # 即使重新计算容器SHA，成员仍必须对上受信清单。
        changed=archive(body.replace(b'[]',b'{}'))
        with self.assertRaisesRegex(ValueError,'^inventory_snapshot_file_sha$'):verified_members(changed,manifest,hashlib.sha256(changed).hexdigest(),msha)


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
