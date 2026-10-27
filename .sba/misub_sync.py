"""同一套件许可内单次保存审计节点和 ECH 入口；不重试、不调用 Cron、不自动恢复。"""
from __future__ import annotations

import copy
import importlib.util
from pathlib import Path
import re

import requests

from aggregator_publish import Candidate, PublicArtifacts, validated_audit
from cloudflare_http import decode_object, secure_session
from coordination import Coordinator, Permit


PROFILE = "aggregator-global"
RUNTIME_PREFIX = "proxy_runtime_node"
ECH_PREFIX = "conn_ech_workers_pref"
MAX_BYTES = 16 * 1024 * 1024


class MiSubError(RuntimeError):
    """Only safe error codes leave the adapter."""


def require(condition: bool, code: str) -> None:
    if not condition:
        raise MiSubError("EASYPROXY_SBA_MISUB_" + code)


def credential(value) -> bool:
    return (isinstance(value, str) and 0 < len(value.encode("utf-8")) <= 512
            and value != "verification-not-applicable" and all(ord(char) >= 32 and ord(char) != 127 for char in value))


class MiSubAPI:
    def __init__(self, project: str, admin_password: str, manifest_token: str):
        require(isinstance(project, str) and re.fullmatch(r"[a-z][a-z0-9-]{1,57}[a-z0-9]", project) is not None, "IDENTITY")
        require(credential(admin_password) and credential(manifest_token), "CREDENTIALS")
        # Management secrets never follow mutable custom DNS. The upstream CSRF
        # middleware accepts the request's own origin, independently of public URLs.
        self.origin = "https://" + project + ".pages.dev"
        self.session = secure_session()
        self.manifest_token = manifest_token
        require(self.request("POST", "/api/login", {"password": admin_password}).get("success") is True, "LOGIN_UNKNOWN")

    def request(self, method: str, path: str, body=None) -> dict:
        require((method, path) in {("POST", "/api/login"), ("POST", "/api/backup/export"),
                                  ("POST", "/api/sba/managed-sync"), ("GET", "/api/settings"),
                                  ("GET", "/api/manifest/" + PROFILE)}, "PATH")
        headers = {"Origin": self.origin, "Cache-Control": "no-cache"}
        if path.startswith("/api/manifest/"):
            headers["Authorization"] = "Bearer " + self.manifest_token
        try:
            with self.session.request(method, self.origin + path, json=body, headers=headers,
                                      timeout=(10, 60), allow_redirects=False, stream=True) as response:
                require(response.status_code == 200, "REQUEST_UNKNOWN")
                raw = bytearray()
                for chunk in response.iter_content(65536):
                    raw.extend(chunk)
                    require(len(raw) <= MAX_BYTES, "RESPONSE_UNKNOWN")
                value = decode_object(bytes(raw), require)
                require(value.get("success") is not False, "REQUEST_UNKNOWN")
                return value
        except (requests.RequestException, OSError):
            raise MiSubError("EASYPROXY_SBA_MISUB_REQUEST_UNKNOWN") from None

    def snapshot(self) -> dict:
        # The approved upstream export handler only reads business collections.
        # /api/data is deliberately forbidden: it may migrate IDs/tokens and run scheduled tasks.
        response = self.request("POST", "/api/backup/export", {"scope": "dataAndSettings"})
        value = response.get("exportData")
        require(response.get("success") is True and type(value) is dict and value.get("version") == 1
                and value.get("app") == "MiSub" and value.get("type") == "misub-backup"
                and value.get("scope") == "dataAndSettings" and type(value.get("source")) is dict
                and value["source"].get("storageType") == "d1" and type(value.get("data")) is dict, "SNAPSHOT")
        data = value["data"]
        require(set(data) == {"subscriptions", "profiles", "settings", "ruleTemplates"}
                and type(data["settings"]) is dict and type(data["ruleTemplates"]) is list, "SNAPSHOT")
        for key in ("subscriptions", "profiles"):
            rows = data[key]
            require(type(rows) is list and all(type(row) is dict and isinstance(row.get("id"), str) and row["id"] for row in rows)
                    and len({row["id"] for row in rows}) == len(rows), "SNAPSHOT")
        return data

    def close(self):
        self.session.close()


