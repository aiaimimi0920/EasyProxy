"""SpringBok v3 的只读应用边界：严格 JSON、实例/版本/预览清单与动作秘密选择。"""
from __future__ import annotations

from dataclasses import dataclass
import json
import re

from coordination import SHA, VERSION
from sealed_box import secret_value
from repair_context import CONTEXT_FIELDS, PARENT_ERROR, PARENT_RELEASE, REPAIR_ID, GITHUB_CONTEXT, GITHUB_PARENT_RELEASE
from repair_context import PAGES_CONTEXT, PAGES_PARENT_RELEASE
from suite_config import ConfigurationError, SuiteConfig, parse_configuration


APPLICATION_ID = "easyproxy-cloud"
ACTIONS = {"deploy", "update", "verify", "preview", "destroy-preview", "repair"}
FIELDS = {"schemaVersion", "taskId", "action", "repository", "sourceSha", "applicationId", "applicationVersion",
          "environment", "configuration", "previous"}
SECRETS = {"EASYPROXY_CLOUDFLARE_API_TOKEN", "EASYPROXY_GITHUB_TOKEN", "EASYPROXY_R2_ACCESS_KEY_ID",
           "EASYPROXY_R2_SECRET_ACCESS_KEY", "EASYPROXY_MISUB_ADMIN_PASSWORD", "EASYPROXY_MISUB_COOKIE_SECRET",
           "EASYPROXY_MANIFEST_TOKEN", "EASYPROXY_ECH_TOKEN", "EASYPROXY_BACKUP_PASSPHRASE",
           "EASYPROXY_AGGREGATOR_SHARED_TOKEN", "EASYPROXY_AGGREGATOR_ISSUE91_SUB_URL_B64"}


class RequestError(RuntimeError):
    pass


def require(value):
    if not value:
        raise RequestError("EASYPROXY_SBA_REQUEST_REJECTED")


def exact(value, keys: set):
    require(type(value) is dict and set(value) == keys)


def matches(pattern, value) -> bool:
    return type(value) is str and re.fullmatch(pattern, value) is not None


def unique(pairs):
    value = {}
    for key, item in pairs:
        require(key not in value)
        value[key] = item
    return value


def release(value):
    exact(value, {"sourceSha", "applicationVersion"})
    require(matches(SHA, value["sourceSha"]) and matches(VERSION, value["applicationVersion"]))


def config_inventory(value, config: SuiteConfig):
    require(type(value) is list and len(value) == len(config.platform_inventory))
    for row in value:
        exact(row, {"key", "kind", "accountId", "remoteId", "name"})
        require(all(type(item) is str for item in row.values()))
    require(sorted(value, key=lambda row: row["key"]) == sorted(config.platform_inventory, key=lambda row: row["key"]))


def task(value):
    require(matches(r"dc-[a-f0-9]{32}", value))


def repair_context(value, config):
    context = value["context"]
    exact(context, CONTEXT_FIELDS)
    task(context["parentTaskId"])
    require(config.repository == "aiaimimi0920/easyproxy" and context["parentTaskId"] != value["taskId"])
    if context["repairId"] == GITHUB_CONTEXT["repairId"]:
        require(context == GITHUB_CONTEXT and value["previous"] == GITHUB_PARENT_RELEASE)
        return
    if context["repairId"] == PAGES_CONTEXT["repairId"]:
        require(context == PAGES_CONTEXT and value["previous"] == PAGES_PARENT_RELEASE)
        return
    require(value["previous"] == PARENT_RELEASE
            and context["repairId"] == REPAIR_ID and context["errorCode"] == PARENT_ERROR
            and context["parentTaskId"] != value["taskId"]
            and type(context["parentRunId"]) is int and context["parentRunId"] > 0
            and matches(r"[a-f0-9]{64}", context["requestDigest"])
            and matches(r"[a-f0-9]{64}", context["resultDigest"]))


