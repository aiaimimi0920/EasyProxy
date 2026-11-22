"""已准备批准源码/依赖中的单动作进程；唯一结果路径由 SpringBok 提供。"""
from __future__ import annotations

import argparse
import os
from pathlib import Path
import time

from application import execute
from deadline import action_deadline
from diagnostics import result_diagnostics, safe_error_code
from refresh_context import manifest_release
from request import parse_request
from result import write_result
from sources import _git


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--request", required=True, type=Path)
    parser.add_argument("--result", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--deadline", required=True, type=float)
    args = parser.parse_args()
    root = Path(__file__).resolve().parents[1]
    request = None
    started = time.monotonic()
    try:
        version = manifest_release(root, _git(root, "rev-parse", "HEAD"))["applicationVersion"]
        request = parse_request(args.request.read_bytes(), application_version=version)
        with action_deadline(args.deadline):
            result = execute(request, root, args.output, environment=os.environ)
        elapsed_ms = int((time.monotonic() - started) * 1000)
        write_result(args.result, request, status="succeeded", checks=result["checks"],
                     lifecycle=result.get("lifecycle"),
                     diagnostics=result_diagnostics(duration_ms=elapsed_ms),
                     check_durations=result.get("check_durations"))
        return 0
    except Exception as error:
        if request is not None and not args.result.exists():
            elapsed_ms = int((time.monotonic() - started) * 1000)
            write_result(args.result, request, status="unknown", checks={},
                         error_code=safe_error_code(error),
                         diagnostics=result_diagnostics(error, failed_step="execute", duration_ms=elapsed_ms))
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
