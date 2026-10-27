"""持有共同许可的 ECH/R2 出口发布阶段；不是整套动作的成功回执。"""
from __future__ import annotations

from dataclasses import dataclass
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import subprocess
import sys
import time

from coordination import Coordinator, Permit, inventory_digest
from domains import bind_domain
from origins import inventory_origin
from sources import inspect_checkout
from worker_api import WorkerAPI, WorkerError, credential, public_release, require


# Exact x64 binaries from official esbuild@0.28.2 esbuild.binaryHashes.
# No floating npx/package download at deploy time, or cross-OS fallback.
ESBUILD_VERSION = "0.28.2"
ESBUILD_TOOLS = {
    "win32": ("esbuild.exe", "c7bee37877d0aa6a046e52783fa0a2cf1a9ce5579d68bb3083bda99d4bff18ef"),
    "linux": ("esbuild", "e1698a3d5c6c0798fee4fd3b5cc816651f460c63d390a7a26ea4beb0b1884100"),
}
COMPATIBILITY_DATE = "2025-01-15"
SOURCE_FILES = {".sba/edge.mjs", ".sba/aggregator-assets.mjs", "workers/ech-workers-cloudflare/src/index.js",
                "workers/ech-workers-cloudflare/src/auth.mjs"}


@dataclass(frozen=True)
class EchDeployment:
    root: Path
    esbuild: Path
    workdir: Path
    worker_name: str
    inventory: list[dict]


def _tags(owner: Coordinator, release: dict) -> list[str]:
    return ["easyproxy-sba:" + owner.inventory_digest, "easyproxy-sha:" + release["sourceSha"],
            "easyproxy-version:" + release["applicationVersion"]]


def metadata(owner: Coordinator, release: dict, ech_token: str) -> dict:
    require(credential(ech_token) and re.fullmatch(r"[!#$%&'*+.^_`|~0-9A-Za-z-]+", ech_token) is not None,
            "ECH_CREDENTIAL")  # ECH token also travels as a WebSocket subprotocol.
    return {"main_module": "edge.mjs", "compatibility_date": COMPATIBILITY_DATE, "tags": _tags(owner, release),
            "observability": {"enabled": True},
            "bindings": [{"type": "r2_bucket", "name": "AGGREGATOR_STORE", "bucket_name": owner.identity["bucket"]},
                         {"type": "secret_text", "name": "ECH_TOKEN", "text": ech_token},
                         {"type": "plain_text", "name": "SBA_PURPOSE", "text": owner.identity["purpose"]},
                         {"type": "plain_text", "name": "SBA_RELEASE_SHA", "text": release["sourceSha"]},
                         {"type": "plain_text", "name": "SBA_RELEASE_VERSION", "text": release["applicationVersion"]}]}


def _verify_settings(value: dict, expected: dict):
    require(type(value.get("tags")) is list and all(isinstance(tag, str) for tag in value["tags"])
            and sorted(value["tags"]) == sorted(expected["tags"])
            and value.get("compatibility_date") == COMPATIBILITY_DATE, "OWNERSHIP")
    require(type(value.get("observability")) is dict and value["observability"].get("enabled") is True,
            "OBSERVABILITY")
    bindings = value.get("bindings")
    require(type(bindings) is list and len(bindings) == len(expected["bindings"])
            and all(type(item) is dict and isinstance(item.get("name"), str) for item in bindings)
            and len({item["name"] for item in bindings}) == len(bindings), "BINDINGS")
    actual = {item["name"]: item for item in bindings}
    for item in expected["bindings"]:
        found = actual.get(item["name"], {})
        require(found.get("type") == item["type"], "BINDINGS")
        # Cloudflare deliberately does not return secret text. Never require it
        # in a read-back response or expose it in a result/diagnostic.
        for key in ("bucket_name", "text"):
            if key in item and item["type"] != "secret_text":
                require(found.get(key) == item[key], "BINDINGS")


def _propagation_delay():
    time.sleep(5)


def _verify_public(origin: str, release: dict):
    # Read-only propagation polling is not publication retry. Never send the
    # Worker upload/subdomain mutation again just because its public URL is late.
    for attempt in range(12):
        try:
            observed = public_release(origin)
            if (type(observed.get("schemaVersion")) is int and
                    observed == {"schemaVersion": 1, "applicationId": "easyproxy-cloud", **release}):
                return
        except WorkerError:
            pass
        if attempt < 11:
            _propagation_delay()
    raise WorkerError("EASYPROXY_SBA_WORKER_PUBLIC_VERSION_UNKNOWN")


def _compiler_tool() -> tuple[str, str]:
    require(sys.platform in ESBUILD_TOOLS and platform.machine().lower() in ("amd64", "x86_64"), "TOOLCHAIN")
    return ESBUILD_TOOLS[sys.platform]


