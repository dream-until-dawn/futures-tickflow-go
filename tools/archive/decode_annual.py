"""离线解码官方SHFE/INE年度ZIP，原件不改写；输出仍须独立复核，不认证时段/挂牌全集。"""
import argparse
import hashlib
import io
import json
import re
import sys
import zipfile
import xml.etree.ElementTree as ET
from datetime import datetime

ENGLISH = ['Contract', 'Date', 'pre close', 'Pre settle', 'Open', 'High', 'Low', 'Close', 'Settle', 'ch1', 'ch2', 'Volume', 'Amount', 'OI']
CHINESE = ['品种', '合约', '日期', '前收盘', '前结算', '开盘价', '最高价', '最低价', '收盘价', '结算价', '涨跌1', '涨跌2', '成交量', '成交金额', '持仓量']
INE = {'bc', 'sc', 'nr', 'lu', 'ec'}
NS = '{http://schemas.openxmlformats.org/spreadsheetml/2006/main}'


def safe_archive(raw, limit):
    """不解压落盘；CRC通过实际读取验证，拒绝重复成员、路径穿越和膨胀超限。"""
    z = zipfile.ZipFile(io.BytesIO(raw))
    names = z.namelist()
    if len(set(names)) != len(names) or any(n.startswith(('/', '\\')) or '..' in n.replace('\\', '/').split('/') for n in names):
        z.close()
        raise ValueError('成员身份重复/路径非法')
    if sum(i.file_size for i in z.infolist()) > limit:
        z.close()
        raise ValueError('展开字节超预算')
    return z


def col(ref):
    m = re.fullmatch(r'([A-Z]+)[1-9][0-9]*', ref)
    if not m:
        raise ValueError('未知单元格坐标')
    n = 0
    for c in m[1]:
        n = n * 26 + ord(c) - ord('A') + 1
    if n > 256:
        raise ValueError('列超预算')
    return n - 1


def xlsx_rows(raw, limit):
    """直接读取XML数值文本，避免openpyxl浮点往返；只接受一个工作表。"""
    with safe_archive(raw, limit) as z:
        sheets = [n for n in z.namelist() if re.fullmatch(r'xl/worksheets/sheet[0-9]+\.xml', n)]
        if len(sheets) != 1:
            raise ValueError('工作表数量变化')
        strings = []
        if 'xl/sharedStrings.xml' in z.namelist():
            for _, element in ET.iterparse(io.BytesIO(z.read('xl/sharedStrings.xml')), events=['end']):
                if element.tag == NS + 'si':
                    strings.append(''.join(t.text or '' for t in element.iter(NS+'t')))
                    element.clear()
        sheet = z.read(sheets[0])
        merged = {}
        for _, e in ET.iterparse(io.BytesIO(sheet), events=['end']):
            if e.tag == NS+'mergeCell':
                first, last = e.attrib['ref'].split(':')
                if col(first) == col(last):
                    a, b = int(re.search(r'[0-9]+', first)[0]), int(re.search(r'[0-9]+', last)[0])
                    merged[(a, col(first))] = b
            e.clear()
        for _, e in ET.iterparse(io.BytesIO(sheet), events=['end']):
            if e.tag != NS+'row':
                continue
            number = int(e.attrib['r']); cells = {}
            for c in e.findall(NS+'c'):
                index = col(c.attrib['r'])
                if index in cells or c.find(NS+'f') is not None:
                    raise ValueError('重复单元格/公式不可作为原报价')
                kind = c.attrib.get('t', 'n'); value = c.findtext(NS+'v', '')
                if kind == 's':
                    value = strings[int(value)]
                elif kind == 'inlineStr':
                    value = ''.join(t.text or '' for t in c.iter(NS+'t'))
                elif kind not in ('n', 'str'):
                    raise ValueError('未知单元格类型')
                cells[index] = value
            width = max(cells, default=-1)+1
            values = [cells.get(i, '') for i in range(width)]
            yield number, values, merged
            e.clear()


def xls_rows(raw):
    """旧XLS需显式安装xlrd==2.0.2；二进制单元格只能保留解码表示及原件位置。"""
    import xlrd
    if xlrd.__version__ != '2.0.2':
        raise ValueError('旧XLS仅验证xlrd==2.0.2')
    book = xlrd.open_workbook(file_contents=raw, on_demand=True, formatting_info=True)
    try:
        if book.nsheets != 1:
            raise ValueError('XLS工作表数量变化')
        sheet = book.sheet_by_index(0)
        merged = {(a+1, left): b for a, b, left, right in sheet.merged_cells if right == left+1}
        for i in range(sheet.nrows):
            values = []
            for cell in sheet.row(i):
                if cell.ctype == xlrd.XL_CELL_NUMBER:
                    # repr保留可往返的二进制数解码值，不宣称是原始十进制报价字符串。
                    value = str(int(cell.value)) if cell.value.is_integer() else repr(cell.value)
                elif cell.ctype in (xlrd.XL_CELL_EMPTY, xlrd.XL_CELL_BLANK, xlrd.XL_CELL_TEXT):
                    value = str(cell.value)
                else:
                    raise ValueError('未知XLS单元格类型')
                values.append(value)
            yield i+1, values, merged
    finally:
        book.release_resources()


