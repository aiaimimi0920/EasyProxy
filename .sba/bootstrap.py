"""正式 PowerShell 入口的 stdlib bootstrap；源码外准备全部 gitlink 与私有 venv。"""
from __future__ import annotations

import argparse
import os
from pathlib import Path
import subprocess
import tempfile
import time

from deadline import MAX_SECONDS, action_deadline, remaining
from python_runtime import environment as python_environment, prepare_python
from refresh_context import manifest_release, require
from request import parse_request
from result import write_result
from sources import _git, inspect_checkout, materialize_workspace


def bootstrap(request_path: Path, result_path: Path, *, environment) -> int:
    root = Path(__file__).resolve().parents[1]
    require(request_path.is_absolute() and request_path.resolve(strict=True) == request_path
            and request_path.is_file() and not request_path.is_symlink() and request_path.stat().st_size <= 49152
            and result_path.is_absolute() and result_path.resolve() == result_path and not result_path.exists()
            and not result_path.is_symlink() and result_path.parent == request_path.parent)
    version = manifest_release(root, _git(root, "rev-parse", "HEAD"))["applicationVersion"]
    request = parse_request(request_path.read_bytes(), application_version=version)
    temporary = Path(environment.get("RUNNER_TEMP", ""))
    require(temporary.is_absolute() and temporary.resolve(strict=True) == temporary and temporary.is_dir()
            and temporary == result_path.parent and root != temporary and root not in temporary.parents)
    values = request.credentials(environment)  # Verify returns {} without reading any env secret.
    deadline = time.monotonic() + MAX_SECONDS
    try:
        with action_deadline(deadline):
            inspect_checkout(root, request.config.repository, request.source_sha)
            output = Path(tempfile.mkdtemp(prefix="easyproxy-sba-", dir=temporary))
            prepared = output / "source"
            materialize_workspace(prepared, request.config.repository, request.source_sha, cache_root=root)
            require(manifest_release(prepared, request.source_sha)["applicationVersion"] == version)
            runtime = prepare_python(prepared / ".sba/requirements-linux.lock", output / "python")
        env = {**python_environment(output), **values, "RUNNER_TEMP": str(output)}
        with (output / "execution.private.log").open("xb") as log:
            result = subprocess.run([str(runtime.python), "-B", "-s", str(prepared / ".sba/execute.py"),
                "--request", str(request_path), "--result", str(result_path), "--output", str(output / "operation"),
                "--deadline", str(deadline)], env=env, cwd=output, stdin=subprocess.DEVNULL,
                stdout=log, stderr=subprocess.STDOUT, timeout=remaining(deadline) + 120, check=False)
        if not result_path.exists():
            write_result(result_path, request, status="unknown", checks={}, error_code="EASYPROXY_SBA_RESULT_MISSING")
            return 1
        return result.returncode
    except Exception:
        if not result_path.exists():
            write_result(result_path, request, status="unknown", checks={}, error_code="EASYPROXY_SBA_BOOTSTRAP_UNKNOWN")
        return 1


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--request", required=True, type=Path)
    parser.add_argument("--result", required=True, type=Path)
    arguments = parser.parse_args()
    try:
        raise SystemExit(bootstrap(arguments.request, arguments.result, environment=os.environ))
    except Exception:
        raise SystemExit("EASYPROXY_SBA_BOOTSTRAP_REJECTED") from None
