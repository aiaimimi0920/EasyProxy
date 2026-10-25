"""从批准根构建订阅审计专用运行时；固定基础镜像、最终 image ID 与来源。"""
from __future__ import annotations

from dataclasses import dataclass
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess

from sources import inspect_checkout
from toolchain import LinuxTools, _run, tool_environment


# Official docker-library/python linux/amd64 manifest, verified online 2026-10-26.
BASE_IMAGE = "docker.io/library/python@sha256:57d96f71de145a98e7ceca18ee4ade77bba4103a33a72dfaea026b23625c6bf0"
IMAGE_ID = re.compile(r"sha256:[a-f0-9]{64}\Z")
SHA = re.compile(r"[a-f0-9]{40}\Z")
DIGEST = re.compile(r"[a-f0-9]{64}\Z")
REPOSITORY = re.compile(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+\Z")


class AuditImageError(RuntimeError):
    pass


def require(value):
    if not value:
        raise AuditImageError("EASYPROXY_SBA_AUDIT_IMAGE")


@dataclass(frozen=True)
class BuiltRuntime:
    binary: Path
    repository: str
    source_sha: str
    binary_sha256: str


@dataclass(frozen=True)
class AuditImage:
    image_id: str
    repository: str
    source_sha: str
    binary_sha256: str


def image_identity(image: AuditImage) -> dict:
    require(type(image) is AuditImage and IMAGE_ID.fullmatch(image.image_id) is not None
            and REPOSITORY.fullmatch(image.repository) is not None and SHA.fullmatch(image.source_sha) is not None
            and DIGEST.fullmatch(image.binary_sha256) is not None)
    return {"imageId": image.image_id, "repository": image.repository, "sourceSha": image.source_sha,
            "binarySha256": image.binary_sha256, "baseImage": BASE_IMAGE, "platform": "linux/amd64",
            "scope": "subscription-only-v1"}


def labels(image: AuditImage) -> dict:
    value = image_identity(image)
    return {"org.opencontainers.image.source": "https://github.com/" + value["repository"],
            "org.opencontainers.image.revision": value["sourceSha"],
            "easyproxy.audit.runtime-sha256": value["binarySha256"], "easyproxy.audit.base": BASE_IMAGE,
            "easyproxy.audit.scope": value["scope"]}


def environment(output: Path) -> dict:
    return {"PATH": os.environ.get("PATH", os.defpath), "HOME": str(output / "home"),
            "DOCKER_CONFIG": str(output / "home"), "TEMP": str(output), "TMP": str(output),
            **{key: value for key, value in os.environ.items() if key.upper() in {"SYSTEMROOT", "WINDIR"}}}


def _docker(args: list[str], env: dict, timeout: int = 60) -> bytes:
    try:
        result = subprocess.run(["docker", *args], env=env, stdin=subprocess.DEVNULL,
                                capture_output=True, timeout=timeout, check=False)
        require(result.returncode == 0 and len(result.stdout) <= 256 * 1024)
        return result.stdout
    except (OSError, subprocess.SubprocessError):
        raise AuditImageError("EASYPROXY_SBA_AUDIT_IMAGE") from None


def verify_image(image: AuditImage, env: dict) -> dict:
    expected = image_identity(image)
    try:
        rows = json.loads(_docker(["image", "inspect", image.image_id], env))
        require(type(rows) is list and len(rows) == 1)
        row = rows[0]
        require(row.get("Id") == image.image_id and row.get("Os") == "linux" and row.get("Architecture") == "amd64")
        actual_labels = row.get("Config", {}).get("Labels", {})
        require(type(actual_labels) is dict and all(actual_labels.get(key) == value for key, value in labels(image).items()))
        return expected
    except (ValueError, TypeError, AttributeError):
        raise AuditImageError("EASYPROXY_SBA_AUDIT_IMAGE") from None


def build_runtime(root: Path, repository: str, sha: str, tools: LinuxTools, output: Path) -> BuiltRuntime:
    require(root.is_absolute() and output.is_absolute() and output.resolve() == output
            and not output.exists() and root.resolve() not in output.parents)
    root = root.resolve(strict=True)
    inspect_checkout(root, repository, sha)
    output.mkdir(mode=0o700, parents=True)
    env = tool_environment(tools, output)
    binary = output / "easy-proxy"
    _run([str(tools.go), "-C", str(root / "service/base"), "build", "-trimpath", "-buildvcs=false",
          "-tags", "with_utls with_quic with_grpc with_wireguard with_gvisor", "-o", str(binary), "./cmd/easy_proxies"],
         output, env, "audit-runtime-build", timeout=600)
    inspect_checkout(root, repository, sha)
    result = BuiltRuntime(binary, repository, sha, hashlib.sha256(binary.read_bytes()).hexdigest())
    (output / "runtime-provenance.json").write_text(json.dumps({"repository": repository, "sourceSha": sha,
        "binarySha256": result.binary_sha256, "goVersion": "1.27.3", "scope": "subscription-only-v1"}, indent=2), encoding="utf-8")
    return result


def build_image(runtime: BuiltRuntime, output: Path) -> AuditImage:
    require(type(runtime) is BuiltRuntime and runtime.binary.is_absolute() and runtime.binary.is_file()
            and not runtime.binary.is_symlink() and runtime.binary.resolve(strict=True) == runtime.binary
            and hashlib.sha256(runtime.binary.read_bytes()).hexdigest() == runtime.binary_sha256)
    # Validate metadata before starting Docker or creating a build context.
    pending = AuditImage("sha256:" + "0" * 64, runtime.repository, runtime.source_sha, runtime.binary_sha256)
    expected_labels = labels(pending)
    require(output.is_absolute() and output.resolve() == output and not output.exists()
            and runtime.binary.parent not in output.parents)
    output.mkdir(mode=0o700, parents=True)
    (output / "home").mkdir(mode=0o700)
    shutil.copyfile(runtime.binary, output / "easy-proxy")
    (output / "easy-proxy").chmod(0o700)
    (output / "Dockerfile").write_text("FROM --platform=linux/amd64 " + BASE_IMAGE + "\n"
        "COPY easy-proxy /usr/local/bin/easy-proxy\n"
        'ENTRYPOINT ["/usr/local/bin/easy-proxy"]\n'
        'CMD ["--config", "/var/lib/easyproxy/config/config.yaml"]\n', encoding="utf-8")
    args = ["build", "--network", "none", "--platform", "linux/amd64", "--iidfile", str(output / "image-id")]
    for key, value in expected_labels.items():
        args.extend(["--label", key + "=" + value])
    args.extend(["-f", str(output / "Dockerfile"), str(output)])
    env = environment(output)
    _docker(args, env, timeout=300)
    result = AuditImage((output / "image-id").read_text(encoding="utf-8").strip(),
                        runtime.repository, runtime.source_sha, runtime.binary_sha256)
    identity = verify_image(result, env)
    require(hashlib.sha256((output / "easy-proxy").read_bytes()).hexdigest() == runtime.binary_sha256)
    (output / "image-provenance.json").write_text(json.dumps(identity, indent=2), encoding="utf-8")
    return result
