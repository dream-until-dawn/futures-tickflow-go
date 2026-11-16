"""本机计划与可移植再表述明确分开；行情与父身份不能被伪装。"""
import io
import sys
import copy
import hashlib
import json
import unittest

from portable_parent import project


class PortableParentTest(unittest.TestCase):
    def fixture(self):
        body=b'original unit fixture';digest=hashlib.sha256(body).hexdigest()
        receipt=b'{}';receipt_sha=hashlib.sha256(receipt).hexdigest()
        values={'warmup-s1':(dict(role='source-original',sha256=digest,bytes=len(body),
                                 source_url='https://www.shfe.com.cn/public.json',source_day=20261113,
                                 retrieved_at=None,version='原始HTTP时间未知'),body),
                'receipt-s1':(dict(role='acquisition-receipt',sha256=receipt_sha),receipt)}
        entry=dict(id='s1',format='shfe-json',day=20261113,file='C:/producer/input.json',
                   original_file='/host/private/original',receipt_file='../receipt.json',
                   sha256=digest,bytes=len(body),receipt_sha256=receipt_sha,
                   evidence=dict(sha256=digest))
        parent=dict(schema='all-market-existing-upstream-parse-v1',documents=[entry,dict(entry,id='not-in-this-bundle')])
        return parent,values

    def test_projection_uses_artifact_identity_and_is_not_the_original_plan(self):
        parent,values=self.fixture();before=copy.deepcopy(parent)
        result=project(parent,'c'*64,values)
        self.assertFalse(result['original_parent_plan_included'])
        self.assertEqual(result['original_parent_plan_sha256'],'c'*64)
        self.assertEqual(result['selected_documents'],1)
        self.assertEqual(result['original_documents'],2)
        entry=result['documents'][0]
        self.assertEqual(entry['original_artifact_id'],'warmup-s1')
        self.assertEqual(entry['receipt_artifact_id'],'receipt-s1')
        self.assertIsNone(entry['original_http_received_at'])
        text=json.dumps(result)
        for forbidden in ('C:/producer','/host/private','../receipt'):
            self.assertNotIn(forbidden,text)
        self.assertEqual(parent,before)

    def test_original_and_receipt_mismatches_refused(self):
        parent,values=self.fixture();values['warmup-s1'][0]['sha256']='d'*64
        with self.assertRaisesRegex(ValueError,'^portable_parent_original_identity$'):
            project(parent,'c'*64,values)
        parent,values=self.fixture();values['receipt-s1'][0]['sha256']='d'*64
        with self.assertRaisesRegex(ValueError,'^portable_parent_receipt_identity$'):
            project(parent,'c'*64,values)

    def test_reproducible_annual_parent_requires_its_real_decoder(self):
        parent,values=self.fixture();parent['documents'][0]['format']='annual-decoded-v1'
        parent['decoder_sha256']='e'*64
        with self.assertRaisesRegex(ValueError,'^portable_parent_decoder_identity$'):
            project(parent,'c'*64,values)
        values['annual-decoder']=(dict(role='derivation-code',sha256='e'*64),b'unit fixture')
        result=project(parent,'c'*64,values)
        self.assertFalse(result['documents'][0]['full_decoded_parent_included'])
        self.assertEqual(result['documents'][0]['decoder_artifact_id'],'annual-decoder')


if __name__=='__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
