"""6.41 的离线预检：不联网、不创建录制目录、不输出凭证、不启动实跑。"""
import argparse
import datetime as dt
import hashlib
import io
import os
from pathlib import Path
import re
import sys
from nonight_plan import validate as validate_plan

ROOT = Path(__file__).resolve().parents[2]
KEYS = ("SHINNY_USER", "SHINNY_PASS", "SHINNY_CLIENT_SECRET")
WINDOWS = {
    "evening": "TestNoNightRealEvening",
    "fetch": "TestNoNightRealFetch",
}


def check(ins, output, env_file, environ):
    """只返回检查结论；凭证字节不离开函数。"""
    problems = []
    if not re.fullmatch(r"SHFE\.rb[0-9]{2}(0[1-9]|1[0-2])", ins):
        problems.append("合约必须为具体螺纹钢合约 SHFE.rbYYMM")
    out = Path(output).resolve()
    if out == ROOT or ROOT in out.parents:
        problems.append("录制目录必须在仓库外")
    if out.exists():
        problems.append("录制目录必须不存在，实跑将独占创建")
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


def main(argv=None, *, now=None):
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--phase", choices=WINDOWS, required=True)
    p.add_argument("--ins", required=True, help="须另行核实的具体合约，预检不验证主力映射")
    p.add_argument("--out", required=True)
    p.add_argument("--env-file", type=Path, default=ROOT / ".env")
    p.add_argument("--plan", type=Path, help="绑定官方公告的参数化执行单；省略会拒绝旧固定日期")
    p.add_argument("--allow-synthetic", action="store_true", help="仅验证合成示例，不输出实跑命令")
    args = p.parse_args(argv)
    # 实跑只读进程环境和根目录 .env，不能用另一份凭证制造预检通过。
    if args.env_file.resolve() != (ROOT / ".env").resolve():
        p.error("--env-file 只能是仓库根目录 .env，须与实跑的凭证来源一致")
    out, problems = check(args.ins, args.out, args.env_file, os.environ)
    if args.plan is None:
        problems.append("旧固定窗口已归档，必须提供--plan")
        plan = None
    else:
        try:
            plan = validate_plan(args.plan, args.allow_synthetic)
            if plan["symbol"] != args.ins:
                raise ValueError("合约与计划不符")
        except (ValueError, OSError, KeyError, TypeError) as error:
            problems.append(str(error))
            plan = None
    begin, end, test = "", "", WINDOWS[args.phase]
    if plan:
        begin, end = plan[args.phase+"_begin"], plan[args.phase+"_end"]
    if plan and plan["evidence_class"] != "synthetic" and (now or dt.datetime.now(dt.timezone.utc)) > dt.datetime.fromisoformat(begin):
        problems.append("计划开始时刻已过，不能照旧运行")
    for problem in problems:
        print("FAIL " + problem)
    if problems:
        return 1
    if plan["evidence_class"] == "synthetic":
        print("PASS 仅合成执行单校验，不输出正式执行命令，不启动任务。")
        return 0
    print("PASS 离线配置预检；未联网、未验证鉴权、未验证主力映射，执行前须核对公告真实性与现场时点。")
    quote = lambda s: "'" + str(s).replace("'", "''") + "'"
    print("Set-Location " + quote(ROOT))
    for key, value in {"TICKFLOW_LIVE":"1", "TICKFLOW_NN_INS":args.ins,
                       "TICKFLOW_NN_OUT":str(out), "TICKFLOW_NN_BEGIN":begin,
                       "TICKFLOW_NN_END":end, "TICKFLOW_NN_PLAN":str(args.plan.resolve()),
                       "TICKFLOW_NN_PLAN_SHA256":hashlib.sha256(args.plan.read_bytes()).hexdigest()}.items():
        print("$env:" + key + " = " + quote(value))
    print("go test ./source/shinnysource/ -run " + quote("^" + test + "$") + " -count=1 -v -timeout 0")
    return 0


if __name__ == "__main__":
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
    raise SystemExit(main())
