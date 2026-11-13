"""有界读取已冻结Word97兼容CFB的活动主文档字符；不渲染、不执行宏、不猜未知编码。"""
import struct
import io
import sys

END = 0xFFFFFFFE
FREE = 0xFFFFFFFF
FAT_SECTOR = 0xFFFFFFFD
MAX_BYTES = 4 * 1024 * 1024


def _value(raw, offset, kind):
    size = struct.calcsize(kind)
    if offset < 0 or offset + size > len(raw):
        raise ValueError('word_bounds')
    return struct.unpack_from(kind, raw, offset)[0]


class _Compound:
    """只支持本批实际遇到的v3、小文件头内DIFAT及根级流；异常分配链拒绝。"""
    def __init__(self, raw):
        if not 1536 <= len(raw) <= MAX_BYTES or len(raw) % 512 or raw[:8] != bytes.fromhex('d0cf11e0a1b11ae1'):
            raise ValueError('cfb_header')
        self.raw = raw
        if (_value(raw, 26, '<H'), _value(raw, 28, '<H'), _value(raw, 30, '<H'),
                _value(raw, 32, '<H'), _value(raw, 56, '<I')) != (3, 0xFFFE, 9, 6, 4096):
            raise ValueError('cfb_variant')
        if _value(raw, 72, '<I') != 0 or _value(raw, 68, '<I') != END:
            raise ValueError('cfb_extended_difat')
        count = _value(raw, 44, '<I')
        if not 1 <= count <= 109:
            raise ValueError('cfb_fat_count')
        ids = [_value(raw, 76 + 4*i, '<I') for i in range(count)]
        if len(set(ids)) != count:
            raise ValueError('cfb_fat_count')
        fat = b''.join(self._sector(i) for i in ids)
        self.fat = struct.unpack('<' + 'I'*(len(fat)//4), fat)
        if any(i >= len(self.fat) or self.fat[i] != FAT_SECTOR for i in ids):
            raise ValueError('cfb_fat_identity')
        directory = self._normal(_value(raw, 48, '<I'))
        self.entries = [directory[i:i+128] for i in range(0, len(directory), 128)]
        if not self.entries or self.entries[0][66] != 5:
            raise ValueError('cfb_root')
        root = self.entries[0]
        self.streams = {}
        pending, seen, names = [_value(root, 76, '<I')], set(), set()
        # 只走根目录的真实兄弟树；孤立或嵌套的同名流不能冒充WordDocument。
        while pending:
            i = pending.pop()
            if i == FREE:
                continue
            if i == 0 or i in seen or i >= len(self.entries):
                raise ValueError('cfb_directory')
            seen.add(i)
            entry = self.entries[i]
            length = _value(entry, 64, '<H')
            if entry[66] not in (1, 2) or length < 2 or length > 64 or length % 2 or entry[length-2:length] != b'\0\0':
                raise ValueError('cfb_directory')
            name = entry[:length-2].decode('utf-16-le')
            if name.casefold() in names:
                raise ValueError('cfb_duplicate_name')
            names.add(name.casefold())
            self.streams[name] = entry
            pending += [_value(entry, 68, '<I'), _value(entry, 72, '<I')]
        mini_count = _value(raw, 64, '<I')
        mini = self._normal(_value(raw, 60, '<I')) if mini_count else b''
        if len(mini) != mini_count * 512:
            raise ValueError('cfb_minifat_size')
        self.mini_fat = struct.unpack('<' + 'I'*(len(mini)//4), mini)
        self.mini = self._sized_normal(_value(root, 116, '<I'), _value(root, 120, '<Q'))

    def _sector(self, i):
        start = (i+1)*512
        if i >= len(self.raw)//512-1 or start + 512 > len(self.raw):
            raise ValueError('cfb_sector_bounds')
        return self.raw[start:start+512]

    def _chain(self, first, table, take):
        chunks, seen = [], set()
        while first != END:
            if first in seen or first >= len(table):
                raise ValueError('cfb_chain')
            seen.add(first)
            chunks.append(take(first))
            if len(seen) > len(self.raw)//64:
                raise ValueError('cfb_chain_limit')
            first = table[first]
        return b''.join(chunks)

    def _normal(self, first):
        return self._chain(first, self.fat, self._sector)

    @staticmethod
    def _sized(data, size, unit):
        if size > MAX_BYTES or len(data) < size or len(data)-size >= unit:
            raise ValueError('cfb_stream_size')
        return data[:size]

    def _sized_normal(self, first, size):
        return self._sized(self._normal(first), size, 512)

    def stream(self, name):
        entry = self.streams.get(name)
        if entry is None or entry[66] != 2:
            raise ValueError('cfb_stream_missing:' + name)
        first, size = _value(entry, 116, '<I'), _value(entry, 120, '<Q')
        if size >= 4096:
            return self._sized_normal(first, size)

        def take(i):
            if (i+1)*64 > len(self.mini):
                raise ValueError('cfb_mini_bounds')
            return self.mini[i*64:(i+1)*64]
        return self._sized(self._chain(first, self.mini_fat, take), size, 64)


def read_main_text(raw):
    """依FIB/CLX活动CP顺序提取主文档；不把物理残留、页眉或脚注混入正文。"""
    compound = _Compound(raw)
    word = compound.stream('WordDocument')
    if _value(word, 0, '<H') != 0xA5EC or _value(word, 2, '<H') != 0xC1:
        raise ValueError('doc_fib')
    flags = _value(word, 10, '<H')
    if flags & 0x8100:
        raise ValueError('doc_encrypted')
    pairs = _value(word, 152, '<H')
    if _value(word, 32, '<H') != 14 or _value(word, 62, '<H') != 22 or pairs < 34 or 154+pairs*8 > len(word):
        raise ValueError('doc_fib_layout')
    cb_mac, count = _value(word, 64, '<I'), _value(word, 76, '<I')
    if not 0 < count <= MAX_BYTES//2 or cb_mac > len(word) or cb_mac < 426:
        raise ValueError('doc_main_count')
    word = word[:cb_mac]
    table = compound.stream('1Table' if flags & 0x200 else '0Table')
    start, length = _value(word, 418, '<I'), _value(word, 422, '<I')
    if length == 0 or start + length > len(table):
        raise ValueError('doc_clx_bounds')
    clx, pos = table[start:start+length], 0
    while pos < len(clx) and clx[pos] == 1:
        pos += 3 + _value(clx, pos+1, '<H')
    if pos + 5 > len(clx) or clx[pos] != 2:
        raise ValueError('doc_clx_kind')
    size = _value(clx, pos+1, '<I')
    if size < 16 or (size-4) % 12 or pos+5+size != len(clx):
        raise ValueError('doc_piece_size')
    plc = clx[pos+5:]
    n = (size-4)//12
    if n > 4096:
        raise ValueError('doc_piece_limit')
    cps = [_value(plc, i*4, '<I') for i in range(n+1)]
    other = sum(_value(word, 64+i*4, '<I') for i in (4, 5, 7, 8, 9, 10))
    if cps[0] != 0 or cps[-1] != count+other+(1 if other else 0) or any(a >= b for a, b in zip(cps, cps[1:])):
        raise ValueError('doc_piece_cps')
    text = []
    for i in range(n):
        if cps[i] >= count:
            break
        units = min(cps[i+1], count)-cps[i]
        fc = _value(plc, 4*(n+1)+i*8+2, '<I')
        if fc & 0x80000000:
            raise ValueError('doc_piece_flags')
        compressed = bool(fc & 0x40000000)
        offset = fc & 0x3FFFFFFF
        if compressed:
            if offset % 2:
                raise ValueError('doc_piece_offset')
            offset //= 2
        end = offset + units*(1 if compressed else 2)
        if offset < 426 or end > len(word):
            raise ValueError('doc_piece_bounds')
        data = word[offset:end]
        if compressed and any(x >= 128 for x in data):
            raise ValueError('doc_compressed_charset')
        text.append(data.decode('ascii' if compressed else 'utf-16-le'))
    result = ''.join(text)
    if not result.endswith('\r'):
        raise ValueError('doc_main_paragraph')
    return result


# 与仓内其他纯辅助模块一致，只在直接运行时封装；导入不改变调用方双流。
if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
