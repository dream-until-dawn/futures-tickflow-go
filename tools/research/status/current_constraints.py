"""复核已封存网页和逐页索引；不联网、不从普通1手自动推定。"""
import io
import sys
import hashlib
import json
from html.parser import HTMLParser
from pathlib import Path
import re
from urllib.parse import urljoin


class Visible(HTMLParser):
    def __init__(self):
        super().__init__()
        self.parts = []
        self.hidden = 0

    def handle_starttag(self, tag, attrs):
        if tag in {'script', 'style'}:
            self.hidden += 1

    def handle_endtag(self, tag):
        if tag in {'script', 'style'}:
            self.hidden -= 1

    def handle_data(self, value):
        if not self.hidden:
            self.parts.append(value)


def visible(raw):
    parser = Visible()
    parser.feed(raw.decode('utf-8', errors='strict'))
    return ''.join(''.join(parser.parts).split())


def index_entries(raw):
    pattern = (r'<div class="info_item_title">\s*<a href="([^"]+)"[^>]*title="([^"]+)"[^>]*>'
               r'.*?</a>\s*</div>\s*<div class="info_item_date">(\d{4}-\d{2}-\d{2})</div>')
    from html import unescape
    rows = [dict(url=urljoin('https://www.shfe.com.cn/publicnotice/notice/', url),
                 title=unescape(title), day=day) for url, title, day in re.findall(pattern, raw.decode('utf-8'), re.S)]
    if len(rows) != 20 or len({r['url'] for r in rows}) != 20:
        raise ValueError('constraint_index_count_or_duplicate')
    if [r['day'] for r in rows] != sorted((r['day'] for r in rows), reverse=True):
        raise ValueError('constraint_index_order')
    return rows


def verify_original(row, root):
    path = (root / row['file']).resolve()
    if not path.is_relative_to(root.resolve()):
        raise ValueError('constraint_source_path')
    raw = path.read_bytes()
    if len(raw) != row['bytes'] or hashlib.sha256(raw).hexdigest() != row['sha256']:
        raise ValueError('constraint_source_identity')
    receipt_path = (root / row['receipt_file']).resolve()
    if not receipt_path.is_relative_to(root.resolve()):
        raise ValueError('constraint_receipt_path')
    receipt_raw = receipt_path.read_bytes()
    if hashlib.sha256(receipt_raw).hexdigest() != row['receipt_sha256']:
        raise ValueError('constraint_receipt_identity')
    receipt = json.loads(receipt_raw)
    http = receipt['http']
    if (not http['complete'] or http['status'] != 200 or http['final_url'] != row['url'] or
            receipt['sha256'] != row['sha256']):
        raise ValueError('constraint_source_transport')
    # 原回执锚点误判不改写；只有保留原件并重核可见全文才能在审核层使用。
    text = visible(raw)
    if any(w in text for w in ('人机识别检测', '向右滑动填充拼图')):
        raise ValueError('constraint_source_challenge')
    if any(w not in text for w in row['required_visible_text']):
        raise ValueError('constraint_source_business_text')
    return text



if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    raise SystemExit('由离线专项审核调用；不执行网络请求')
