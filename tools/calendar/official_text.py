"""历史日历工具共用的原文解析、日期及字节源锁；不执行网络或历史事实推断。"""
import datetime as dt
import hashlib
import io
import re
import sys
from pathlib import Path
from html.parser import HTMLParser

ZONE = dt.timezone(dt.timedelta(hours=8))
VOID = {'br', 'hr', 'img', 'input', 'meta', 'link'}


class Article(HTMLParser):
    """只读已知官网正文容器；未知模板拒绝，避免把导航或挑战页当正文。"""
    def __init__(self):
        super().__init__()
        self.depth, self.parts = 0, []

    def handle_starttag(self, tag, attrs):
        classes = set(dict(attrs).get('class', '').split())
        if not self.depth and classes & {'notice_contain', 'TRS_Editor', 'm_content_test'}:
            self.depth = 1
        elif self.depth and tag not in VOID:
            self.depth += 1

    def handle_endtag(self, tag):
        if self.depth and tag not in VOID:
            self.depth -= 1

    def handle_data(self, data):
        if self.depth:
            self.parts.append(data)


def normalize(text):
    return re.sub(r'\s+', '', text)


def article(raw):
    reader = Article()
    reader.feed(raw.decode('utf-8'))
    text = normalize(''.join(reader.parts))
    if not text:
        raise ValueError('missing_article')
    return text


def date(number):
    return dt.datetime.strptime(str(number), '%Y%m%d').date()


def number(day):
    return int(day.strftime('%Y%m%d'))


def stamp(day, minute):
    return int(dt.datetime.combine(day, dt.time(), ZONE).timestamp() * 1000) + minute * 60000


def load_sources(facts, root):
    """源锁独立于回执，逐字节检查；本工具不认证任意新源锁的事实质量。"""
    result = {}
    for name, source in facts['sources'].items():
        filename = source['file']
        if Path(filename).name != filename:
            raise ValueError('unsafe_source_path')
        raw = (root / filename).read_bytes()
        if len(raw) != source['bytes'] or hashlib.sha256(raw).hexdigest() != source['ref']['sha256']:
            raise ValueError('source_hash:' + name)
        result[name] = raw
    return result



if __name__ == "__main__":
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
