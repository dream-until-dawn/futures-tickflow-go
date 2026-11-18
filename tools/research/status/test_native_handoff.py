"""交接反例必须命中原件、重复及隔离语义，而非仅验证文件存在。"""
import copy
import io
from pathlib import Path
import sys
import tempfile
import unittest

from native_handoff import checked, encoded, sha, summarize, verify


class NativeHandoffTest(unittest.TestCase):
    def row(self):
        return dict(document_id='day', state='price-readable', record=dict(
            day=20210104, native='RM101', fields=dict(open='2', close='2', settle=None,
                                                      volume='0', open_interest='3')))

    def test_missing_settlement_not_filled_from_close_and_no_listing_claim(self):
        row = self.row()
        before = copy.deepcopy(row)
        years, days = summarize([row])
        self.assertEqual(years['2021']['nonempty_settlement'], 0)
        self.assertEqual(years['2021']['volume_and_oi'], 1)
        self.assertFalse(days[0]['legal_listing_universe_verified'])
        self.assertEqual(row, before)

    def test_quarantined_ohlc_keeps_settlement_without_becoming_valid_price(self):
        row = self.row()
        row['state'] = 'quarantined'
        row['record']['fields'].update(open='0', close='0', settle='2')
        years, days = summarize([row])
        self.assertEqual(years['2021']['price_readable'], 0)
        self.assertEqual(years['2021']['quarantined'], 1)
        self.assertEqual(years['2021']['nonempty_settlement'], 1)
        self.assertEqual(days[0]['observed_candidates'][0]['state'], 'quarantined')

    def test_duplicate_contract_day_not_silently_deduplicated(self):
        with self.assertRaisesRegex(ValueError, 'native_handoff_duplicate_contract_day'):
            summarize([self.row(), self.row()])

    def test_unknown_state_is_rejected(self):
        row = self.row()
        row['state'] = 'admitted'
        with self.assertRaisesRegex(ValueError, 'native_handoff_unknown_state'):
            summarize([row])

    def test_source_hash_and_length_are_independent_guards(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'body'
            path.write_bytes(b'good')
            self.assertEqual(checked(path, sha(b'good'), 4), b'good')
            with self.assertRaisesRegex(ValueError, 'native_handoff_source_sha'):
                checked(path, sha(b'good'), 3)
            path.write_bytes(b'evil')
            with self.assertRaisesRegex(ValueError, 'native_handoff_source_sha'):
                checked(path, sha(b'good'), 4)

    def test_migration_rejects_extra_tamper_duplicate_and_path_escape(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'body').write_bytes(b'good')
            item = dict(path='body', bytes=4, sha256=sha(b'good'))
            def manifest(items):
                raw = encoded(dict(members=items))
                (root / 'manifest.json').write_bytes(raw)
                return sha(raw)
            expected = manifest([item])
            self.assertEqual(verify(root, expected), 2)
            (root / 'extra').write_bytes(b'x')
            with self.assertRaisesRegex(ValueError, 'native_handoff_member_set'):
                verify(root, expected)
            (root / 'extra').unlink()
            (root / 'body').write_bytes(b'evil')
            with self.assertRaisesRegex(ValueError, 'native_handoff_source_sha'):
                verify(root, expected)
            (root / 'body').write_bytes(b'good')
            with self.assertRaisesRegex(ValueError, 'native_handoff_member_path'):
                verify(root, manifest([item, item]))
            for name in ('../body', 'C:/body', 'dir\\body'):
                with self.subTest(name=name), self.assertRaisesRegex(ValueError, 'native_handoff_member_path'):
                    verify(root, manifest([dict(item, path=name)]))


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
