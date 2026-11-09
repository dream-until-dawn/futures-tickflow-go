"""通过Go overlay破坏有效源码；不改工作树，不以编译失败冒充测试杀死。"""
import io
import json
import hashlib
from pathlib import Path
import subprocess
import sys
import argparse

ROOT = Path(__file__).resolve().parents[2]
CASES = [
    ('原件哈希失效', 'refdata/evidence/evidence.go', 'if hex.EncodeToString(sum[:]) != r.SHA256 {', 'if false && hex.EncodeToString(sum[:]) != r.SHA256 {', './refdata/evidence', 'TestEvidenceHashAndIdentity'),
    ('无证据日历被接受', 'calendar/verified/calendar.go', 'if len(r.Evidence) == 0 {', 'if false && len(r.Evidence) == 0 {', './calendar/verified', 'TestVerifiedCalendarRejectsGapsAndInvalidProof'),
    ('候选冒充历史映射', 'refdata/catalog/catalog.go', 'known := e.Listed.Day != 0', 'known := true || e.Listed.Day != 0', './refdata/catalog', 'TestCatalogAmbiguityUnknownAndSnapshot'),
    ('漏合约不报错', 'refdata/catalog/catalog.go', 'if len(seen) != len(set) {', 'if false && len(seen) != len(set) {', './refdata/catalog', 'TestCatalogIndependentRosterAndLifecycle'),
    ('请求预算失效', 'archive/batch/download.go', 'if d.requests >= d.limits.Requests || d.bytes >= d.limits.Bytes {', 'if false && (d.requests >= d.limits.Requests || d.bytes >= d.limits.Bytes) {', './archive/batch', 'TestBatchBudgetResumeAndTamper'),
    ('年包原件哈希失效', 'source/officialdaily/annual.go', 'if err := d.Origin.Verify(d.Original); err != nil {', 'if err := d.Origin.Verify(d.Original); false && err != nil {', './source/officialdaily', 'TestAnnualOriginAndArtifactAreBothBound'),
    ('Feed收盘错列', 'source/officialdaily/source.go', 'Close: c, Volume:', 'Close: o, Volume:', './source/officialdaily', 'TestOfficialSyncStoreFeedResume'),
]


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--out',type=Path,required=True,help='仓库外的独立证据目录')
    out=parser.parse_args().out.resolve()
    if out==ROOT or ROOT in out.parents:
        raise ValueError('破坏验证产物必须放在仓库外，不能污染源码扫描')
    out.mkdir(parents=True, exist_ok=True)
    rows = []
    for i, (name, path, old, new, package, test) in enumerate(CASES):
        source = ROOT / path
        original = source.read_bytes(); text = original.decode('utf-8')
        assert text.count(old) == 1, (name, 'mutant site count')
        cmd = ['go', 'test', package, '-count=1', '-run', '^'+test+'$', '-timeout', '30s']
        control = subprocess.run(cmd, cwd=ROOT, capture_output=True, text=True, encoding='utf-8', timeout=90)
        assert control.returncode == 0 and 'ok ' in control.stdout, (name, control.stdout, control.stderr)
        replacement = out / f'mutant-{i}.go'
        replacement.write_text(text.replace(old, new), encoding='utf-8', newline='\n')
        overlay = out / f'overlay-{i}.json'
        overlay.write_text(json.dumps({'Replace': {str(source.resolve()): str(replacement.resolve())}}), encoding='utf-8')
        result = subprocess.run(['go', 'test', '-overlay='+str(overlay), package, '-count=1', '-run', '^'+test+'$', '-timeout', '30s'], cwd=ROOT, capture_output=True, text=True, encoding='utf-8', timeout=90)
        log = result.stdout + result.stderr
        assert result.returncode != 0 and '--- FAIL: '+test in log and '[build failed]' not in log, (name, log)
        assert source.read_bytes() == original, '工作树被修改'
        (out / f'mutant-{i}.log').write_text(log, encoding='utf-8', newline='\n')
        rows.append(dict(name=name, file=path, baseline='passed', mutant='assertion_rejected', source_sha256=hashlib.sha256(original).hexdigest(), test=test))
    (out / 'report.json').write_text(json.dumps(dict(status='passed', mutants=len(rows), cases=rows, workspace_unchanged=True), ensure_ascii=False, indent=2)+'\n', encoding='utf-8', newline='\n')
    print('有效源码破坏全部被断言拒绝:', len(rows), '报告:',out/'report.json')


if __name__ == '__main__':
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    main()
