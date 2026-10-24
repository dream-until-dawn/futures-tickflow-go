"""绑定编译产物、执行参数和采集清单，不读取或保存行情凭证。"""
import hashlib
import datetime as dt
import io
import json
import os
from pathlib import Path
import re
import subprocess
import sys

SPEC_KEYS = {"INS", "DAYS", "START", "BEGIN", "DROP", "RESUME", "END", "DEADLINE"}


def synthetic_spec():
    """固定虚拟时钟，与本地假服务演练入口对表，不是可实跑的历史窗口。"""
    return dict(INS="SHFE.rb2610", DAYS="20260903,20260904", START="2026-09-04T09:00:00+08:00",
                BEGIN="2026-09-04T09:01:00+08:00", DROP="2026-09-04T09:06:00+08:00",
                RESUME="2026-09-04T09:38:00+08:00", END="2026-09-04T09:48:00+08:00",
                DEADLINE="2026-09-04T10:00:00+08:00")


def validate_artifact(value):
    """严格保留来源字段；不将未知元数据混入执行清单。"""
    if not isinstance(value, dict) or set(value) != {"path", "sha256", "source_sha", "mode", "spec"}:
        raise ValueError("artifact字段不完整或含未知字段")
    if value["mode"] not in ("real", "synthetic"):
        raise ValueError("未知材料分类")
    for key, length in (("sha256", 64), ("source_sha", 40)):
        if not isinstance(value[key], str) or not re.fullmatch("[0-9a-f]{" + str(length) + "}", value[key]):
            raise ValueError("编译来源哈希格式错误")
    if not isinstance(value["path"], str) or not Path(value["path"]).is_absolute():
        raise ValueError("编译产物必须使用绝对路径")
    spec = value["spec"]
    if not isinstance(spec, dict) or set(spec) != SPEC_KEYS or any(not isinstance(v, str) or not v for v in spec.values()):
        raise ValueError("执行参数必须完整且不含凭证键")
    if value["mode"] == "synthetic" and spec != synthetic_spec():
        raise ValueError("合成模式只能使用明确的固定虚拟时钟")
    if value["mode"] == "real":
        for key in ("START", "BEGIN", "DROP", "RESUME", "END", "DEADLINE"):
            if dt.datetime.fromisoformat(spec[key]).utcoffset() != dt.timedelta(hours=8):
                raise ValueError("执行单时刻须明确东八区，与独立复核口径一致")
    return dict(value, spec=dict(spec))


def parameters(artifact):
    """只返回非敏感运行参数，真实与合成入口严格分离。"""
    result = {"TICKFLOW_GAP_" + k: v for k, v in artifact["spec"].items()}
    result.update(TICKFLOW_GAP_OUT="{task_dir}/capture", TICKFLOW_GAP_REAL="0", TICKFLOW_LIVE="0")
    if artifact["mode"] == "real":
        result.update(TICKFLOW_GAP_REAL="1", TICKFLOW_LIVE="1")
    else:
        result["TICKFLOW_GAP_OFFLINE"] = "1"
    return result


def command(artifact):
    """执行固定测试入口，不能因筛选错字而零测试成功。"""
    test = "TestGapOfflineBundle" if artifact["mode"] == "synthetic" else "TestLiveLongGapReal"
    return ["{task_dir}/" + Path(artifact["path"]).name, "-test.run=^" + test + "$", "-test.v", "-test.timeout=0"]


def check_binding(plan):
    artifact = plan["artifact"]
    if artifact["mode"] == "real" and plan.get("process_scope", "independent") != "independent":
        raise ValueError("真实采集产物必须使用独立进程作用域")
    if (plan["env"] != parameters(artifact) or plan["command"] != command(artifact)
            or plan["stdout_name"] != "run.log" or plan["watch"] != ["capture/reference.jsonl", "capture/live.jsonl"]):
        raise ValueError("执行计划与绑定产物参数不一致")


def preflight(binary, spec):
    """调用编译产物中的真实参数校验器，禁用联网opt-in并拒绝零测试。"""
    env = {k: v for k, v in os.environ.items() if not k.startswith("SHINNY_")}
    env.update({"TICKFLOW_GAP_" + k: v for k, v in spec.items()})
    env.update(TICKFLOW_GAP_PLAN_CHECK="1", TICKFLOW_GAP_REAL="0", TICKFLOW_LIVE="0")
    result = subprocess.run([str(binary), "-test.run=^TestGapPlanOfflinePreflight$", "-test.v", "-test.timeout=30s"],
                            env=env, capture_output=True, timeout=40)
    output = (result.stdout + result.stderr).decode("utf-8", "replace")
    if result.returncode or "--- PASS: TestGapPlanOfflinePreflight" not in output or "--- SKIP:" in output:
        raise ValueError("编译产物离线预检未通过：" + output[-1500:])
    return output


def check(plan):
    """启动前再次验证字节和未来窗口，拒绝过期准备单。"""
    check_binding(plan)
    artifact = plan["artifact"]
    data = Path(artifact["path"]).read_bytes()
    if hashlib.sha256(data).hexdigest() != artifact["sha256"]:
        raise ValueError("编译产物哈希不匹配")
    if artifact["mode"] == "real":
        preflight(Path(artifact["path"]), artifact["spec"])
    return data


def stage(root, plan, data):
    """向独占新目录落编译产物和清单；源产物后续变化不影响该次执行。"""
    artifact = plan["artifact"]
    target = root / Path(artifact["path"]).name
    with target.open("xb") as output:
        output.write(data)
    target.chmod(0o700)
    manifest = {k.lower(): v for k, v in artifact["spec"].items()}
    manifest.update(contract=artifact["spec"]["INS"], sha=artifact["source_sha"], provenance=artifact["mode"],
                    process_scope=plan.get("process_scope", "independent"),
                    binary_file=target.name, binary_sha256=artifact["sha256"],
                    plan_sha256=hashlib.sha256(json.dumps(plan, sort_keys=True).encode()).hexdigest())
    with (root / "manifest.json").open("x", encoding="utf-8") as output:
        json.dump(manifest, output, ensure_ascii=False, indent=2)


if __name__ == "__main__":
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")
