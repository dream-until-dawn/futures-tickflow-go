"""用分层样本验证文档不把样本相加、无参考或空响应当成完整和准入。"""
import copy
import io
import hashlib
import json
import sys
import tempfile
import unittest
from pathlib import Path
from page_model import model, profile_ranges
from generate import source_cells, markdown, render_html, locked_repo, check_docs, check_rendered


class StatusModelTest(unittest.TestCase):
    def fixture(self):
        common=dict(financial_eligible=False,lifecycle_certified=False,official_listing_day=None,official_last_trading_day=None)
        rows=[dict(**common,symbol='CZCE.MA2101',exchange='CZCE',product='MA',availability=dict(state='nonempty',rows=2,first_observed_day=20210104,last_observed_day=20210105,source_id='czce'),reference_review=dict(reference_days=1,unresolved_distinct_fields=1,calendar_unknown_actual_days=2)),
              dict(**common,symbol='DCE.m2105',exchange='DCE',product='m',availability=dict(state='not_requested_on_directory_route'))]
        catalog=dict(full_market_universe_certified=False,strict_backtest_admission_expanded=False,contracts=rows,summary=dict(candidates=2,rows=2,states=dict(nonempty=1,not_requested_on_directory_route=1)))
        report=dict(rows=2,first_day=20210104,last_day=20210105,financial_eligible=False,official_days=1,overlapping_days=1,missing_official_days=[],official_unpriced_fields=[],binary64_distinct_differences=1,binary64_equivalent_differences=0,calendar_unknown_actual_days=[20210104,20210105])
        sina=[dict(code='M2701',exchange='DCE',daily=dict(raw_rows=4,raw_first='2026-01-01',raw_last='2026-01-06',financial_eligible=False))]
        legacy=dict(results={'DCE.m2105':dict(rows=3,first_day='2021-01-04',last_day='2021-01-06',differences=[{}])})
        labels={k:dict(name=k,short=k,type='显示') for k in ['SHFE','INE','CZCE','DCE','GFEX','CFFEX']};labels['product_display_aliases']={}
        return [catalog,dict(czce={'CZCE.MA2101':report}),{},[],sina,legacy,labels,'2026-11-15']

    def test_other_samples_never_increase_primary_rows_or_reference_coverage(self):
        data=model(*self.fixture());p=next(p for p in data['products'] if p['key']=='DCE.m')
        self.assertEqual(data['summary']['rows'],2)
        self.assertEqual(p['rows'],0);self.assertEqual(p['state'],'sample_only')
        self.assertEqual(len(p['supplementary']),2);self.assertEqual(p['reference_days'],0)
        self.assertFalse(p['financial_eligible'])
        self.assertIn('2候选',markdown(data));self.assertNotIn('5,351',markdown(data))

    def test_source_matrix_does_not_turn_empty_or_unknown_into_data(self):
        data=model(*self.fixture());p=next(p for p in data['products'] if p['key']=='DCE.m')
        cells=source_cells(p,data['exchanges'])
        self.assertEqual(cells[0],['天勤 EDB（样本）','新浪（样本）'])
        self.assertEqual(cells[3:], [['新浪（样本）'],[],[]])
        p['supplementary']=[];p['empty_unavailable']=1
        self.assertEqual(source_cells(p,data['exchanges']),[[],[],[],[],[],[]])
        p['reference_days']=1;p['exchange']='CFFEX'
        self.assertEqual(source_cells(p,data['exchanges'])[3],['中金所（明文原表列）'])
        p['reference_days']=0
        p['supplementary']=[dict(source='新浪当期列表日线样本',rows=2,absent_settlement=1,zero_settlement=1)]
        self.assertEqual(source_cells(p,data['exchanges'])[3],[])
        del p['supplementary'][0]['absent_settlement']
        self.assertEqual(source_cells(p,data['exchanges'])[3],[])
        p['supplementary'][0]['absent_settlement']=1
        p['supplementary'][0]['rows']=3
        self.assertEqual(source_cells(p,data['exchanges'])[3],['新浪（样本）'])

    def test_missing_contract_counter_and_scope_mutations_are_rejected(self):
        args=self.fixture();args[0]['summary']['rows']=99
        with self.assertRaisesRegex(ValueError,'^status_catalog_totals$'):model(*args)
        args=self.fixture();args[0]['contracts'].append(copy.deepcopy(args[0]['contracts'][0]))
        with self.assertRaisesRegex(ValueError,'^status_contract_scope_or_duplicate$'):model(*args)
        args=self.fixture();args[0]['contracts'][0]['financial_eligible']=True
        with self.assertRaisesRegex(ValueError,'^status_contract_scope_or_duplicate$'):model(*args)

    def test_empty_coverage_and_supplement_scope_cannot_be_promoted(self):
        args=self.fixture();args[0]['contracts'][1]['availability']=dict(state='empty_unavailable',rows=0,counts_as_success=False,counts_as_coverage=True,raw_receipt_state='failed')
        args[0]['summary']['states']=dict(nonempty=1,empty_unavailable=1)
        with self.assertRaisesRegex(ValueError,'^status_empty_promotion$'):model(*args)
        args=self.fixture();args[4][0]['daily']['financial_eligible']=True
        with self.assertRaisesRegex(ValueError,'^status_supplement_scope$'):model(*args)

    def test_reference_and_duplicate_supplementary_identity_are_bound(self):
        args=self.fixture();args[1]['czce']['CZCE.MA2101']['official_days']=2
        with self.assertRaisesRegex(ValueError,'^status_report_totals$'):model(*args)
        args=self.fixture();args[4].append(copy.deepcopy(args[4][0]))
        with self.assertRaisesRegex(ValueError,'^status_sina_duplicate$'):model(*args)

    def test_profile_gap_and_overlap_are_not_silently_joined(self):
        p=dict(series=[dict(product=dict(Exchange='INE',Product='lu'),records=[dict(day=20210101,sessions=[]),dict(day=20210103,sessions=[])])])
        with self.assertRaisesRegex(ValueError,'^status_profile_dates$'):profile_ranges([('a',p)])
        p['series'][0]['records']=p['series'][0]['records'][:1]
        with self.assertRaisesRegex(ValueError,'^status_profile_overlap$'):profile_ranges([('a',p),('b',p)])

    def test_embedded_names_cannot_close_json_script(self):
        data=model(*self.fixture());data['products'][0]['name']='</script><script>alert(1)</script>'
        html=render_html(data,Path(__file__).parent/'assets')
        self.assertNotIn('</script><script>alert(1)',html)
        self.assertIn('\\u003c/script',html)
        self.assertIn('connect-src',html)

    def test_repo_metadata_same_length_tamper_is_rejected(self):
        with tempfile.TemporaryDirectory() as d:
            root=Path(d);p=root/'input.json';p.write_bytes(b'{}')
            item=dict(file=p.name,sha256=hashlib.sha256(p.read_bytes()).hexdigest())
            self.assertEqual(locked_repo(root,item),{})
            p.write_bytes(b'[]')
            with self.assertRaisesRegex(ValueError,'^status_repo_sha:input.json$'):locked_repo(root,item)

    def test_published_docs_reproduce_without_private_bundle(self):
        repo=Path(__file__).resolve().parents[3]
        config=json.loads((Path(__file__).parent/'sources.json').read_bytes())
        self.assertEqual(check_docs(repo,config)['products'],89)

    def test_manual_html_edit_and_changed_published_model_are_rejected(self):
        data=model(*self.fixture())
        with tempfile.TemporaryDirectory() as d:
            repo=Path(d);(repo/'docs').mkdir()
            (repo/'docs/data-status.md').write_text(markdown(data),encoding='utf-8',newline='\n')
            (repo/'docs/data-status.html').write_text(render_html(data,Path(__file__).parent/'assets'),encoding='utf-8',newline='\n')
            check_rendered(repo,data)
            with (repo/'docs/data-status.html').open('a',encoding='utf-8') as f:f.write('manual edit')
            with self.assertRaisesRegex(ValueError,'^status_render_drift:data-status.html$'):check_rendered(repo,data)
            raw=b'{}';(repo/'docs/data-status.json').write_bytes(b'[]')
            with self.assertRaisesRegex(ValueError,'^status_published_model_sha$'):check_docs(repo,dict(published_model_sha256=hashlib.sha256(raw).hexdigest()))


if __name__=='__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
