"""在无部署凭据的新进程中预检固定源码；不调用任何套件发布或 provider 客户端。"""
from __future__ import annotations

import argparse
import json
from pathlib import Path
import re
import subprocess
import sys


REPOSITORY = "aiaimimi0920/EasyProxy"
STAGES = {"source", "python", "process", "inspection", "tools", "runtime", "image", "sealer", "generation", "workflow"}
GENERATION_DIAGNOSTICS = frozenset({
    "EASYPROXY_SBA_GENERATION_" + stage + "_UNKNOWN" for stage in (
        "SOURCE", "RUNTIME", "CONFIG", "COLLECT", "READBACK", "OUTPUTS", "CONVERSION", "VALIDATION")
} | {
    "EASYPROXY_SBA_ARTIFACT_" + code for code in (
        "SECRETS", "OUTPUT", "SOURCE", "CONFIG", "RUNTIME_SOURCE", "RUNTIME_HOST", "RUNTIME_PATCH",
        "RUNTIME_TOOL", "BUILD_CONFIG", "BUILD_GENERATION", "V2RAY", "INPUT", "EMPTY", "FORMAT")
})


def report(path: Path, sha: str, stage: str, *, passed: bool, checks: dict | None = None, diagnostic_code=None):
    if stage not in STAGES or re.fullmatch(r"[a-f0-9]{40}", sha) is None:
        raise ValueError("EASYPROXY_PREFLIGHT_INPUT")
    if diagnostic_code is not None and (passed or stage != "generation"
            or diagnostic_code not in GENERATION_DIAGNOSTICS):
        raise ValueError("EASYPROXY_PREFLIGHT_INPUT")
    value = {"schemaVersion": 1, "sourceSha": sha, "passed": passed, "cloudWrites": False,
             "stage": stage, "checks": checks or {}}
    if not passed:
        value["errorCode"] = "EASYPROXY_PREFLIGHT_" + stage.upper()
        if diagnostic_code is not None:
            value["diagnosticCode"] = diagnostic_code
    with path.open("x", encoding="utf-8", newline="\n") as stream:
        json.dump(value, stream, indent=2)
        stream.write("\n")
    print(json.dumps(value), flush=True)


def inspect_prepared(root: Path, output: Path, sha: str, report_path: Path) -> int:
    # This function runs only in a fresh interpreter. Imports resolve to the
    # exact target checkout, not the diagnostic driver's checkout.
    sys.path.insert(0, str(root / ".sba"))
    from aggregator_build import DISABLED_SEED, SEED_SECRETS, build_aggregator
    from audit_image import build_image, build_runtime, image_identity
    from github_environment import reviewed_workflow
    from sealed_box import build_sealer
    from sources import inspect_checkout
    from toolchain import prepare_linux_tools

    stage, checks = "inspection", {}
    try:
        inspect_checkout(root, REPOSITORY, sha)
        checks["exact-source"] = True
        stage = "tools"
        tools = prepare_linux_tools(output / "tools")
        checks[stage] = True
        print("PREFLIGHT_TOOLS_PASSED", flush=True)
        stage = "runtime"
        runtime = build_runtime(root, REPOSITORY.lower(), sha, tools, output / "runtime")
        checks[stage] = True
        print("PREFLIGHT_RUNTIME_PASSED", flush=True)
        stage = "image"
        image_identity(build_image(runtime, output / "image"))
        checks[stage] = True
        print("PREFLIGHT_IMAGE_PASSED", flush=True)
        stage = "sealer"
        build_sealer(root, REPOSITORY.lower(), sha, tools, output / "sealer")
        checks[stage] = True
        stage = "generation"
        build_aggregator(root, REPOSITORY.lower(), sha, output / "generation",
                         seed_secrets={name: DISABLED_SEED for name in SEED_SECRETS})
        checks[stage] = True
        stage = "workflow"
        reviewed_workflow(root)
        inspect_checkout(root, REPOSITORY, sha)
        checks[stage] = True
    except Exception as error:
        diagnostic_code = None
        if stage == "generation":
            # 类型来自准确目标 checkout；只匹配有限常量，不导出异常正文或私有日志。
            from artifact_store import ArtifactError
            target_diagnostics = sys.modules.get("diagnostics")
            stage_error = getattr(target_diagnostics, "StageError", None)
            # 老目标可能没有 diagnostics；不为诊断而回退加载 driver 的新模块。
            if type(error) in (ArtifactError, stage_error) and str(error) in GENERATION_DIAGNOSTICS:
                diagnostic_code = str(error)
        report(report_path, sha, stage, passed=False, checks=checks, diagnostic_code=diagnostic_code)
        return 1
    report(report_path, sha, stage, passed=True, checks=checks)
    return 0


def prepare(root: Path, output: Path, sha: str, report_path: Path) -> int:
    from deadline import MAX_SECONDS
    from python_runtime import environment, prepare_python
    from sources import materialize_workspace

    stage = "source"
    try:
        output.mkdir(mode=0o700, parents=True, exist_ok=False)
        source = output / "source"
        materialize_workspace(source, REPOSITORY, sha, cache_root=root)
        stage = "python"
        runtime = prepare_python(source / ".sba/requirements-linux.lock", output / "python")
        stage = "process"
        env = {**environment(output), "RUNNER_TEMP": str(output)}
        result = subprocess.run([str(runtime.python), "-B", "-s", str(Path(__file__).resolve()),
            "--prepared", str(source), "--source-sha", sha, "--output", str(output), "--report", str(report_path)],
            env=env, cwd=output, stdin=subprocess.DEVNULL, timeout=MAX_SECONDS, check=False)
        if report_path.is_file():
            return 0 if result.returncode == 0 and json.loads(report_path.read_bytes())["passed"] is True else 1
    except Exception:
        pass
    if not report_path.exists():
        report(report_path, sha, stage, passed=False)
    return 1


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--source-sha", required=True)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--report", required=True, type=Path)
    parser.add_argument("--prepared", type=Path)
    args = parser.parse_args()
    root = Path(__file__).resolve().parents[1]
    if (re.fullmatch(r"[a-f0-9]{40}", args.source_sha) is None or not args.output.is_absolute()
            or args.output.resolve() != args.output or root == args.output or root in args.output.parents
            or not args.report.is_absolute() or args.report.resolve() != args.report or args.report.exists()
            or not args.report.parent.is_dir() or args.report.parent == args.output or args.output in args.report.parents):
        raise SystemExit("EASYPROXY_PREFLIGHT_INPUT")
    if args.prepared is not None:
        if args.prepared != args.output / "source" or not args.output.is_dir():
            raise SystemExit("EASYPROXY_PREFLIGHT_INPUT")
        return inspect_prepared(args.prepared, args.output, args.source_sha, args.report)
    return prepare(root, args.output, args.source_sha, args.report)


if __name__ == "__main__":
    raise SystemExit(main())
