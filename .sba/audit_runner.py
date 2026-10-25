"""审计专用子进程组与单次补清理；异常一律 UNKNOWN，不释放共同 writer。"""
from __future__ import annotations

import importlib.util
import os
from pathlib import Path
import signal
import subprocess
import time

from artifact_store import ArtifactError, require


def _confirm_group_dead(group: int) -> None:
    # Waiting for the leader does not wait for orphaned descendants. A successful
    # killpg only queues signals; do not race a still-running docker CLI with rm.
    deadline = time.monotonic() + 10
    while True:
        live = False
        try:
            for process in Path("/proc").iterdir():
                if not process.name.isdecimal():
                    continue
                try:
                    fields = (process / "stat").read_text(encoding="utf-8").rsplit(")", 1)[1].split()
                except FileNotFoundError:
                    continue
                if int(fields[2]) == group and fields[0] not in ("Z", "X"):
                    live = True
                    break
        except (OSError, UnicodeError, ValueError, IndexError):
            raise ArtifactError("EASYPROXY_SBA_ARTIFACT_AUDIT_TERMINATION_UNKNOWN") from None
        if not live:
            return
        if time.monotonic() >= deadline:
            raise ArtifactError("EASYPROXY_SBA_ARTIFACT_AUDIT_TERMINATION_UNKNOWN")
        time.sleep(.05)


def container_owner(root: Path, output: Path, audit_id: str, image_id: str, source_sha: str):
    spec = importlib.util.spec_from_file_location("sba_audit_lifecycle", root / "scripts/easyproxy_audit_lifecycle.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    name = ("easyproxy-source-audit-" + audit_id).lower().replace("_", "-")
    return module.AuditContainer(name, image_id, source_sha, audit_id,
                                 output / "runtime/container-cleanup-attempt.json")


def run_audit(args: list[str], output: Path, env: dict, *, lifecycle, timeout: int = 1200) -> None:
    require(os.name == "posix", "AUDIT_LINUX")
    child = None
    try:
        with (output / "audit.private.log").open("xb") as log:
            child = subprocess.Popen(args, cwd=output, env=env, stdin=subprocess.DEVNULL,
                                     stdout=log, stderr=subprocess.STDOUT, start_new_session=True)
            code = child.wait(timeout=timeout)
        if code == 0:
            return
    except (OSError, subprocess.SubprocessError):
        pass
    if child is not None:
        # Also kill the group after an ordinary nonzero/signal exit: the leader may
        # have died while a docker CLI descendant is still creating the container.
        try:
            os.killpg(child.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        except OSError:
            raise ArtifactError("EASYPROXY_SBA_ARTIFACT_AUDIT_TERMINATION_UNKNOWN") from None
        try:
            child.wait(timeout=10)
        except subprocess.SubprocessError:
            raise ArtifactError("EASYPROXY_SBA_ARTIFACT_AUDIT_TERMINATION_UNKNOWN") from None
        _confirm_group_dead(child.pid)
    # A claimed cleanup may already have reached Docker. Never retry an ambiguous removal.
    if not lifecycle.journal.exists():
        try:
            lifecycle.journal.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
            lifecycle.cleanup()
        except (OSError, RuntimeError, subprocess.SubprocessError):
            pass
    raise ArtifactError("EASYPROXY_SBA_ARTIFACT_AUDIT_UNKNOWN")