def build_bundle(root: Path, esbuild: Path, workdir: Path) -> bytes:
    """调用已固定摘要的真实 esbuild；输出目录必须全新且在批准源码之外。"""
    for path in (root, esbuild, workdir):
        require(isinstance(path, Path) and path.is_absolute() and not str(path).startswith(("\\\\", "//")), "PATH")
    root = root.resolve(strict=True)
    output = workdir.resolve()
    require(root not in output.parents and output != root and not output.exists(), "PATH")
    try:
        for relative in SOURCE_FILES:
            path = root / relative
            require(path.is_file() and not path.is_symlink() and path.resolve(strict=True) == path, "SOURCE")
        compiler_name, compiler_hash = _compiler_tool()
        require(esbuild.is_file() and not esbuild.is_symlink(), "TOOLCHAIN")
        compiler = esbuild.read_bytes()
        require(hashlib.sha256(compiler).hexdigest() == compiler_hash, "TOOLCHAIN")
        output.mkdir(parents=True, exist_ok=False)
        # Execute the verified bytes, not the still-mutable installed tool path.
        compiler_path = output / compiler_name
        with compiler_path.open("xb") as stream:
            stream.write(compiler)
        if sys.platform == "linux":
            compiler_path.chmod(0o700)
        names = {"PATH", "PATHEXT", "SYSTEMROOT", "WINDIR", "SYSTEMDRIVE", "USERPROFILE", "HOME"}
        env = {key: value for key, value in os.environ.items() if key.upper() in names}
        env.update(TEMP=str(output), TMP=str(output))
        result = subprocess.run([str(compiler_path), str(root / ".sba/edge.mjs"), "--bundle", "--format=esm", "--platform=browser",
                                 "--external:cloudflare:sockets", "--log-level=error", "--outfile=" + str(output / "edge.mjs"),
                                 "--metafile=" + str(output / "inputs.json")],
                                cwd=root, env=env, stdin=subprocess.DEVNULL, capture_output=True, timeout=120, check=False)
        require(result.returncode == 0, "BUILD")
        inputs = json.loads((output / "inputs.json").read_text(encoding="utf-8"))
        require(type(inputs) is dict and type(inputs.get("inputs")) is dict and set(inputs["inputs"]) == SOURCE_FILES,
                "SOURCE")
        bundle = (output / "edge.mjs").read_bytes()
        require(0 < len(bundle) <= 3 * 1024 * 1024, "BUNDLE")
        bundle.decode("utf-8", errors="strict")
        return bundle
    except (OSError, UnicodeError, ValueError, subprocess.SubprocessError):
        raise WorkerError("EASYPROXY_SBA_WORKER_BUILD") from None


def publish_ech(owner: Coordinator, permit: Permit, settings: EchDeployment, *, api_token: str, ech_token: str, resume=None) -> dict:
    from pages_repair import PagesResume, WORKER_BUNDLE_SHA256, worker_bundle_digest
    state = owner.assert_owned(permit)
    require(permit.action in ("deploy", "update", "preview"), "OWNERSHIP")
    require(inventory_digest(settings.inventory) == owner.inventory_digest, "INVENTORY")
    workers = [row for row in settings.inventory if row["kind"] == "worker"]
    buckets = [row for row in settings.inventory if row["kind"] == "r2"]
    require(len(workers) == len(buckets) == 1 and workers[0]["id"] == workers[0]["name"] == settings.worker_name
            and buckets[0]["id"] == buckets[0]["name"] == owner.identity["bucket"], "IDENTITY")
    if resume is not None:
        require(type(resume) is PagesResume, "OWNERSHIP")
        resume.assert_permit(owner, permit)
    release = state["writer"]["candidate"]
    expected = metadata(owner, release, ech_token)
    api = WorkerAPI(owner.identity["accountId"], api_token)
    try:
        inspect_checkout(settings.root, owner.identity["repository"], release["sourceSha"])
        bundle = build_bundle(settings.root, settings.esbuild, settings.workdir)
        inspect_checkout(settings.root, owner.identity["repository"], release["sourceSha"])
        subdomain = api.account_subdomain()  # Never create/change the account-wide subdomain.
        existing = [row for row in api.list_workers() if row["id"] == settings.worker_name]
        if resume is not None:
            require(len(existing) == 1 and hashlib.sha256(bundle).hexdigest() == WORKER_BUNDLE_SHA256, "OWNERSHIP")
            _verify_settings(api.settings(settings.worker_name), metadata(owner, resume.parent_release, ech_token))
            require(worker_bundle_digest(api, settings.worker_name) == WORKER_BUNDLE_SHA256, "OWNERSHIP")
            api.script_subdomain(settings.worker_name)
        elif permit.action == "update":
            require(len(existing) == 1, "OWNERSHIP")
            _verify_settings(api.settings(settings.worker_name), metadata(owner, state["active"], ech_token))
        else:
            require(not existing, "UNOWNED_RESOURCE")  # No implicit import or overwrite on first deployment.
        owner.claim_stage(permit, "ech-publication")
        api.upload(settings.worker_name, expected, bundle)
        owner.assert_owned(permit)
        _verify_settings(api.settings(settings.worker_name), expected)
        if resume is None:
            api.enable_subdomain(settings.worker_name)
        owner.assert_owned(permit)
        api.script_subdomain(settings.worker_name)
        provider_origin = f"https://{settings.worker_name}.{subdomain}.workers.dev"
        _verify_public(provider_origin, release)
        origin = inventory_origin(settings.inventory, settings.worker_name, provider_origin)
        if origin != provider_origin:
            require(resume is None, "OWNERSHIP")
            bind_domain(owner, permit, settings.inventory, settings.worker_name, "worker", api_token)
            _verify_public(origin, release)
        owner.assert_owned(permit)
        return {"checks": {"ech-published": True, "ech-bindings-verified": True, "ech-version-verified": True},
                "worker": settings.worker_name, "origin": origin, "aggregatorBaseUrl": origin + "/aggregator",
                "bundleSha256": hashlib.sha256(bundle).hexdigest(), **release}
    finally:
        api.close()  # No unlock, rollback, retry, deletion or second publication on any failure.
