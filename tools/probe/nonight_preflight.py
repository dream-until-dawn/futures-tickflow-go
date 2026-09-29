"""6.41 的离线预检：不联网、不创建录制目录、不输出凭证、不启动实跑。"""
import argparse
import datetime as dt
import os
from pathlib import Path
import re
import sys

ROOT = Path(__file__).resolve().parents[2]
KEYS = ("SHINNY_USER", "SHINNY_PASS", "SHINNY_CLIENT_SECRET")
WINDOWS = {
    "evening": ("2026-09-30T20:59:00+08:00", "2026-09-30T21:10:00+08:00", "TestNoNightRealEvening"),
    "fetch": ("2026-10-08T15:10:00+08:00", "2026-10-08T15:15:00+08:00", "TestNoNightRealFetch"),
}


def check(ins, output, env_file, environ):
    """只返回检查结论；凭证字节不离开函数。"""
    problems = []
    if not re.fullmatch(r"SHFE\.rb[0-9]{2}(0[1-9]|1[0-2])", ins):
        problems.append("合约必须为具体螺纹钢合约 SHFE.rbYYMM")
    out = Path(output).resolve()
    if out == ROOT or ROOT in out.parents:
        problems.append("录制目录必须在仓库外")
    if out.exists() and (not out.is_dir() or any(out.iterdir())):
        problems.append("录制目录必须不存在或为空，避免覆盖此前证据")
    values = {k: environ.get(k, "") for k in KEYS}
    if env_file.exists():
        for line in env_file.read_text(encoding="utf-8-sig").splitlines():
            if "=" not in line or line.lstrip().startswith("#"):
                continue
            k, v = line.split("=", 1)
            k = k.strip()
            if k in values and not values[k]:
                values[k] = v.strip().strip("\"'")
    missing = [k for k in KEYS if not values[k]]
    if missing:
        problems.append("缺少凭证键：" + " / ".join(missing))
    return out, problems


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--phase", choices=WINDOWS, required=True)
    p.add_argument("--ins", required=True, help="须另行核实的具体合约，预检不验证主力映射")
    p.add_argument("--out", required=True)
    p.add_argument("--env-file", type=Path, default=ROOT / ".env")
    args = p.parse_args()
    out, problems = check(args.ins, args.out, args.env_file, os.environ)
    begin, end, test = WINDOWS[args.phase]
    if dt.datetime.now(dt.timezone.utc) > dt.datetime.fromisoformat(begin):
        problems.append("计划开始时刻已过，不能照旧运行")
    for problem in problems:
        print("FAIL " + problem)
    if problems:
        return 1
    print("PASS 离线配置预检；未联网、未验证鉴权、未验证主力映射，以下命令须确认后执行。")
    quote = lambda s: "'" + str(s).replace("'", "''") + "'"
    print("Set-Location " + quote(ROOT))
    for key, value in {"TICKFLOW_LIVE":"1", "TICKFLOW_NN_INS":args.ins,
                       "TICKFLOW_NN_OUT":str(out), "TICKFLOW_NN_BEGIN":begin,
                       "TICKFLOW_NN_END":end}.items():
        print("$env:" + key + " = " + quote(value))
    print("go test ./source/shinnysource/ -run " + quote("^" + test + "$") + " -count=1 -v -timeout 0")
    return 0


if __name__ == "__main__":
    sys.stdout.reconfigure(encoding="utf-8")
    raise SystemExit(main())
