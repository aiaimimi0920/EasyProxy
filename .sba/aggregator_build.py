"""批准 Aggregator 的隔离本地生成；不给 upstream Cloudflare、R2 或 GitHub 写凭据。"""
from __future__ import annotations

import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys

from aggregator_publish import ARTIFACTS, RAW, _validate
from aggregator_runtime import prepare_runtime, verify_runtime
from artifact_store import ArtifactError, MAX_BYTES, require
from diagnostics import phase
from sources import inspect_checkout, inspect_gitlink_checkout


PRIVATE = {"crawledproxies": "internal/crawledproxies.txt"}
FILES = {**ARTIFACTS, **PRIVATE}
SEED_SECRETS = {"EASYPROXY_AGGREGATOR_SHARED_TOKEN", "EASYPROXY_AGGREGATOR_ISSUE91_SUB_URL_B64"}
DISABLED_SEED = "sba-disabled-source-v1"


def configured_seed_secrets(values: dict) -> dict:
    """可选旧私有种子允许显式停用；不把占位字符串当成真实 URL/token。"""
    require(type(values) is dict and set(values) <= SEED_SECRETS
            and all(isinstance(value, str) and 0 < len(value) <= 4096 and all(33 <= ord(char) <= 126 for char in value)
                    for value in values.values()), "SECRETS")
    return {name: value for name, value in values.items() if value != DISABLED_SEED}


def _environment(output: Path) -> dict:
    names = {"PATH", "PATHEXT", "SYSTEMROOT", "WINDIR", "COMSPEC", "SYSTEMDRIVE"}
    result = {key: value for key, value in os.environ.items() if key.upper() in names}
    home = output / "home"
    home.mkdir()
    result.update(HOME=str(home), USERPROFILE=str(home), APPDATA=str(home), LOCALAPPDATA=str(home),
                  TEMP=str(output), TMP=str(output), TMPDIR=str(output), CI="true", PYTHONDONTWRITEBYTECODE="1",
                  PYTHONNOUSERSITE="1", PYTHONUTF8="1", PYTHONIOENCODING="utf-8")
    return result


def _copy(source: Path, target: Path) -> None:
    target.mkdir(exist_ok=False)
    for child in source.iterdir():
        if child.name == ".git":
            continue
        require(not child.is_symlink() and child.resolve(strict=True) == child, "SOURCE")
        if child.is_dir():
            _copy(child, target / child.name)
        else:
            require(child.is_file(), "SOURCE")
            shutil.copyfile(child, target / child.name)


def local_config(template: dict, output: Path) -> dict:
    """保留批准的公开采集频道；禁任意脚本、仓库插件、注册和远端 persistence。"""
    require(output.is_absolute() and output.resolve() == output and output.is_dir(), "OUTPUT")
    result = json.loads(json.dumps(template))
    require(type(result) is dict and type(result.get("storage")) is dict
            and result["storage"].get("engine") == "r2"
            and set(result["storage"].get("items", {})) == set(FILES), "CONFIG")
    for name, key in FILES.items():
        require(result["storage"]["items"][name].get("key") == key, "CONFIG")
    require(type(result.get("domains")) is list and result["domains"], "CONFIG")
    for entry in result["domains"]:
        require(type(entry) is dict and entry.get("domain", "") == "" and type(entry.get("sub")) is list
                and all(isinstance(url, str) and url.startswith("https://") for url in entry["sub"]), "CONFIG")
    crawl = result.get("crawl")
    require(type(crawl) is dict and all(crawl.get(key) in ([], None) for key in ("scripts", "repositories", "pages")), "CONFIG")
    groups = result.get("groups")
    require(type(groups) is dict and bool(groups) and all(type(group) is dict
            and type(group.get("regularize")) is dict and group["regularize"].get("enable") is False
            for group in groups.values()), "CONFIG")
    # No caller-supplied local/file paths survive the storage replacement.
    result["storage"] = {"engine": "local", "items": {
        name: {"folder_id": str(output), "file_id": name + ".artifact"} for name in FILES}}
    result["update"] = {"enable": False}
    return result


