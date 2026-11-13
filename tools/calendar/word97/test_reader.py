"""合成CFB/Word97正反控制：活动片段顺序与物理顺序分开，旧残留不应进入正文。"""
import struct
import unittest
import io
import sys

from reader import read_main_text, END, FREE, FAT_SECTOR


def fixture(compressed=False):
    """固定的独立小样本：FAT正文流、miniFAT表流及两段活动字符，另有假旧条款。"""
    raw = bytearray(13*512)
    raw[:8] = bytes.fromhex('d0cf11e0a1b11ae1')
    def put(p, value, kind='<I'):
        struct.pack_into(kind, raw, p, value)
    for p, v in [(24, 0x3E), (26, 3), (28, 0xFFFE), (30, 9), (32, 6)]:
        put(p, v, '<H')
    for p, v in [(44, 1), (48, 1), (56, 4096), (60, 11), (64, 1), (68, END)]:
        put(p, v)
    for i in range(109):
        put(76+i*4, 0 if i == 0 else FREE)
    fat = [FREE]*128
    fat[0], fat[1], fat[10], fat[11] = FAT_SECTOR, END, END, END
    for i in range(2, 10):
        fat[i] = i+1 if i < 9 else END
    raw[512:1024] = struct.pack('<128I', *fat)
    def entry(i, name, kind, first, size, right=FREE, child=FREE):
        p = 1024+i*128
        encoded = (name+'\0').encode('utf-16-le')
        raw[p:p+len(encoded)] = encoded
        put(p+64, len(encoded), '<H')
        raw[p+66] = kind
        for offset, value in [(68, FREE), (72, right), (76, child), (116, first)]:
            put(p+offset, value)
        put(p+120, size, '<Q')
    # 活动文本第一段在1200，第二段在1024：顺序不能直接按物理偏移排序。
    first = b'A' if compressed else '甲'.encode('utf-16-le')
    second = '乙\r'.encode('utf-16-le')
    plc = struct.pack('<3I', 0, 1, 3)
    for fc in [(1200*2 | 0x40000000) if compressed else 1200, 1024]:
        plc += struct.pack('<HIH', 0, fc, 0)
    clx = b'\x02'+struct.pack('<I', len(plc))+plc
    entry(0, 'Root Entry', 5, 10, 64, child=1)
    entry(1, 'WordDocument', 2, 2, 4096, right=2)
    entry(2, '1Table', 2, 0, len(clx))
    word = 3*512
    for p, v in [(0, 0xA5EC), (2, 0xC1), (10, 0x200), (32, 14), (62, 22), (152, 34)]:
        put(word+p, v, '<H')
    for p, v in [(64, 4096), (76, 3), (418, 0), (422, len(clx))]:
        put(word+p, v)
    raw[word+1200:word+1200+len(first)] = first
    raw[word+1024:word+1024+len(second)] = second
    residue = '旧条款不能当正文'.encode('utf-16-le')
    raw[word+1400:word+1400+len(residue)] = residue
    raw[11*512:11*512+len(clx)] = clx
    raw[12*512:13*512] = struct.pack('<128I', END, *([FREE]*127))
    return raw


class Word97ReaderTest(unittest.TestCase):
    def test_active_order_excludes_residue_and_uses_minifat(self):
        raw = fixture()
        self.assertIn('旧条款不能当正文'.encode('utf-16-le'), raw)
        self.assertEqual(read_main_text(raw), '甲乙\r')
        self.assertEqual(read_main_text(fixture(compressed=True)), 'A乙\r')

    def test_header_fat_and_directory_refusals(self):
        for offset, value, kind, reason in [
            (26, 4, '<H', 'cfb_variant'),
            (72, 1, '<I', 'cfb_extended_difat'),
            (512+2*4, 2, '<I', 'cfb_chain'),
            (1024+128+72, FREE, '<I', 'cfb_stream_missing'),
            (1024+128+72, 1, '<I', 'cfb_directory'),
            (12*512, 0, '<I', 'cfb_chain'),
        ]:
            with self.subTest(reason=reason):
                raw = fixture()
                struct.pack_into(kind, raw, offset, value)
                with self.assertRaisesRegex(ValueError, reason):
                    read_main_text(raw)

    def test_fib_encryption_and_clx_refusals(self):
        word = 3*512
        for offset, value, kind, reason in [
            (word+2, 0xD9, '<H', 'doc_fib'),
            (word+10, 0x300, '<H', 'doc_encrypted'),
            (word+32, 13, '<H', 'doc_fib_layout'),
            (word+76, 0, '<I', 'doc_main_count'),
            (word+422, 4096, '<I', 'doc_clx_bounds'),
            (11*512, 0, '<B', 'doc_clx_kind'),
            (11*512+1, 29, '<I', 'doc_piece_size'),
        ]:
            with self.subTest(reason=reason):
                raw = fixture()
                struct.pack_into(kind, raw, offset, value)
                with self.assertRaisesRegex(ValueError, reason):
                    read_main_text(raw)

    def test_active_piece_bounds_and_identity_refusals(self):
        table = 11*512
        for offset, value, reason in [
            (table+5+4, 0, 'doc_piece_cps'),
            (table+5+8, 4, 'doc_piece_cps'),
            (table+5+12+2, 0x80000400, 'doc_piece_flags'),
            (table+5+12+2, 4096, 'doc_piece_bounds'),
            (table+5+12+2, 0x40000961, 'doc_piece_offset'),
        ]:
            with self.subTest(reason=reason):
                raw = fixture()
                struct.pack_into('<I', raw, offset, value)
                with self.assertRaisesRegex(ValueError, reason):
                    read_main_text(raw)

    def test_unknown_compressed_charset_and_missing_final_paragraph(self):
        raw = fixture(compressed=True)
        raw[3*512+1200] = 0xFF
        with self.assertRaisesRegex(ValueError, 'doc_compressed_charset'):
            read_main_text(raw)
        raw = fixture()
        raw[3*512+1026:3*512+1028] = '丙'.encode('utf-16-le')
        with self.assertRaisesRegex(ValueError, 'doc_main_paragraph'):
            read_main_text(raw)

    def test_partial_and_oversized_inputs_fail_closed(self):
        for raw in [b'', fixture()[:-1], bytes(4*1024*1024+512)]:
            with self.subTest(size=len(raw)), self.assertRaisesRegex(ValueError, 'cfb_header'):
                read_main_text(raw)


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    unittest.main()
