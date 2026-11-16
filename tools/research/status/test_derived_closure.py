"""引用闭包只减未使用中间镜像；用途数据和现有预算必须继续受守卫保护。"""
import io
import sys
import copy
import json
import unittest
from unittest.mock import patch

from derived_closure import sha, transform


class DerivedClosureTest(unittest.TestCase):
    def fixture(self):
        values={}
        def add(id,role,path,raw):
            values[id]=(dict(id=id,role=role,path=path,bytes=len(raw),sha256=sha(raw)),raw)
        add('raw','source-original','original.body',b'unit-fixture-raw-only')
        add('unused-raw','source-original','other.body',b'unit-fixture-other-raw')
        add('data','daily-records','daily.jsonl',b'{"source_id":"raw","settle":"532.70000000000005"}\n')
        add('decoded','derived-source','decoded.json',b'{"unused":"'+b'x'*4096+b'"}')
        add('annual-decoder','derivation-code','decode.py',b'# synthetic test fixture\n')
        add('history-parent-plan','review-evidence','parent.json',b'{}')
        add('history-row-lineage','review-evidence','lineage.jsonl',b'{"source_id":"raw","decoded_source_id":"decoded","member":"sheet.xlsx","row":14282}\n')
        manifest=dict(schema='unit-fixture-only',synthetic=True,policy=dict(target=20261116),
                      current_contracts=[],products=[dict(gates=dict(signal=dict(hard_items=dict(
                          settlement=dict(evidence_ids=['data'],scope=['unit.symbol'])))))],
                      artifacts=[item for item,_ in values.values()])
        return manifest,values

    def test_actual_content_reduction_preserves_originals_prices_and_gates(self):
        manifest,values=self.fixture();before=copy.deepcopy(manifest)
        with patch('derived_closure.JSON_LIMIT',2048):
            result,kept=transform(manifest,values,{'decoded'},{'decoded':'raw'})
        self.assertNotIn('decoded',kept)
        for id in ('raw','unused-raw','data'):
            self.assertEqual(kept[id][1],values[id][1])
        self.assertEqual(result['policy'],before['policy'])
        self.assertEqual(result['products'],before['products'])
        self.assertEqual(manifest,before)
        lineage=json.loads(kept['history-row-lineage'][1])
        self.assertIsNone(lineage['decoded_source_id'])
        self.assertEqual(lineage['row'],14282)
        parents=json.loads(kept['unbundled-derived-parents'][1])['parents']
        self.assertEqual(parents[0]['sha256'],values['decoded'][0]['sha256'])

    def test_required_derived_or_price_references_cannot_be_removed(self):
        manifest,values=self.fixture()
        manifest['products'][0]['gates']['signal']['hard_items']['settlement']['evidence_ids'].append('decoded')
        with self.assertRaisesRegex(ValueError,'^closure_required_reference_drop$'):
            transform(manifest,values,{'decoded'},{'decoded':'raw'})
        manifest,values=self.fixture()
        with self.assertRaisesRegex(ValueError,'^closure_required_reference_drop$'):
            transform(manifest,values,{'raw'},{'raw':'raw'})
        with self.assertRaisesRegex(ValueError,'^closure_only_unused_derived_sources$'):
            transform(manifest,values,{'unused-raw'},{'unused-raw':'raw'})

    def test_remaining_oversize_and_missing_regeneration_still_fail(self):
        manifest,values=self.fixture()
        with patch('derived_closure.JSON_LIMIT',2048):
            with self.assertRaisesRegex(ValueError,'^closure_json_freeze_budget:decoded$'):
                transform(manifest,values,set(),{})
        del values['annual-decoder']
        with self.assertRaisesRegex(ValueError,'^closure_missing_regeneration_chain$'):
            transform(manifest,values,{'decoded'},{'decoded':'raw'})

    def test_missing_price_source_cannot_be_hidden_by_repackaging(self):
        manifest,values=self.fixture()
        item,raw=values['data'];raw=raw.replace(b'"raw"',b'"missing-source"')
        item.update(bytes=len(raw),sha256=sha(raw));values['data']=(item,raw)
        with self.assertRaisesRegex(ValueError,'^closure_missing_required_reference$'):
            transform(manifest,values,{'decoded'},{'decoded':'raw'})


if __name__=='__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
