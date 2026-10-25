"""仓库级公开调度索引；它不包含版本或秘密，不是实例来源权威。"""
from __future__ import annotations

import json
import re


REGISTRY_VARIABLE = "EASYPROXY_SBA_REFRESH_REGISTRY"


class RegistryError(RuntimeError):
    pass


def require(value):
    if not value:
        raise RegistryError("EASYPROXY_SBA_REFRESH_REGISTRY_REJECTED")


def unique(pairs):
    result = {}
    for key, value in pairs:
        require(key not in result)
        result[key] = value
    return result


def parse_registry(raw: str | None, repository: str) -> dict:
    require(type(repository) is str and re.fullmatch(
        r"[a-z0-9][a-z0-9_.-]{0,99}/[a-z0-9][a-z0-9_.-]{0,99}", repository) is not None)
    if raw is None:
        return {"schemaVersion": 1, "repository": repository, "environments": []}
    require(type(raw) is str and 0 < len(raw.encode("utf-8")) <= 16384)
    try:
        value = json.loads(raw, object_pairs_hook=unique)
        require(type(value) is dict and set(value) == {"schemaVersion", "repository", "environments"}
                and type(value["schemaVersion"]) is int and value["schemaVersion"] == 1
                and value["repository"] == repository and type(value["environments"]) is list
                and len(value["environments"]) <= 32
                and all(type(name) is str and re.fullmatch(r"[a-z][a-z0-9-]{1,62}", name) is not None
                        for name in value["environments"])
                and sorted(set(value["environments"])) == value["environments"])
        return value
    except (ValueError, UnicodeError, RecursionError):
        raise RegistryError("EASYPROXY_SBA_REFRESH_REGISTRY_REJECTED") from None


def include_environment(value: dict, environment: str) -> str:
    updated = {**value, "environments": sorted(set(value["environments"]) | {environment})}
    raw = json.dumps(updated, sort_keys=True, separators=(",", ":"))
    parse_registry(raw, value["repository"])
    return raw