def preview_context(value: dict, config: SuiteConfig):
    context = value["context"]
    if value["action"] == "preview":
        exact(context, {"source", "resources", "urls"})
        source = context["source"]
        exact(source, {"instanceId", "taskId", "sourceSha", "applicationVersion", "environment", "configuration",
                       "resources", "resultDigest"})
        task(source["instanceId"])
        task(source["taskId"])
        require(matches(r"[a-f0-9]{64}", source["resultDigest"])
                and {key: source[key] for key in ("sourceSha", "applicationVersion")} == value["previous"]
                and source["environment"] != config.environment)
        original = parse_configuration(source["configuration"], repository=config.repository, environment=source["environment"])
        require(original.account_id == config.account_id)
        config_inventory(source["resources"], original)
        config_inventory(context["resources"], config)
        original_ids = {(row["accountId"], row["kind"], row["remoteId"]) for row in original.platform_inventory}
        require(not any((row["accountId"], row["kind"], row["remoteId"]) in original_ids for row in config.platform_inventory))
        require(type(context["urls"]) is list and len(context["urls"]) == 2
                and all(type(url) is str for url in context["urls"])
                and set(context["urls"]) == {config.worker_origin, config.pages_origin})
    else:
        exact(context, {"instanceId", "previewTaskId", "resultDigest", "resources"})
        task(context["instanceId"])
        task(context["previewTaskId"])
        require(matches(r"[a-f0-9]{64}", context["resultDigest"]))
        config_inventory(context["resources"], config)


@dataclass(frozen=True)
class ApplicationRequest:
    task_id: str
    action: str
    source_sha: str
    application_version: str
    config: SuiteConfig
    previous: dict | None
    context: dict | None

    @property
    def candidate(self) -> dict:
        return {"sourceSha": self.source_sha, "applicationVersion": self.application_version}

    def credentials(self, environment: dict) -> dict:
        # Verify deliberately never reads placeholders or creates write/admin clients.
        if self.action == "verify":
            return {}
        values = {name: environment.get(name) for name in SECRETS}
        require(all(secret_value(value) for value in values.values()))
        return values


def parse_request(raw: bytes, *, application_version: str) -> ApplicationRequest:
    require(type(raw) is bytes and 0 < len(raw) <= 49152 and matches(VERSION, application_version))
    try:
        value = json.loads(raw.decode("utf-8"), object_pairs_hook=unique,
                           parse_constant=lambda _: require(False))
        require(type(value) is dict and type(value.get("action")) is str and value["action"] in ACTIONS)
        preview = value["action"] in ("preview", "destroy-preview")
        exact(value, FIELDS | ({"context"} if preview or value["action"] == "repair" else set()))
        require(type(value["schemaVersion"]) is int and value["schemaVersion"] == 3
                and matches(r"[a-z][a-z0-9-]{1,62}", value["taskId"])
                and value["applicationId"] == APPLICATION_ID and value["applicationVersion"] == application_version
                and matches(SHA, value["sourceSha"]))
        config = parse_configuration(value["configuration"], repository=value["repository"], environment=value["environment"])
        candidate = {"sourceSha": value["sourceSha"], "applicationVersion": value["applicationVersion"]}
        if value["action"] == "deploy":
            require(value["previous"] is None)
        else:
            release(value["previous"])
            if value["action"] in ("update", "preview", "repair"):
                require(candidate["sourceSha"] != value["previous"]["sourceSha"] and
                        tuple(map(int, application_version.split("."))) >
                        tuple(map(int, value["previous"]["applicationVersion"].split("."))))
            else:
                require(candidate == value["previous"])
        if preview:
            preview_context(value, config)
        if value["action"] == "repair":
            repair_context(value, config)
        return ApplicationRequest(value["taskId"], value["action"], value["sourceSha"], application_version, config,
                                  value["previous"], value.get("context"))
    except (ValueError, TypeError, UnicodeError, RecursionError, KeyError, ConfigurationError):
        raise RequestError("EASYPROXY_SBA_REQUEST_REJECTED") from None
