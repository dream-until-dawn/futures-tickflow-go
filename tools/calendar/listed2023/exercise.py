"""在实际产物副本上调用Go二进制，逐项核对定向拒绝原因；不改原件。"""
import argparse
import copy
import io
import json
from pathlib import Path
import subprocess
import sys


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ['calendar', 'facts', 'evidence-dir', 'checker', 'out']:
        parser.add_argument('--' + name, required=True, type=Path)
    args = parser.parse_args()
    args.out.mkdir()
    original = json.loads(args.calendar.read_text(encoding='utf-8'))
    results = []

    def run(name, value, reason=None):
        path = args.out / (name + '.json')
        path.write_text(json.dumps(value, ensure_ascii=False), encoding='utf-8')
        cmd = [str(args.checker), '-calendar', str(path), '-facts', str(args.facts),
               '-evidence-dir', str(args.evidence_dir), '-out', str(args.out / (name + '-checked'))]
        if reason is None:
            cmd.append('-market')
        result = subprocess.run(cmd, capture_output=True, encoding='utf-8', timeout=90)
        text = result.stdout + result.stderr
        (args.out / (name + '.log')).write_text(text, encoding='utf-8')
        if reason is None:
            assert result.returncode == 0, text
        else:
            assert result.returncode != 0 and reason in text, (name, reason, text)
        results.append({'case': name, 'exit': result.returncode, 'reason': reason, 'actual': text.strip()})

    run('positive', original)
    cases = [
        ('ao-cross-midnight', 'ao', 20230703, 'absolute_session', lambda r: r['sessions'][0].__setitem__('End', r['sessions'][0]['End']-7200000)),
        ('br-friday-night', 'br', 20230731, 'absolute_session', lambda r: [r['sessions'][0].__setitem__(k, r['sessions'][0][k]+172800000) for k in ['Start', 'End']]),
        ('ec-published-not-effective', 'ec', 20230818, 'template_version_phase', lambda r: r['template'].__setitem__('From', 20230811)),
        ('ec-nov-version', 'ec', 20231121, 'template_version_phase', lambda r: r['template'].__setitem__('From', 20231117)),
        ('template-phase', 'ao', 20230703, 'template_version_phase', lambda r: [r['template']['Night'][0].__setitem__(k, r['template']['Night'][0][k]+60000) for k in ['Start', 'End']]),
        ('foreign-ref', 'br', 20230731, 'evidence_binding', lambda r: r['evidence'][0].__setitem__('sha256', 'a'*64)),
        ('missing-rule-ref', 'ec', 20231121, 'required_evidence', lambda r: r.__setitem__('evidence', r['evidence'][:2])),
    ]
    for name, product, day, reason, mutate in cases:
        value = copy.deepcopy(original)
        row = next(r for s in value['series'] if s['product']['Product'] == product
                   for r in s['records'] if r['day'] == day)
        mutate(row)
        run(name, value, reason)
    value = copy.deepcopy(original)
    value['series'][0]['records'].pop()
    run('missing-natural-day', value, 'natural_coverage')
    (args.out / 'matrix.json').write_text(json.dumps(results, ensure_ascii=False, indent=2), encoding='utf-8')
    print('正向1项、定向反例8项通过')


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    main()
