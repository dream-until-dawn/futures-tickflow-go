"""在副本上调用独立Go检查器，要求正向通过、定向篡改按目标原因失败。"""
import argparse
import copy
import json
import io
from pathlib import Path
import subprocess
import sys


def main():
    p = argparse.ArgumentParser(description=__doc__)
    for name in ['calendar', 'facts', 'evidence-dir', 'checker', 'out']:
        p.add_argument('--' + name, required=True, type=Path)
    args = p.parse_args()
    args.out.mkdir()  # 已有目录不得覆盖；原件不修改。
    original = json.loads(args.calendar.read_text(encoding='utf-8'))
    results = []

    def run(name, value, reason=None):
        path = args.out / (name + '.json')
        path.write_text(json.dumps(value, ensure_ascii=False), encoding='utf-8')
        cmd = [str(args.checker), '-calendar', str(path), '-facts', str(args.facts),
               '-evidence-dir', str(args.evidence_dir), '-out', str(args.out / (name + '-checked'))]
        if reason is None:
            cmd.append('-market')
        result = subprocess.run(cmd, capture_output=True, text=True, encoding='utf-8', timeout=45)
        text = result.stdout + result.stderr
        (args.out / (name + '.log')).write_text(text, encoding='utf-8')
        if reason is None:
            assert result.returncode == 0, (name, text)
        else:
            assert result.returncode != 0 and reason in text, (name, reason, text)
        results.append({'case': name, 'exit': result.returncode, 'required_reason': reason, 'actual': text.strip()})

    run('positive', original)
    cases = [
        ('holiday-night', 20210218, 'night_presence', lambda r: r['sessions'].insert(0,
          {'Start': 1612962000000, 'End': 1612969200000})),
        ('friday-to-sunday', 20210111, 'absolute_session', lambda r: [s.__setitem__(k, s[k]+172800000)
          for s in r['sessions'][:1] for k in ['Start', 'End']]),
        ('day-break', 20210728, 'absolute_session', lambda r: r['sessions'][1].__setitem__('End', r['sessions'][1]['End']+60000)),
        ('holiday-open', 20210212, 'closed_day_opened', lambda r: r.__setitem__('sessions',
          [{'Start': 1613091600000, 'End': 1613096100000}])),
        ('template-phase', 20210728, 'nominal_template', lambda r: [s.__setitem__(k, s[k]+60000)
          for s in r['template']['Night'] for k in ['Start', 'End']]),
        ('wrong-version', 20210728, 'version_partition', lambda r: r['template'].__setitem__('From', 20210610)),
        ('foreign-evidence', 20210728, 'calendar_evidence_binding', lambda r: r['evidence'][0].__setitem__('sha256', 'a'*64)),
        ('missing-rule-evidence', 20210728, 'calendar_evidence_required', lambda r: r.__setitem__('evidence', r['evidence'][:2])),
    ]
    for name, day, reason, change in cases:
        value = copy.deepcopy(original)
        row = next(r for r in value['series'][0]['records'] if r['day'] == day)
        change(row)
        run(name, value, reason)
    value = copy.deepcopy(original)
    value['series'][0]['records'].pop()
    run('missing-day', value, 'profile_identity')
    (args.out / 'matrix.json').write_text(json.dumps(results, ensure_ascii=False, indent=2), encoding='utf-8')
    print(f'正向1项、定向反例{len(results)-1}项通过')


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    main()
