"""单次公开结算参数原件取证；固定资源，原失败也不得换输出目录重试。"""
import io
import sys
import argparse
import datetime as dt
import hashlib
import json
from pathlib import Path
import urllib.error
import urllib.request

from clear_parameters import parse


URL = 'https://www.czce.com.cn/cn/DFSStaticFiles/Future/2026/20261116/FutureDataClearParams.txt'
LIMIT = 2 * 1024 * 1024


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--root', type=Path, required=True)
    p.add_argument('--run', action='store_true')
    a = p.parse_args()
    print(json.dumps(dict(url=URL, requests=1, max_bytes=LIMIT, timeout_seconds=15,
                         retries=0, redirects=0, accounts=False), ensure_ascii=False), flush=True)
    if not a.run:
        return
    # 与输出目录分离，换目录不能绕过单次标记；采集前已核既有失败目录。
    marker = Path('C:/Temp/tickflow-czce-clear-20261116.once')
    marker.parent.mkdir(parents=True, exist_ok=True)
    with marker.open('x', encoding='utf-8') as f:
        f.write(str(a.root.resolve()))
    a.root.mkdir(parents=True, exist_ok=False)
    receipt = dict(url=URL, requested_at=dt.datetime.now(dt.timezone.utc).isoformat(), state='unknown')
    (a.root / 'receipt.json').write_text(json.dumps(receipt, indent=2), encoding='utf-8')
    try:
        opener = urllib.request.build_opener(NoRedirect)
        try:
            response = opener.open(URL, timeout=15)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            raw = response.read(LIMIT + 1)
            receipt.update(http_status=response.status, bytes=len(raw),
                           sha256=hashlib.sha256(raw).hexdigest(), final_url=response.url,
                           retrieved_at=dt.datetime.now(dt.timezone.utc).isoformat())
            (a.root / 'original.body').write_bytes(raw)
            if len(raw) > LIMIT or response.status != 200 or response.url != URL:
                raise ValueError('clear_response_boundary')
        rows = parse(raw, 20261116)
        (a.root / 'parsed.json').write_text(json.dumps(rows, ensure_ascii=False, indent=2)+'\n', encoding='utf-8', newline='\n')
        receipt.update(state='parsed', rows=len(rows))
    except Exception as error:
        receipt.update(state='failed', error=type(error).__name__+': '+str(error))
    (a.root / 'receipt.json').write_text(json.dumps(receipt, ensure_ascii=False, indent=2)+'\n', encoding='utf-8', newline='\n')
    print(json.dumps(receipt, ensure_ascii=False))
    if receipt['state'] != 'parsed':
        raise SystemExit(1)


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    main()
