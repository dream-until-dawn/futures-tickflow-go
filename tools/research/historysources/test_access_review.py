"""用时间/字段业务变异反控，避免把文档示例的局部一致性提升为获证数据。"""
import io
import json
import sys
import unittest

import access_review


class AccessReviewTest(unittest.TestCase):
    def fixture(self):
        current = dict(timestamp=1786936479, datetime='2026-08-17 11:14:39', minute='11:15', money=674100)
        response = dict(code='00000', data=[dict(symbol='stock.sz.000001', current=current)])
        home = b'<html>public docs</html>'
        historical = b'<html>x-api-key POST /future-contract/minute</html>'
        realtime = ('<html><pre>' + json.dumps(response) + '</pre>K线数据字段说明 amount float 当前成交额 响应压缩</html>').encode()
        return home, historical, realtime

    def test_timestamp_example_is_local_evidence_only(self):
        current = dict(timestamp=1786936479, datetime='2026-08-17 11:14:39', minute='11:15')
        result = access_review.example_time(current)
        self.assertTrue(result['consistent_with_utc8'])
        self.assertEqual(result['label_minus_event_seconds'], 21)
        self.assertFalse(result['closed_bar_certified'])
        # 单独改UTC秒数，必须出现不一致；不能把时区假设当成解析结果硬编码。
        current['timestamp'] += 3600
        self.assertFalse(access_review.example_time(current)['consistent_with_utc8'])

    def test_public_link_and_key_are_not_data_license(self):
        inputs = self.fixture()
        result = access_review.access_review(*inputs)
        self.assertTrue(result['historical_api']['key_required'])
        self.assertTrue(result['realtime_example']['example_money_key'])
        self.assertTrue(result['realtime_example']['table_amount_key'])
        self.assertIsNone(result['futures_time_units']['volume_unit'])
        changed = (b'<html><a href="/license">Terms</a></html>', *inputs[1:])
        result = access_review.access_review(*changed)
        self.assertEqual(result['inspected_legal_link_candidates'], ['/license'])
        self.assertFalse(result['data_use_grant_verified'])
        self.assertFalse(result['concrete_contract_sample_obtained'])

    def test_missing_access_marker_or_replaced_example_rejected(self):
        home, historical, realtime = self.fixture()
        with self.assertRaisesRegex(ValueError, 'historical_access_schema'):
            access_review.access_review(home, historical.replace(b'x-api-key', b'public'), realtime)
        with self.assertRaisesRegex(ValueError, 'realtime_document_example'):
            access_review.access_review(home, historical, realtime.replace(b'stock.sz.000001', b'future.A88'))


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