def _module(root: Path, name: str):
    path = root / "scripts" / (name + ".py")
    require(path.is_file() and not path.is_symlink() and path.resolve(strict=True) == path, "SOURCE")
    spec = importlib.util.spec_from_file_location("sba_" + name.replace("-", "_"), path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def _managed(row: dict) -> bool:
    return re.fullmatch(r"(?:proxy_runtime_node|conn_ech_workers_pref)_\d+", row["id"]) is not None


def _diff(before: list[dict], after: list[dict]) -> dict:
    old, new = {row["id"]: row for row in before}, {row["id"]: row for row in after}
    return {"added": [row for row in after if row["id"] not in old],
            "updated": [row for row in after if row["id"] in old and row != old[row["id"]]],
            "removed": [row["id"] for row in before if row["id"] not in new]}


def desired_state(root: Path, baseline: dict, uris: list[str], ech_origin: str, ech_token: str, audit_id: str) -> dict:
    runtime, ech = (_module(root, name) for name in ("sync-misub-runtime-sources", "sync-misub-ech-profile"))
    sources, profiles = baseline["subscriptions"], baseline["profiles"]
    retained = [copy.deepcopy(row) for row in sources if not _managed(row)]
    connector_sources = [row for row in sources if row["id"].startswith(ECH_PREFIX + "_")]
    _, existing_ips = ech.normalize_existing_sources(connector_sources, ECH_PREFIX)
    connectors = ech.build_sources(ech_origin, ech_token, existing_ips, "http", ECH_PREFIX,
                                   "ECH Worker Preferred", "ECH Connectors", "Preferred Cloudflare entry IP")
    nodes = [runtime.build_runtime_source(uri, f"{RUNTIME_PREFIX}_{index}", "Managed Runtime Proxies",
                                         "Managed by the approved EasyProxy source audit.", audit_id)
             for index, uri in enumerate(uris, 1)]
    index, old = runtime.find_profile(profiles, [PROFILE])
    require(old is None or old.get("enabled", True) is True, "PROFILE_DISABLED")
    profile = copy.deepcopy(old or {})
    # Preserve user fields, public visibility, expiry and non-managed connectors.
    other_connectors = runtime.resolve_connector_node_ids(settings=baseline["settings"], existing_profile=old, sources=retained)
    profile.update(id=(old or {}).get("id", PROFILE.replace("-", "_")), customId=PROFILE,
                   name=(old or {}).get("name", "Aggregator Global"), enabled=True, subscriptions=[],
                   manualNodes=[row["id"] for row in nodes + connectors] + other_connectors,
                   isPublic=(old or {}).get("isPublic", False), expiresAt=(old or {}).get("expiresAt", ""))
    profile["options"] = {**(profile.get("options") or {}), "managed_by": "easyproxy_runtime_profile",
                          "sync_managed": True, "source_role": "runtime_effective_profile"}
    result = copy.deepcopy(baseline)
    result["subscriptions"] = retained + nodes + connectors
    if index < 0:
        result["profiles"].append(profile)
    else:
        result["profiles"][index] = profile
    require(len({row["id"] for row in result["subscriptions"]}) == len(result["subscriptions"]), "IDENTITY")
    return result


def _preserved(baseline: dict, expected: dict, actual: dict):
    require(expected["settings"] == actual["settings"] and expected["ruleTemplates"] == actual["ruleTemplates"], "DATA_DRIFT_UNKNOWN")
    for key in ("subscriptions", "profiles"):
        wanted, observed = {row["id"]: row for row in expected[key]}, {row["id"]: row for row in actual[key]}
        require(set(wanted) == set(observed), "DATA_DRIFT_UNKNOWN")
        for identity, row in wanted.items():
            # Upstream assigns sortIndex and normalizes source/profile defaults.
            # Existing unmanaged rows must otherwise remain byte-for-byte equivalent.
            seen = observed[identity]
            require(all(seen.get(field) == value for field, value in row.items() if field != "sortIndex"), "DATA_DRIFT_UNKNOWN")
        for row in baseline[key]:
            if key == "subscriptions" and not _managed(row) or key == "profiles" and row == wanted.get(row["id"]):
                require(observed.get(row["id"]) == row, "DATA_DRIFT_UNKNOWN")


def sync_misub(owner: Coordinator, permit: Permit, candidate: Candidate, audit, public: PublicArtifacts,
                root: Path, inventory: list[dict], *, admin_password: str, manifest_token: str, ech_token: str) -> dict:
    from coordination import inventory_digest
    require(permit.action in ("deploy", "update", "preview", "refresh") and inventory_digest(inventory) == owner.inventory_digest, "IDENTITY")
    uris, _ = validated_audit(owner, permit, candidate, audit, root, public)
    require(credential(ech_token), "CREDENTIALS")
    projects = [row for row in inventory if row["kind"] == "pages"]
    workers = [row for row in inventory if row["kind"] == "worker"]
    require(len(projects) == len(workers) == 1 and projects[0]["id"] == projects[0]["name"]
            and workers[0]["id"] == workers[0]["name"], "IDENTITY")
    ech_origin = public.origin.removesuffix("/aggregator")
    domains = [row for row in inventory if row["kind"] == "domain" and row["name"] == workers[0]["name"]]
    require((len(domains) == 1 and ech_origin == domains[0]["id"]) if domains else
            ech_origin.startswith("https://" + workers[0]["name"] + "."), "IDENTITY")
    api = MiSubAPI(projects[0]["name"], admin_password, manifest_token)
    try:
        baseline = api.snapshot()
        settings = api.request("GET", "/api/settings")
        # Saving existing subscriptions can send Telegram notifications. This
        # stage must remain bounded and side-effect isolated, including previews.
        require(not (settings.get("BotToken") and settings.get("ChatID")), "NOTIFICATIONS_ENABLED")
        sync = settings.get("aggregatorSync") or {}
        require(type(sync) is dict and (not sync.get("enabled") or sync.get("runOnCron") is False), "CRON_WRITER_ENABLED")
        desired = desired_state(root, baseline, uris, ech_origin, ech_token, "sba-" + permit.operation_id)
        require(api.snapshot() == baseline, "CONCURRENT_EDIT")
        owner.assert_owned(permit)
        owner.claim_stage(permit, "misub-managed-sync")
        changes = {"diff": {"subscriptions": _diff(baseline["subscriptions"], desired["subscriptions"]),
                            "profiles": _diff(baseline["profiles"], desired["profiles"])}}
        profile_diff = changes["diff"]["profiles"]
        if not profile_diff["added"] and not profile_diff["updated"]:
            profile_diff["updated"] = [next(row for row in desired["profiles"] if row.get("customId") == PROFILE)]
        changes.update(operationId=permit.operation_id, sourceSha=owner.assert_owned(permit)["writer"]["candidate"]["sourceSha"],
                       before={key: baseline[key] for key in ("subscriptions", "profiles")})
        saved = api.request("POST", "/api/sba/managed-sync", changes)
        require(saved.get("success") is True, "SAVE_UNKNOWN")
        _preserved(baseline, desired, api.snapshot())
        value = api.request("GET", "/api/manifest/" + PROFILE)
        require(value.get("success") is True and type(value.get("profile")) is dict
                and value["profile"].get("customId") == PROFILE and type(value.get("sources")) is list, "MANIFEST_UNKNOWN")
        sources = value["sources"]
        expected_ids = next(row for row in desired["profiles"] if row.get("customId") == PROFILE)["manualNodes"]
        expected = {row["id"]: row for row in desired["subscriptions"] if row["id"] in expected_ids and row.get("enabled", True)}
        require(len(sources) == len(expected) and {row.get("id") for row in sources} == set(expected), "MANIFEST_UNKNOWN")
        for row in sources:
            target = expected[row["id"]]
            require(row.get("kind") == target.get("kind") and row.get("input") == target.get("input"), "MANIFEST_UNKNOWN")
            if target.get("kind") == "connector":
                options = row.get("options") or {}
                config = row.get("connector_config") or options.get("connector_config") or {}
                require(config == target.get("connector_config", target.get("options", {}).get("connector_config")), "MANIFEST_UNKNOWN")
        owner.assert_owned(permit)
        return {"checks": {"misub-refreshed": True}, "profileId": PROFILE, "runtimeNodes": len(uris),
                "connectorNodes": sum(row.get("kind") == "connector" for row in sources)}
    finally:
        api.close()  # No rollback, Cron invocation, unlock or second save after UNKNOWN.
