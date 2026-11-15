"""业务反例直接改文本，避免只被SHA错误拦住而未覆盖业务核查。"""
import hashlib
import io
import json
import sys
import tempfile
import unittest
from pathlib import Path

import review


class SourceReviewTest(unittest.TestCase):
    def test_annual_positive_and_changed_holiday(self):
        text = '<html>2023-12-27 2023年12月26日' + ''.join(''.join(row[1:]) for row in review.HOLIDAYS) + '</html>'
        self.assertEqual(review.annual_review(text.encode())['holidays_checked'], 7)
        self.assertFalse(review.annual_review(text.encode())['calendar_qualified'])
        for name, clause, *_ in review.HOLIDAYS:
            with self.subTest(holiday=name):
                with self.assertRaisesRegex(ValueError, 'annual_clause:' + name):
                    review.annual_review(text.replace(clause, clause.replace('休市', '开市')).encode())

    def vendor(self):
        header = review.MINUTE_HEADER
        rows = '\n'.join(f'DCE,A2605,豆一2605,2026-03-13,2026-03-12 21:0{i}:00,1,1,1,1,1,1,1,1' for i in (1, 2))
        main = rows.replace('A2605', 'A88')
        ticks = 'symbol,code,name,time,volume\nA2607.XDCE,a2607,豆一,20260529210000,713\nA2607.XDCE,a2607,豆一,20260529210000,1013'
        return f'<html>A99 豆一指数连续 A888 价差平滑<pre>{header}\n{rows}</pre><pre>{header}\n{main}</pre><pre>{ticks}</pre></html>'

    def test_vendor_examples_are_not_market_dataset(self):
        text = self.vendor()
        facts = review.vendor_review(text.encode())
        self.assertEqual(facts['same_second_tick_rows'], 2)
        self.assertFalse(facts['market_dataset_obtained'])
        self.assertFalse(facts['financial_eligible'])
        for bad, reason in [(text.replace('A2605', 'A88'), 'vendor_contract_and_main_examples'),
                            (text.replace('2026-03-13', '2026-03-12'), 'vendor_night_trading_date'),
                            (text.replace('A888', 'A999'), 'vendor_synthetic_series_identity')]:
            with self.subTest(reason=reason):
                with self.assertRaisesRegex(ValueError, reason):
                    review.vendor_review(bad.encode())

    def test_cy_dates_encoding_and_hours(self):
        text = '<html>2019年12月18日交易提示 2019-12-17 16:30 自2019年12月11日当晚起调整夜盘交易时间 全部夜盘交易品种夜盘交易时间为21:00-23:00</html>'
        facts = review.cy_review(text.encode('gb18030'))
        self.assertEqual(facts['asserted_effective_evening'], 20191211)
        self.assertFalse(facts['full_product_scope_certified'])
        for old, new in [('12月11日', '12月18日'), ('23:00', '23:30')]:
            with self.assertRaisesRegex(ValueError, 'cy_change_clause'):
                review.cy_review(text.replace(old, new).encode('gb18030'))

    def test_partial_wrong_identity_and_mutated_source_are_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            raw = b'<html>synthetic</html>'
            entry = dict(id='fixture', url='https://example.invalid/document', bytes=len(raw), sha256=hashlib.sha256(raw).hexdigest())
            receipt = dict(evidence=dict(id='fixture', url=entry['url'], sha256=entry['sha256']), bytes=len(raw), state='received', body_state='complete', http=dict(status=200, complete=True, final_url=entry['url']))
            body, meta = root / 'fixture.body', root / 'fixture.json'
            body.write_bytes(raw)
            meta.write_text(json.dumps(receipt), encoding='utf-8')
            self.assertEqual(review.load_locked(root, entry), raw)
            receipt['body_state'] = 'partial'
            meta.write_text(json.dumps(receipt), encoding='utf-8')
            with self.assertRaisesRegex(ValueError, 'source_not_complete_200'):
                review.load_locked(root, entry)
            receipt['body_state'] = 'complete'
            receipt['evidence']['url'] += '/wrong'
            meta.write_text(json.dumps(receipt), encoding='utf-8')
            with self.assertRaisesRegex(ValueError, 'receipt_identity'):
                review.load_locked(root, entry)
            body.write_bytes(raw + b'x')
            with self.assertRaisesRegex(ValueError, 'locked_source_bytes'):
                review.load_locked(root, entry)


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