def decode(raw, year, product='', max_expanded=1024*1024*1024):
    records, seen = [], set()
    excluded_options, excluded_tas = 0, 0
    with safe_archive(raw, max_expanded) as archive:
        for info in archive.infolist():
            if info.is_dir():
                continue
            if not info.filename.lower().endswith(('.xls', '.xlsx')):
                raise ValueError('未知年度包成员')
            data = archive.read(info)  # 实际CRC校验，不只读目录。
            iterator = xlsx_rows(data, max_expanded) if info.filename.lower().endswith('.xlsx') else xls_rows(data)
            offset, carry, until = None, '', -1
            for row_number, values, merged in iterator:
                while values and values[-1] == '':
                    values.pop()
                if offset is None:
                    if values == ENGLISH:
                        offset = 0
                    elif values == CHINESE:
                        offset = 1
                    elif row_number >= 6:
                        raise ValueError('未知年度包表头')
                    continue
                if not any(values):
                    continue
                values += [''] * max(0, offset+14-len(values))
                if (row_number, offset) in merged:
                    carry, until = values[offset], merged[row_number, offset]
                if not values[offset] and row_number <= until:
                    values[offset] = carry
                row = values[offset:offset+14]; contract = row[0].strip()
                if contract.startswith('sc_tas'):
                    excluded_tas += 1
                    continue
                if re.fullmatch(r'[a-z]{1,3}[0-9]{4}[CP][0-9]+(?:\.[0-9]+)?', contract):
                    excluded_options += 1
                    continue
                if re.match(r'(?:[0-9]+[、．.]|注[:：]|说明[:：])', contract) and not any(row[1:]):
                    continue
                m = re.fullmatch(r'([a-z]{1,3})([0-9]{4})', contract)
                if not m:
                    raise ValueError('未知合约行: '+contract)
                day = row[1].strip(); datetime.strptime(day, '%Y%m%d')
                if not re.fullmatch(str(year)+r'[0-9]{4}', day):
                    raise ValueError('年度包业务日期错配')
                member_month = re.search(rf'{year}\.([0-9]{{1,2}})\.xlsx?$', info.filename)
                if member_month and day[:6] != str(year)+member_month[1].zfill(2):
                    raise ValueError('月成员日期错配')
                if product and m[1] != product:
                    continue
                key = day, contract
                if key in seen:
                    raise ValueError('重复合约日')
                seen.add(key)
                fields = {k: row[i].strip() for k, i in [('pre_settle', 3), ('open', 4), ('high', 5), ('low', 6), ('close', 7), ('settle', 8), ('volume', 11), ('turnover', 12), ('open_interest', 13)]}
                records.append(dict(exchange='INE' if m[1] in INE else 'SHFE', native=contract, day=int(day), fields=fields,
                                    turnover_unit='unknown', member=info.filename, row=row_number))
            if offset is None:
                raise ValueError('缺少表头')
    if not records:
        raise ValueError('无选择范围内的合约记录')
    return dict(schema='official-annual-decoded-v1', year=year, product_filter=product,
                original_sha256=hashlib.sha256(raw).hexdigest(), decoder_sha256=hashlib.sha256(__file_bytes()).hexdigest(),
                excluded_options=excluded_options, excluded_tas=excluded_tas,
                number_semantics='XLSX numeric XML text; XLS binary numeric decoded representation; no quote text reconstruction', records=records)


def __file_bytes():
    from pathlib import Path
    return Path(__file__).read_bytes()


def main():
    from pathlib import Path
    p = argparse.ArgumentParser(description=__doc__); p.add_argument('original', type=Path); p.add_argument('output', type=Path)
    p.add_argument('--sha256', required=True); p.add_argument('--year', required=True, type=int); p.add_argument('--product', default='')
    args = p.parse_args()
    if args.original.stat().st_size > 256*1024*1024:
        raise ValueError('原件超过256MiB预算')
    raw = args.original.read_bytes()
    if hashlib.sha256(raw).hexdigest() != args.sha256:
        raise ValueError('原件SHA不符')
    value = decode(raw, args.year, args.product)
    # 不覆盖已有冻结产物；重跑必须用新输出文件。
    with args.output.open('x', encoding='utf-8', newline='\n') as f:
        json.dump(value, f, ensure_ascii=False, indent=2); f.write('\n')
    print(json.dumps(dict(records=len(value['records']), sha256=hashlib.sha256(args.output.read_bytes()).hexdigest())))


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    main()
