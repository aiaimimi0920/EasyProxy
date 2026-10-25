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
TLS_BEFORE = b"CTX = ssl.create_default_context()\nCTX.check_hostname = False\nCTX.verify_mode = ssl.CERT_NONE\n"
TLS_AFTER = b"CTX = ssl.create_default_context()\n"


def patch_source_tls(workspace: Path) -> dict:
    path = workspace / "subscribe/utils.py"
    require(path.is_file() and not path.is_symlink() and path.resolve(strict=True) == path, "RUNTIME_SOURCE")
    body = path.read_bytes()
    require(hashlib.sha256(body).hexdigest() == UTILS_SHA256 and body.count(TLS_BEFORE) == 1, "RUNTIME_PATCH")
    patched = body.replace(TLS_BEFORE, TLS_AFTER, 1)
    path.write_bytes(patched)
    return {"id": "source-https-verify-v1", "path": "subscribe/utils.py",
            "beforeSha256": UTILS_SHA256, "afterSha256": hashlib.sha256(patched).hexdigest()}


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
    patch = patch_source_tls(workspace)
    for path in paths.values():
        path.chmod(0o700)
    return {"sourceSha": source_sha, "platform": "linux/amd64", "binarySha256": dict(BINARIES),
            "binarySource": "approved-gitlink", "patches": [patch]}


def verify_runtime(workspace: Path, receipt: dict) -> None:
    require(receipt.get("sourceSha") == AGGREGATOR_SHA and receipt.get("binarySha256") == BINARIES,
            "RUNTIME_SOURCE")
    for relative, digest in BINARIES.items():
        path = workspace / relative
        require(path.is_file() and not path.is_symlink() and path.resolve(strict=True) == path
                and hashlib.sha256(path.read_bytes()).hexdigest() == digest, "RUNTIME_TOOL")
    patch = receipt["patches"][0]
    require(patch["id"] == "source-https-verify-v1" and hashlib.sha256(
        (workspace / patch["path"]).read_bytes()).hexdigest() == patch["afterSha256"], "RUNTIME_PATCH")
