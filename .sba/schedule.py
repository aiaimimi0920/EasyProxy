"""stdlib-only bootstrap；工具安装不继承秘密，默认分支不承担发布实现。"""
from __future__ import annotations

import os
from pathlib import Path
import subprocess
import sys
import tempfile
import time

from deadline import MAX_SECONDS, action_deadline, remaining
from python_runtime import environment as python_environment, prepare_python
from refresh_context import CONFIG_VARIABLE, OWNER_VARIABLE, context, credentials, require


def child_environment(output: Path, source_environment) -> dict:
    context(source_environment)
    values = credentials(source_environment)
    # No GitHub token, Cloudflare token, repo/org secret sweep, PYTHONPATH or proxy.
    return {**python_environment(output), **values,
            **{key: source_environment.get(key) for key in
               (OWNER_VARIABLE, CONFIG_VARIABLE, "GITHUB_REPOSITORY", "EASYPROXY_SBA_ENVIRONMENT")}}


def run_schedule(*, environment):
    context(environment)
    credentials(environment)
    root = Path(__file__).resolve().parents[1]
    temporary = Path(environment.get("RUNNER_TEMP", ""))
    require(temporary.is_absolute() and temporary.resolve(strict=True) == temporary and temporary.is_dir()
            and root != temporary and root not in temporary.parents)
    output = Path(tempfile.mkdtemp(prefix="easyproxy-refresh-", dir=temporary))
    deadline = time.monotonic() + MAX_SECONDS
    env = child_environment(output, environment)
    with action_deadline(deadline):
        dispatcher = prepare_python(root / ".sba/requirements-linux.lock", output / "dispatcher-python")
        with (output / "selection.private.log").open("xb") as log:
            selected = output / "selected"
            result = subprocess.run([str(dispatcher.python), "-B", "-s", str(root / ".sba/refresh_select.py"),
                                     "--output", str(selected)], env=env, cwd=output,
                                    stdin=subprocess.DEVNULL, stdout=log, stderr=subprocess.STDOUT,
                                    timeout=remaining(deadline), check=False)
        require(result.returncode == 0)
        approved = selected / "source"
        # Installed dependencies belong to the approved source, not to main.
        runtime = prepare_python(approved / ".sba/requirements-linux.lock", output / "approved-python")
    # The approved child owns its alarm and audit cleanup. Parent allows cleanup
    # time instead of killing it at the same instant its business budget expires.
    with (output / "refresh.private.log").open("xb") as log:
        result = subprocess.run([str(runtime.python), "-B", "-s", str(approved / ".sba/run_refresh.py"),
                                 "--selection", str(selected / "selection.json"), "--output", str(output / "run"),
                                 "--deadline", str(deadline)], env=env, cwd=output,
                                stdin=subprocess.DEVNULL, stdout=log, stderr=subprocess.STDOUT,
                                timeout=remaining(deadline) + 120, check=False)
    require(result.returncode == 0)


if __name__ == "__main__":
    try:
        run_schedule(environment=os.environ)
    except Exception:
        raise SystemExit("EASYPROXY_SBA_SCHEDULE_FAILED_OR_UNKNOWN") from None
    print("EASYPROXY_SBA_SCHEDULE_VERIFIED")
