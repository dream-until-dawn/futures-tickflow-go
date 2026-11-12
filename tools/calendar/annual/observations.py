"""只读SHFE年度XLS/XLSX中RB公布日期，作日期交叉控制；不推断时段或价格资格。"""
import argparse
import datetime as dt
import hashlib
import io
import json
from pathlib import Path
import re
import sys
import zipfile


def record(values, year):
    candidates = [i for i, v in enumerate(values[:3]) if re.fullmatch(r'rb[0-9]{4}', str(v).strip())]
    if not candidates:
        return None
    if len(candidates) != 1:
        raise ValueError('ambiguous_rb_column')
    pos = candidates[0]
    value = values[pos+1]
    text = str(int(value)) if type(value) in (float, int) and value == int(value) else str(value).strip()
    if not re.fullmatch(str(year)+r'[0-9]{4}', text):
        raise ValueError('observation_year')
    dt.datetime.strptime(text, '%Y%m%d')
    return str(values[pos]).strip(), int(text)


def scan(raw, year):
    import xlrd  # 运行实际XLS扫描时需要2.0.2；单元格检查不依赖第三方库。
    import openpyxl  # XLSX独立于现有年度解码器的XML读取实现。
    months, seen, rows = {}, set(), 0
    with zipfile.ZipFile(io.BytesIO(raw)) as archive:
        names = archive.namelist()
        if len(names) != len(set(names)) or any(n.startswith(('/', '\\')) or '..' in n.replace('\\', '/').split('/') for n in names):
            raise ValueError('zip_identity')
        if sum(x.file_size for x in archive.infolist()) > 1024**3 or archive.testzip() is not None:
            raise ValueError('zip_crc_or_budget')
        for name in names:
            if name.endswith('/'):
                continue
            if name.startswith('ine') and name.endswith('.xlsx'):
                continue  # 已核年度ZIP内的INE年成员不含RB；仍在前面的ZIP CRC核验中。
            match = re.search(rf'{year}\.([0-9]{{1,2}})\.xlsx?$', name)
            if not match:
                raise ValueError('unsupported_member:' + name)
            month = int(match[1])
            if month in months or not 1 <= month <= 12:
                raise ValueError('month_identity')
            dates = set()
            # 只在原件明示的合并区域内读取合约锚点，不跨空白行任意前向填充。
            anchors = {}
            if name.endswith('.xls'):
                sheet = xlrd.open_workbook(file_contents=archive.read(name), formatting_info=True).sheet_by_index(0)
                for low, high, left, right in sheet.merged_cells:
                    if left < 3:
                        for index in range(low, high):
                            anchors[index, left] = sheet.cell_value(low, left)
                iterator = (sheet.row_values(i) for i in range(sheet.nrows))
            else:
                book = openpyxl.load_workbook(io.BytesIO(archive.read(name)), data_only=False)
                if len(book.worksheets) != 1:
                    raise ValueError('worksheet_count')
                sheet = book.worksheets[0]
                for area in sheet.merged_cells.ranges:
                    if area.min_col <= 3:
                        for index in range(area.min_row-1, area.max_row):
                            anchors[index, area.min_col-1] = sheet.cell(area.min_row, area.min_col).value
                iterator = (list(row) for row in sheet.iter_rows(values_only=True))
            for index, values in enumerate(iterator):
                for col in range(min(3, len(values))):
                    if not values[col] and (index, col) in anchors:
                        values[col] = anchors[index, col]
                item = record(values, year)
                if item is None:
                    continue
                if item in seen or item[1]//100%100 != month:
                    raise ValueError('duplicate_or_wrong_month')
                seen.add(item)
                dates.add(item[1])
                rows += 1
            if name.endswith('.xlsx'):
                book.close()
            if not dates:
                raise ValueError('month_without_rb')
            months[f'{month:02d}'] = sorted(dates)
    if len(months) != 12:
        raise ValueError('missing_month')
    return {'schema': 'shfe-rb-observed-days-v1', 'year': year,
            'origin_sha256': hashlib.sha256(raw).hexdigest(),
            'scanner_sha256': hashlib.sha256(Path(__file__).read_bytes()).hexdigest(),
            'months': months, 'contract_days': rows,
            'scope': 'observed published dates only; not a schedule/price/full-universe certificate'}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('original', type=Path)
    parser.add_argument('out', type=Path)
    parser.add_argument('--sha256', required=True)
    parser.add_argument('--year', type=int, required=True)
    args = parser.parse_args()
    if args.original.stat().st_size > 256*1024**2:
        raise ValueError('original_budget')
    raw = args.original.read_bytes()
    if hashlib.sha256(raw).hexdigest() != args.sha256:
        raise ValueError('original_hash')
    value = scan(raw, args.year)
    with args.out.open('x', encoding='utf-8', newline='\n') as output:
        json.dump(value, output, ensure_ascii=False, indent=2)
        output.write('\n')
    print(json.dumps({'months': len(value['months']), 'observed_days': sum(map(len, value['months'].values())),
                      'contract_days': value['contract_days']}))


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    main()
