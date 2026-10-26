"""批准 gitlink 的 Linux 工具与源 HTTPS 适配；只修改本次隔离生成副本。"""
from __future__ import annotations

import hashlib
from pathlib import Path
import platform
import sys

from artifact_store import require


AGGREGATOR_SHA = "089fb85414efff4c7deccfda48b5a540ce536835"
BINARIES = {
    "clash/clash-linux-amd": "3e92df24f5e80e86b9cf9183ceb7bb575f0bd132a9dc4081dae42e80f21076ae",
    "subconverter/subconverter-linux-amd": "04a0e0bb933a4b46ed0946ab9a9344e1dd9103ceebecc661b604215383841a21",
}
UTILS_SHA256 = "a298f46c5b684982de2bb5830bb5afe8950cc914b605d49fc98e5fe1b930427f"
TLS_PATCH_SHA256 = "efef37dbf1b18614448607465e6ab627b8579e9a3596d4fdefc91573555d0a29"
WORKFLOW_SHA256 = "ce4f361afb9667aa4e1d95ac583977a2f5b25c6ee7fbc623c11a8795e94adf0b"
TASK_PATCH_SHA256 = "bd304b06f03c9c5bc3937b59af125d62dc0b62803e686019ec22b721d823ab2e"
TLS_BEFORE = b"CTX = ssl.create_default_context()\nCTX.check_hostname = False\nCTX.verify_mode = ssl.CERT_NONE\n"
TLS_AFTER = b"CTX = ssl.create_default_context()\n"
TASK_BEFORE = b"    taskid = task_conf.taskid\n    proxies = execute(task_conf=task_conf)\n    return (taskid, proxies)\n"
TASK_AFTER = (b"    taskid = task_conf.taskid\n    try:\n        proxies = execute(task_conf=task_conf)\n"
              b"    except Exception:\n        logger.error(\"[SBA] subscription task failed\")\n"
              b"        proxies = []\n    return (taskid, proxies)\n")


def _patch_receipts():
    return [{"id": name, "path": path, "beforeSha256": before, "afterSha256": after} for name, path, before, after in (
        ("source-https-verify-v1", "subscribe/utils.py", UTILS_SHA256, TLS_PATCH_SHA256),
        ("subscription-task-isolation-v1", "subscribe/workflow.py", WORKFLOW_SHA256, TASK_PATCH_SHA256))]


def _workflow_source(workspace: Path):
    path = workspace / "subscribe/workflow.py"
    require(path.is_file() and not path.is_symlink() and path.resolve(strict=True) == path, "RUNTIME_SOURCE")
    body = path.read_bytes()
    require(hashlib.sha256(body).hexdigest() == WORKFLOW_SHA256 and body.count(TASK_BEFORE) == 1, "RUNTIME_PATCH")
    return path, body


def patch_source_tls(workspace: Path) -> dict:
    path = workspace / "subscribe/utils.py"
    require(path.is_file() and not path.is_symlink() and path.resolve(strict=True) == path, "RUNTIME_SOURCE")
    body = path.read_bytes()
    require(hashlib.sha256(body).hexdigest() == UTILS_SHA256 and body.count(TLS_BEFORE) == 1, "RUNTIME_PATCH")
    patched = body.replace(TLS_BEFORE, TLS_AFTER, 1)
    require(hashlib.sha256(patched).hexdigest() == TLS_PATCH_SHA256, "RUNTIME_PATCH")
    path.write_bytes(patched)
    return _patch_receipts()[0]


def patch_source_tasks(workspace: Path) -> dict:
    """坏源返回原编号的空结果，不丢弃其他任务；取消与结构错误仍向外传播。"""
    path, body = _workflow_source(workspace)
    patched = body.replace(TASK_BEFORE, TASK_AFTER, 1)
    require(hashlib.sha256(patched).hexdigest() == TASK_PATCH_SHA256, "RUNTIME_PATCH")
    path.write_bytes(patched)
    return _patch_receipts()[1]


def prepare_runtime(workspace: Path, source_sha: str) -> dict:
    require(sys.platform == "linux" and platform.machine().lower() == "x86_64", "RUNTIME_HOST")
    require(source_sha == AGGREGATOR_SHA and workspace.is_absolute()
            and workspace.resolve(strict=True) == workspace, "RUNTIME_SOURCE")
    # Validate every executable before modifying the isolated source or enabling execution.
    paths = {}
    for relative, digest in BINARIES.items():
        path = workspace / relative
        require(path.is_file() and not path.is_symlink() and path.resolve(strict=True) == path
                and hashlib.sha256(path.read_bytes()).hexdigest() == digest, "RUNTIME_TOOL")
        paths[relative] = path
    # 校验全部源后才修改隔离副本；固定源码、补丁后字节及工具均不可漂移。
    _workflow_source(workspace)
    patches = [patch_source_tls(workspace), patch_source_tasks(workspace)]
    for path in paths.values():
        path.chmod(0o700)
    return {"sourceSha": source_sha, "platform": "linux/amd64", "binarySha256": dict(BINARIES),
            "binarySource": "approved-gitlink", "patches": patches}


def verify_runtime(workspace: Path, receipt: dict) -> None:
    require(receipt.get("sourceSha") == AGGREGATOR_SHA and receipt.get("binarySha256") == BINARIES,
            "RUNTIME_SOURCE")
    for relative, digest in BINARIES.items():
        path = workspace / relative
        require(path.is_file() and not path.is_symlink() and path.resolve(strict=True) == path
                and hashlib.sha256(path.read_bytes()).hexdigest() == digest, "RUNTIME_TOOL")
    require(receipt.get("patches") == _patch_receipts(), "RUNTIME_PATCH")
    for patch in _patch_receipts():
        path = workspace / patch["path"]
        require(path.is_file() and not path.is_symlink() and path.resolve(strict=True) == path
                and hashlib.sha256(path.read_bytes()).hexdigest() == patch["afterSha256"], "RUNTIME_PATCH")
