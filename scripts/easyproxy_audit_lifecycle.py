"""严格审计的单次清理；仅删除本次标签、源码和镜像都匹配的精确容器 ID。"""
from __future__ import annotations

import json
import os
from pathlib import Path
import re
import subprocess


class AuditContainer:
    def __init__(self, name: str, image_id: str, source_sha: str, operation: str, journal: Path):
        if (re.fullmatch(r"[a-z0-9][a-z0-9-]{0,127}", name) is None
                or re.fullmatch(r"sha256:[a-f0-9]{64}", image_id) is None
                or re.fullmatch(r"[a-f0-9]{40}", source_sha) is None
                or re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.-]{0,95}", operation) is None):
            raise ValueError("EASYPROXY_AUDIT_CONTAINER_IDENTITY")
        self.name, self.image_id, self.source_sha = name, image_id, source_sha
        self.operation, self.journal = operation, journal

    def _run(self, args: list[str]):
        return subprocess.run(["docker", *args], capture_output=True, text=True, stdin=subprocess.DEVNULL,
                              timeout=30, check=False)

    def _ids(self) -> list[str]:
        result = self._run(["container", "ls", "--all", "--no-trunc", "--filter",
                            "name=^/" + self.name + "$", "--format", "{{.ID}}"])
        ids = result.stdout.splitlines()
        if (result.returncode != 0 or len(ids) > 1
                or any(re.fullmatch(r"[a-f0-9]{64}", value) is None for value in ids)):
            raise RuntimeError("EASYPROXY_AUDIT_CONTAINER_LOOKUP_UNKNOWN")
        return ids

    def assert_absent(self) -> None:
        if self._ids():
            raise RuntimeError("EASYPROXY_AUDIT_CONTAINER_ALREADY_EXISTS")

    def assert_startable(self) -> None:
        if os.path.lexists(self.journal):
            raise RuntimeError("EASYPROXY_AUDIT_CLEANUP_ALREADY_ATTEMPTED")
        self.assert_absent()

    def label_args(self) -> list[str]:
        return ["--label", "easyproxy.audit.operation=" + self.operation,
                "--label", "easyproxy.audit.source=" + self.source_sha]

    def verify(self) -> str:
        ids = self._ids()
        if len(ids) != 1:
            raise RuntimeError("EASYPROXY_AUDIT_CONTAINER_MISSING")
        container_id = ids[0]
        result = self._run(["container", "inspect", container_id])
        try:
            if result.returncode != 0 or len(result.stdout) > 256 * 1024:
                raise ValueError("inspect")
            rows = json.loads(result.stdout)
            if type(rows) is not list or len(rows) != 1:
                raise ValueError("shape")
            row = rows[0]
            labels = row.get("Config", {}).get("Labels", {})
            if (row.get("Id") != container_id or row.get("Name") != "/" + self.name
                    or row.get("Image") != self.image_id
                    or labels.get("easyproxy.audit.operation") != self.operation
                    or labels.get("easyproxy.audit.source") != self.source_sha):
                raise ValueError("owner")
        except (ValueError, TypeError, AttributeError):
            raise RuntimeError("EASYPROXY_AUDIT_CONTAINER_OWNER_MISMATCH") from None
        return container_id

    def cleanup(self) -> None:
        # Claim before inspecting/removing. The parent process uses the same exclusive file,
        # so UNKNOWN or an interrupted rm is never automatically sent a second time.
        try:
            fd = os.open(self.journal, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        except FileExistsError:
            raise RuntimeError("EASYPROXY_AUDIT_CLEANUP_ALREADY_ATTEMPTED") from None
        with os.fdopen(fd, "w", encoding="utf-8") as stream:
            json.dump({"operation": self.operation, "container": self.name}, stream)
            stream.flush()
            os.fsync(stream.fileno())
        if not self._ids():
            return
        container_id = self.verify()
        result = self._run(["rm", "--force", container_id])
        if result.returncode != 0 or result.stdout.strip() != container_id:
            raise RuntimeError("EASYPROXY_AUDIT_CLEANUP_UNCONFIRMED")
