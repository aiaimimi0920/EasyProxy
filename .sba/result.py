"""仅写应用合同结果；不包含 provider 正文、URL、秘密或阶段私有证据。"""
from __future__ import annotations

import json
import os
from pathlib import Path
import re
import uuid

from request import ApplicationRequest


class ResultError(RuntimeError):
    pass


def write_result(path: Path, request: ApplicationRequest, *, status: str, checks: dict,
                 error_code: str | None = None, lifecycle: dict | None = None):
    if not (type(request) is ApplicationRequest and path.is_absolute() and path.resolve() == path
            and path.parent.is_dir() and not path.exists() and not path.is_symlink()
            and status in {"succeeded", "failed", "unknown"}
            and type(checks) is dict and len(checks) <= 30
            and all(type(key) is str and re.fullmatch(r"[a-z][a-z0-9-]{1,62}", key) is not None
                    and type(value) is bool for key, value in checks.items())
            and (status != "succeeded" or checks and all(checks.values()))
            and (error_code is None or re.fullmatch(r"[A-Z][A-Z0-9_]{1,79}", error_code) is not None)
            and (lifecycle is None or request.action in {"preview", "destroy-preview"})):
        raise ResultError("EASYPROXY_SBA_RESULT_REJECTED")
    value = {"schemaVersion": 3, "taskId": request.task_id, "action": request.action,
             "sourceSha": request.source_sha, "applicationVersion": request.application_version,
             "status": status, "checks": [{"id": key, "passed": passed} for key, passed in sorted(checks.items())]}
    if error_code is not None:
        value["errorCode"] = error_code
    if lifecycle is not None:
        value["lifecycle"] = lifecycle
    body = (json.dumps(value, ensure_ascii=True, separators=(",", ":")) + "\n").encode("utf-8")
    if len(body) > 32768:
        raise ResultError("EASYPROXY_SBA_RESULT_REJECTED")
    temporary = path.with_name("." + path.name + "." + uuid.uuid4().hex + ".tmp")
    descriptor = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(descriptor, "wb") as stream:
        stream.write(body)
        stream.flush()
        os.fsync(stream.fileno())
    # Atomic, create-only publication. Never overwrite a result from this or a
    # previous attempt. An interruption with two links is rejected by the runner.
    os.link(temporary, path)
    temporary.unlink()
