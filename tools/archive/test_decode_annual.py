"""标准库生成最小XLSX反例；不依赖真实市场、联网或额外包。"""
import io
import json
import unittest
import sys
import zipfile
from xml.sax.saxutils import escape
from decode_annual import decode, ENGLISH


def package(rows, merged='', name='official2025.1.xlsx'):
    sheet = ['<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>']
    for n, row in enumerate(rows, 1):
        cells = []
        for i, v in enumerate(row):
            if v is not None:
                cells.append(f'<c r="{chr(65+i)}{n}" t="inlineStr"><is><t>{escape(str(v))}</t></is></c>')
        sheet.append(f'<row r="{n}">'+''.join(cells)+'</row>')
    sheet.append('</sheetData>'+merged+'</worksheet>')
    inside = io.BytesIO()
    with zipfile.ZipFile(inside, 'w') as z:
        z.writestr('xl/worksheets/sheet1.xml', ''.join(sheet))
    outside = io.BytesIO()
    with zipfile.ZipFile(outside, 'w') as z:
        z.writestr(name, inside.getvalue())
    return outside.getvalue()


def row(contract='rb2505', day='20250102'):
    return [contract, day, '3000', '3001.00', '3010', '3020', '2990', '3015', '3009', '0', '0', '10', '300900', '200']


class DecodeTest(unittest.TestCase):
    def test_real_control_and_decimal_preservation(self):
        result = decode(package([ENGLISH, row()]), 2025)
        self.assertEqual(result['records'][0]['fields']['pre_settle'], '3001.00')
        self.assertEqual(result['records'][0]['row'], 2)
        self.assertEqual(result['records'][0]['turnover_unit'], 'unknown')

    def test_only_explicit_merged_contract_is_carried(self):
        rows = [ENGLISH, row(), row(None, '20250103')]
        good = decode(package(rows, '<mergeCells><mergeCell ref="A2:A3"/></mergeCells>'), 2025)
        self.assertEqual(len(good['records']), 2)
        with self.assertRaises(ValueError):
            decode(package(rows), 2025)

    def test_options_and_tas_are_excluded_and_counted(self):
        result = decode(package([ENGLISH, row(), row('rb2505C3000'), row('sc_tas2505')]), 2025)
        self.assertEqual(len(result['records']), 1)
        self.assertEqual((result['excluded_options'], result['excluded_tas']), (1, 1))

    def test_wrong_date_header_duplicate_and_unknown_fail(self):
        for rows in [[ENGLISH, row(day='20260102')], [ENGLISH, row(day='20250230')], [ENGLISH, row(), row()],
                     [ENGLISH, row('rb2505UNKNOWN')], [['bad header']]*6]:
            with self.subTest(rows=rows), self.assertRaises(ValueError):
                decode(package(rows), 2025)

    def test_crc_truncation_and_expansion_budget(self):
        raw = package([ENGLISH, row()])
        with self.assertRaises((zipfile.BadZipFile, ValueError)):
            decode(raw[:-25], 2025)
        with self.assertRaises(ValueError):
            decode(raw, 2025, max_expanded=5)
        with self.assertRaises(ValueError):
            decode(package([ENGLISH, row()], name='../escape.xlsx'), 2025)

    def test_filter_does_not_make_unknown_code_valid(self):
        with self.assertRaises(ValueError):
            decode(package([ENGLISH, row('BAD')]), 2025, product='rb')


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
