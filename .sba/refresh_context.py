"""周期任务的只读边界；生产 marker 校验先于凭据读取和网络。"""
from __future__ import annotations

import json
from pathlib import Path

from refresh_registry import unique
from sealed_box import secret_value
from suite_config import parse_configuration


OWNER_VARIABLE = "EASYPROXY_SBA_OWNER"
CONFIG_VARIABLE = "EASYPROXY_SBA_CONFIG"
REFRESH_SECRETS = {"EASYPROXY_R2_ACCESS_KEY_ID", "EASYPROXY_R2_SECRET_ACCESS_KEY",
                   "EASYPROXY_MISUB_ADMIN_PASSWORD", "EASYPROXY_MANIFEST_TOKEN", "EASYPROXY_ECH_TOKEN",
                   "EASYPROXY_AGGREGATOR_SHARED_TOKEN", "EASYPROXY_AGGREGATOR_ISSUE91_SUB_URL_B64"}


class RefreshError(RuntimeError):
    pass


def require(value):
    if not value:
        raise RefreshError("EASYPROXY_SBA_REFRESH_REJECTED")


def object_json(raw: str) -> dict:
    require(type(raw) is str and 0 < len(raw.encode("utf-8")) <= 49152)
    try:
        value = json.loads(raw, object_pairs_hook=unique)
        require(type(value) is dict)
        return value
    except (ValueError, UnicodeError, RecursionError):
        raise RefreshError("EASYPROXY_SBA_REFRESH_REJECTED") from None


def context(environment):
    marker = object_json(environment.get(OWNER_VARIABLE))
    require(set(marker) == {"schemaVersion", "repositoryId", "environmentId", "identity", "inventoryDigest"}
            and type(marker["schemaVersion"]) is int and marker["schemaVersion"] == 1
            and all(type(marker[key]) is int and marker[key] > 0 for key in ("repositoryId", "environmentId")))
    identity = marker["identity"]
    # Repo/org secrets can be inherited by any environment. Never read them for
    # a preview (or an unmarked environment), regardless of where they came from.
    require(type(identity) is dict and identity.get("purpose") == "production")
    repository, target = environment.get("GITHUB_REPOSITORY"), environment.get("EASYPROXY_SBA_ENVIRONMENT")
    require(type(repository) is str and type(target) is str)
    config = parse_configuration(object_json(environment.get(CONFIG_VARIABLE)), repository=repository, environment=target)
    require(identity == config.identity("production") and marker["inventoryDigest"] == config.inventory_digest)
    return config


def credentials(environment) -> dict:
    values = {name: environment.get(name) for name in REFRESH_SECRETS}
    require(all(secret_value(value) for value in values.values()))
    return values


def manifest_release(root: Path, source_sha: str) -> dict:
    path = root / ".sba/manifest.json"
    require(path.is_file() and not path.is_symlink() and path.resolve(strict=True) == path
            and 0 < path.stat().st_size <= 65536)
    value = object_json(path.read_text(encoding="utf-8"))
    require(value.get("schemaVersion") == 3 and value.get("id") == "easyproxy-cloud")
    from coordination import _release
    release = {"sourceSha": source_sha, "applicationVersion": value.get("version")}
    require(_release(release))
    return release