def _run(args: list[str], cwd: Path, env: dict, label: str, timeout: int) -> None:
    try:
        with (Path(env["TEMP"]) / (label + ".private.log")).open("xb") as log:
            completed = subprocess.run(args, cwd=cwd, env=env, stdin=subprocess.DEVNULL,
                                       stdout=log, stderr=subprocess.STDOUT, timeout=timeout, check=False)
        require(completed.returncode == 0, "BUILD_" + label.upper())
    except (OSError, subprocess.SubprocessError):
        raise ArtifactError("EASYPROXY_SBA_ARTIFACT_BUILD_" + label.upper()) from None


def build_aggregator(root: Path, repository: str, source_sha: str, output: Path, *, seed_secrets: dict) -> dict[str, bytes]:
    with phase("GENERATION_SOURCE"):
        root = root.resolve(strict=True)
        rows = inspect_checkout(root, repository, source_sha)
        row = next(row for row in rows if row["path"] == "upstreams/aggregator")
        inspect_gitlink_checkout(root, row)
    with phase("GENERATION_CONFIG"):
        require(output.is_absolute() and not str(output).startswith(("\\\\", "//")), "OUTPUT")
        output = output.resolve()
        require(not output.exists() and root not in output.parents and output != root, "OUTPUT")
        seed_secrets = configured_seed_secrets(seed_secrets)
        output.mkdir(mode=0o700, parents=True, exist_ok=False)
        env = _environment(output)
    with phase("GENERATION_RUNTIME"):
        workspace = output / "aggregator"
        _copy(root / row["path"], workspace)
        runtime = prepare_runtime(workspace, row["sourceSha"])
        (output / "runtime-provenance.json").write_text(json.dumps(runtime, indent=2), encoding="utf-8")
    with phase("GENERATION_CONFIG"):
        # 这里只物化占位符；环境不含云写凭据，诊断也不保留私有异常正文。
        materialized = output / "materialized.private.json"
        _run([sys.executable, str(root / "scripts/materialize-aggregator-config.py"),
              "--template", str(root / "deploy/upstreams/aggregator/config/config.actions.r2.json"),
              "--output", str(materialized)], output, {**env, **seed_secrets}, "config", 30)
        artifacts = output / "artifacts"
        artifacts.mkdir()
        config = local_config(json.loads(materialized.read_text(encoding="utf-8")), artifacts)
        config_path, empty_env = output / "runtime.private.json", output / "empty.env"
        config_path.write_text(json.dumps(config, ensure_ascii=False), encoding="utf-8")
        empty_env.write_text("", encoding="utf-8")
    with phase("GENERATION_COLLECT"):
        _run([sys.executable, "-u", "subscribe/process.py", "--overwrite", "--skip-remark", "--invisible",
              "--environment", str(empty_env), "--server", str(config_path)], workspace, env, "generation", 2400)
    with phase("GENERATION_READBACK"):
        verify_runtime(workspace, runtime)
        inspect_gitlink_checkout(root, row)
        inspect_checkout(root, repository, source_sha)
    with phase("GENERATION_OUTPUTS"):
        bodies = {}
        for name in ("public-clash", "public-singbox", "public-mixed", "crawledsubs"):
            path = artifacts / (name + ".artifact")
            require(path.is_file() and not path.is_symlink() and path.resolve(strict=True) == path, "OUTPUT")
            with path.open("rb") as stream:
                bodies[ARTIFACTS[name]] = stream.read(MAX_BYTES + 1)
    with phase("GENERATION_CONVERSION"):
        # 复用纯转换函数，不调用 R2 publish/main 入口。
        spec = importlib.util.spec_from_file_location("sba_v2ray_converter", root / "scripts/publish-aggregator-v2ray.py")
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        try:
            bodies["subs/v2ray.txt"], _ = module.build_payload(bodies["subs/mixed.txt"])
        except (ValueError, RuntimeError):
            raise ArtifactError("EASYPROXY_SBA_ARTIFACT_V2RAY") from None
    with phase("GENERATION_VALIDATION"):
        _validate(bodies, RAW)
    return bodies
