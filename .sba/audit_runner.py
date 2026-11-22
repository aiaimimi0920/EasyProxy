"""审计专用子进程组与单次补清理；区分已确认结束与终止/清理未知。"""
from __future__ import annotations

from contextlib import contextmanager
import importlib.util
import os
from pathlib import Path
import signal
import subprocess
import threading
import time

from artifact_store import ArtifactError, require


class AuditStoppedError(ArtifactError):
    """审计失败，但进程组已终止且准确审计容器已确认不存在。"""


@contextmanager
def _defer_sigalrm():
    """在已设短超时的进程组终止与单次容器清理期间暂缓 SIGALRM，避免清理中途抛出 OSError。"""
    if threading.current_thread() is not threading.main_thread() or not hasattr(signal, "getitimer"):
        yield
        return
    rem, interval = signal.getitimer(signal.ITIMER_REAL)
    if rem <= 0:
        yield
        return
    started = time.monotonic()
    signal.setitimer(signal.ITIMER_REAL, 0)
    try:
        yield
    finally:
        left = max(0.05, rem - (time.monotonic() - started))
        signal.setitimer(signal.ITIMER_REAL, left, interval)


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
    preclaimed = lifecycle.journal.exists()
    code = None
    try:
        with (output / "audit.private.log").open("xb") as log:
            child = subprocess.Popen(args, cwd=output, env=env, stdin=subprocess.DEVNULL,
                                     stdout=log, stderr=subprocess.STDOUT, start_new_session=True)
            code = child.wait(timeout=timeout)
    except (OSError, subprocess.SubprocessError):
        code = None
    with _defer_sigalrm():
        if code == 0:
            if child is not None:
                try:
                    os.killpg(child.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                except OSError:
                    raise ArtifactError("EASYPROXY_SBA_ARTIFACT_AUDIT_TERMINATION_UNKNOWN") from None
                _confirm_group_dead(child.pid)
            try:
                lifecycle.assert_absent()
            except (OSError, RuntimeError, subprocess.SubprocessError):
                raise ArtifactError("EASYPROXY_SBA_ARTIFACT_AUDIT_CLEANUP_UNKNOWN") from None
            return
        if child is not None:
            # If the child just claimed the single cleanup journal before timeout/deadline,
            # let its in-flight docker rm finish rather than SIGKILLing it mid-removal.
            if not preclaimed and lifecycle.journal.exists() and child.poll() is None:
                try:
                    child.wait(timeout=min(65, max(5, timeout)))
                except subprocess.SubprocessError:
                    pass
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
        try:
            lifecycle.assert_absent()
        except (OSError, RuntimeError, subprocess.SubprocessError):
            raise ArtifactError("EASYPROXY_SBA_ARTIFACT_AUDIT_CLEANUP_UNKNOWN") from None
    raise AuditStoppedError("EASYPROXY_SBA_ARTIFACT_AUDIT_UNKNOWN")
