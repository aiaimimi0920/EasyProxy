#!/usr/bin/env python3

from __future__ import annotations

import argparse
import base64
import copy
import hashlib
import json
import os
import re
import secrets
import subprocess
import time
from pathlib import Path
from typing import Any
from urllib.parse import urlparse

import requests
import yaml
from nacl import encoding, public


REPO_ROOT = Path(__file__).resolve().parent.parent
DEFAULT_CONFIG_PATH = REPO_ROOT / "config.yaml"
DEFAULT_TEMPLATE_PATH = REPO_ROOT / "config.example.yaml"
PROXY_AVAILABILITY_POLICY_PATH = REPO_ROOT / "shared" / "proxy-availability" / "policy.json"
DEFAULT_EASYEMAIL_CONFIG = Path(r"C:\Users\Public\nas_home\AI\GameEditor\EasyEmail\config.yaml")
DEFAULT_LEGACY_AGGREGATOR_CLASH_CONFIG = Path(
    r"C:\Users\Public\nas_home\AI\GameEditor\ProxyService\repos\aggregator\clash\config.yaml"
)
DEFAULT_OWNER_KEY_DIR = Path.home() / ".easyproxy"
DEFAULT_OWNER_PUBLIC_KEY_PATH = DEFAULT_OWNER_KEY_DIR / "easyproxy_import_code_owner_public.txt"
DEFAULT_OWNER_PRIVATE_KEY_PATH = DEFAULT_OWNER_KEY_DIR / "easyproxy_import_code_owner_private.txt"
DEFAULT_OWNER_BUNDLE_PATH = DEFAULT_OWNER_KEY_DIR / "easyproxy_import_code_owner_keypair.json"

REPO = "aiaimimi0920/EasyProxy"

CF_PERMISSION_GROUPS = {
    "pages_write": "8d28297797f24fb8a0c332fe0866ec89",
    "d1_write": "09b2857d1c31407795e75e3fed8617a1",
    "workers_scripts_write": "e086da7e2179491d91ee5f35b3ca210a",
    "r2_bucket_read": "6a018a9f2fc74eb6b293b0c548f38b39",
    "r2_bucket_write": "2efd5506f9c8494dacb1fa10a3e7d5b6",
}


def load_yaml(path: Path) -> dict[str, Any]:
    if not path.exists():
        return {}
    return yaml.safe_load(path.read_text(encoding="utf-8")) or {}


def deep_merge(base: Any, overlay: Any) -> Any:
    if isinstance(base, dict) and isinstance(overlay, dict):
        merged = copy.deepcopy(base)
        for key, value in overlay.items():
            if key in merged:
                merged[key] = deep_merge(merged[key], value)
            else:
                merged[key] = copy.deepcopy(value)
        return merged
    return copy.deepcopy(overlay)


def save_yaml(path: Path, payload: dict[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(yaml.safe_dump(payload, allow_unicode=True, sort_keys=False), encoding="utf-8")


def load_proxy_availability_probe_targets() -> list[str]:
    if not PROXY_AVAILABILITY_POLICY_PATH.exists():
        return []
    payload = json.loads(PROXY_AVAILABILITY_POLICY_PATH.read_text(encoding="utf-8"))
    targets = [
        str(item).strip()
        for item in (payload.get("management_probe_targets") or [])
        if str(item).strip()
    ]
    if targets:
        return targets
    return [
        str(item.get("url") or "").strip()
        for item in (payload.get("http_probe_targets") or [])
        if str(item.get("url") or "").strip()
    ]


def ensure_dict(parent: dict[str, Any], key: str) -> dict[str, Any]:
    value = parent.get(key)
    if not isinstance(value, dict):
        value = {}
        parent[key] = value
    return value


def get_nested(config: dict[str, Any], *keys: str, default: Any = "") -> Any:
    current: Any = config
    for key in keys:
        if not isinstance(current, dict):
            return default
        current = current.get(key)
        if current is None:
            return default
    return current


def is_placeholder(value: str) -> bool:
    text = str(value or "").strip()
    if not text:
        return True
    placeholder_markers = (
        "change_me",
        "example.com",
        "your-github-owner",
        "__KEY_PLACEHOLDER__",
        "__TOKEN_PLACEHOLDER__",
        "your-profile",
    )
    lowered = text.lower()
    return any(marker in lowered for marker in placeholder_markers)


def read_legacy_aggregator_shared_token(path: Path) -> str:
    if not path.exists():
        return ""
    text = path.read_text(encoding="utf-8")
    shared_match = re.search(r"https://raoku-pm\.hf\.space/api/v1/subscribe\?token=([^&\s]+)", text)
    return shared_match.group(1).strip() if shared_match else ""


def resolve_optional_secret(
    config: dict[str, Any],
    path: tuple[str, ...],
    env_name: str,
    fallback_value: str = "",
) -> str:
    configured = str(get_nested(config, *path, default="")).strip()
    if configured and not is_placeholder(configured):
        return configured
    env_value = os.environ.get(env_name, "").strip()
    if env_value and not is_placeholder(env_value):
        return env_value
    if fallback_value and not is_placeholder(fallback_value):
        return fallback_value
    return ""


def get_or_generate(config: dict[str, Any], path: tuple[str, ...], factory) -> str:
    current = get_nested(config, *path, default="")
    if isinstance(current, str) and not is_placeholder(current):
        return current
    return factory()


def set_nested(config: dict[str, Any], path: tuple[str, ...], value: Any) -> None:
    current = config
    for key in path[:-1]:
        current = ensure_dict(current, key)
    current[path[-1]] = value


def load_cloudflare_auth(config: dict[str, Any], fallback_path: Path) -> tuple[str, str]:
    auth_email = str(get_nested(config, "cloudflare", "authEmail", default="")).strip()
    global_api_key = str(get_nested(config, "cloudflare", "globalApiKey", default="")).strip()
    if auth_email and global_api_key:
        return auth_email, global_api_key

    if fallback_path.exists():
        fallback = load_yaml(fallback_path)
        auth_email = str(
            get_nested(fallback, "cloudflareMail", "routing", "cloudflareGlobalAuth", "authEmail", default="")
        ).strip()
        global_api_key = str(
            get_nested(fallback, "cloudflareMail", "routing", "cloudflareGlobalAuth", "globalApiKey", default="")
        ).strip()
        if auth_email and global_api_key:
            return auth_email, global_api_key

    auth_email = os.environ.get("CLOUDFLARE_AUTH_EMAIL", "").strip()
    global_api_key = os.environ.get("CLOUDFLARE_GLOBAL_API_KEY", "").strip()
    if auth_email and global_api_key:
        return auth_email, global_api_key

    raise SystemExit("Unable to resolve Cloudflare authEmail/globalApiKey from config, fallback config, or environment.")


def cf_request(session: requests.Session, method: str, url: str, **kwargs) -> dict[str, Any]:
    response = session.request(method, url, timeout=30, **kwargs)
    response.raise_for_status()
    payload = response.json()
    if not payload.get("success", False):
        raise RuntimeError(f"Cloudflare API failure for {url}: {payload}")
    return payload


def ensure_bucket(session: requests.Session, account_id: str, bucket: str) -> None:
    buckets = cf_request(session, "GET", f"https://api.cloudflare.com/client/v4/accounts/{account_id}/r2/buckets")["result"][
        "buckets"
    ]
    if any(item.get("name") == bucket for item in buckets):
        return
    cf_request(
        session,
        "POST",
        f"https://api.cloudflare.com/client/v4/accounts/{account_id}/r2/buckets",
        json={"name": bucket},
    )


def list_account_tokens(session: requests.Session, account_id: str) -> list[dict[str, Any]]:
    return cf_request(session, "GET", f"https://api.cloudflare.com/client/v4/accounts/{account_id}/tokens").get("result") or []


def delete_account_token(session: requests.Session, account_id: str, token_id: str) -> None:
    cf_request(session, "DELETE", f"https://api.cloudflare.com/client/v4/accounts/{account_id}/tokens/{token_id}")


def create_account_token(session: requests.Session, account_id: str, name: str, policies: list[dict[str, Any]]) -> dict[str, Any]:
    for token in list_account_tokens(session, account_id):
        if token.get("name") == name:
            delete_account_token(session, account_id, token["id"])
    return cf_request(
        session,
        "POST",
        f"https://api.cloudflare.com/client/v4/accounts/{account_id}/tokens",
        json={"name": name, "policies": policies},
    )["result"]


def bucket_resource(account_id: str, bucket: str) -> str:
    return f"com.cloudflare.edge.r2.bucket.{account_id}_default_{bucket}"


def to_r2_credentials(token_result: dict[str, Any]) -> tuple[str, str]:
    token_value = token_result["value"]
    access_key_id = token_result["id"]
    secret_access_key = hashlib.sha256(token_value.encode("utf-8")).hexdigest()
    return access_key_id, secret_access_key


def resolve_account_id(session: requests.Session, config: dict[str, Any]) -> str:
    configured = str(get_nested(config, "cloudflare", "accountId", default="")).strip()
    if configured:
        return configured
    payload = cf_request(session, "GET", "https://api.cloudflare.com/client/v4/accounts")
    accounts = payload.get("result") or []
    if not accounts:
        raise RuntimeError("No Cloudflare account was returned.")
    return str(accounts[0]["id"]).strip()


def fetch_workers_subdomain(session: requests.Session, account_id: str) -> str:
    payload = cf_request(session, "GET", f"https://api.cloudflare.com/client/v4/accounts/{account_id}/workers/subdomain")
    return str(payload["result"].get("subdomain") or "").strip()


def infer_workers_subdomain(config: dict[str, Any]) -> str:
    candidate = str(get_nested(config, "echWorkersCloudflare", "publicUrl", default="")).strip()
    if not candidate:
        return ""
    host = urlparse(candidate).hostname or ""
    if not host:
        return ""
    parts = host.split(".")
    if len(parts) >= 3 and parts[-2:] == ["aiaimimi", "com"]:
        return parts[0]
    return parts[0] if parts else ""


def ensure_owner_keypair(public_key_path: Path, private_key_path: Path, bundle_path: Path) -> None:
    if public_key_path.exists() and private_key_path.exists():
        return
    public_key_path.parent.mkdir(parents=True, exist_ok=True)
    subprocess.run(
        [
            "python",
            str(REPO_ROOT / "scripts" / "easyproxy-import-code.py"),
            "generate-keypair",
            "--public-key-output",
            str(public_key_path),
            "--private-key-output",
            str(private_key_path),
            "--bundle-output",
            str(bundle_path),
        ],
        check=True,
    )


def github_headers(token: str) -> dict[str, str]:
    return {
        "Authorization": f"Bearer {token}",
        "Accept": "application/vnd.github+json",
        "X-GitHub-Api-Version": "2022-11-28",
    }


def github_request(method: str, url: str, *, headers: dict[str, str], json_payload: dict[str, Any] | None = None) -> requests.Response:
    last_error: Exception | None = None
    for attempt in range(1, 6):
        try:
            response = requests.request(method, url, headers=headers, json=json_payload, timeout=30)
            response.raise_for_status()
            return response
        except requests.RequestException as exc:
            last_error = exc
            if attempt == 5:
                break
            time.sleep(2 * attempt)
    raise last_error if last_error else RuntimeError(f"GitHub request failed: {method} {url}")


def set_github_secret(token: str, repo: str, name: str, value: str) -> None:
    headers = github_headers(token)
    pk_resp = github_request("GET", f"https://api.github.com/repos/{repo}/actions/secrets/public-key", headers=headers)
    payload = pk_resp.json()
    public_key = public.PublicKey(payload["key"].encode("utf-8"), encoding.Base64Encoder())
    sealed_box = public.SealedBox(public_key)
    encrypted = base64.b64encode(sealed_box.encrypt(value.encode("utf-8"))).decode("ascii")
    github_request(
        "PUT",
        f"https://api.github.com/repos/{repo}/actions/secrets/{name}",
        headers=headers,
        json_payload={"encrypted_value": encrypted, "key_id": payload["key_id"]},
    )


def set_github_variable(token: str, repo: str, name: str, value: str) -> None:
    headers = github_headers(token)
    patch_payload = {"name": name, "value": value}
    try:
        github_request(
            "PATCH",
            f"https://api.github.com/repos/{repo}/actions/variables/{name}",
            headers=headers,
            json_payload=patch_payload,
        )
        return
    except requests.HTTPError as exc:
        response = exc.response
        if response is None or response.status_code != 404:
            raise
        github_request(
            "POST",
            f"https://api.github.com/repos/{repo}/actions/variables",
            headers=headers,
            json_payload=patch_payload,
        )
        return


def delete_github_variable(token: str, repo: str, name: str) -> None:
    headers = github_headers(token)
    try:
        github_request(
            "DELETE",
            f"https://api.github.com/repos/{repo}/actions/variables/{name}",
            headers=headers,
        )
    except requests.HTTPError as exc:
        response = exc.response
        if response is None or response.status_code != 404:
            raise
    except requests.RequestException as exc:
        print(f"warning: failed to delete GitHub variable {name}: {exc}", file=sys.stderr)


def main() -> int:
    parser = argparse.ArgumentParser(description="Regenerate local config.yaml and sync GitHub deployment settings.")
    parser.add_argument("--config-path", default=str(DEFAULT_CONFIG_PATH))
    parser.add_argument("--template-path", default=str(DEFAULT_TEMPLATE_PATH))
    parser.add_argument("--repo", default=REPO)
    parser.add_argument("--fallback-cloudflare-config", default=str(DEFAULT_EASYEMAIL_CONFIG))
    parser.add_argument("--owner-public-key-path", default=str(DEFAULT_OWNER_PUBLIC_KEY_PATH))
    parser.add_argument("--owner-private-key-path", default=str(DEFAULT_OWNER_PRIVATE_KEY_PATH))
    parser.add_argument("--owner-key-bundle-path", default=str(DEFAULT_OWNER_BUNDLE_PATH))
    parser.add_argument("--skip-github-sync", action="store_true")
    args = parser.parse_args()

    gh_token = os.environ.get("GH_TOKEN") or os.environ.get("GITHUB_TOKEN")
    if not gh_token:
        raise SystemExit("Missing GH_TOKEN or GITHUB_TOKEN.")

    config_path = Path(args.config_path)
    template_path = Path(args.template_path)
    fallback_cf_path = Path(args.fallback_cloudflare_config)
    owner_public_key_path = Path(args.owner_public_key_path)
    owner_private_key_path = Path(args.owner_private_key_path)
    owner_bundle_path = Path(args.owner_key_bundle_path)

    template_config = load_yaml(template_path)
    existing_config = load_yaml(config_path) if config_path.exists() else {}
    config = deep_merge(template_config, existing_config)
    if not config:
        raise SystemExit(f"Failed to load config template from {template_path}")

    auth_email, global_api_key = load_cloudflare_auth(config, fallback_cf_path)
    cf_session = requests.Session()
    cf_session.headers.update(
        {
            "X-Auth-Email": auth_email,
            "X-Auth-Key": global_api_key,
            "Content-Type": "application/json",
        }
    )
    account_id = resolve_account_id(cf_session, config)
    try:
        workers_subdomain = fetch_workers_subdomain(cf_session, account_id)
    except requests.RequestException:
        workers_subdomain = infer_workers_subdomain(config)

    misub_public_url = "https://misub.aiaimimi.com"
    misub_callback_url = "https://misub.aiaimimi.com"
    misub_project_name = "misub-git"
    misub_branch = "main"
    misub_d1_name = "misub"
    misub_d1_binding = "MISUB_DB"
    misub_manifest_profile_id = "aggregator-global"
    misub_connector_profile_id = "easyproxies-ech-runtime"
    ech_worker_public_url = "https://proxyservice-ech-workers.aiaimimi.com"
    aggregator_public_base_url = "https://sub.aiaimimi.com"
    aggregator_effective_url = "https://sub.aiaimimi.com/subs/effective.txt"
    service_base_network_name = "EasyAiMi"
    service_base_dns_servers = ["1.1.1.1", "8.8.8.8", "223.5.5.5"]
    shared_management_probe_targets = load_proxy_availability_probe_targets()
    default_preferred_entry_ips = [
        "198.41.185.161",
        "198.41.230.22",
        "198.41.251.20",
        "162.158.3.95",
        "198.41.247.52",
    ]
    legacy_shared_token = read_legacy_aggregator_shared_token(DEFAULT_LEGACY_AGGREGATOR_CLASH_CONFIG)

    misub_admin_password = get_or_generate(config, ("misub", "docker", "env", "ADMIN_PASSWORD"), lambda: secrets.token_urlsafe(24))
    misub_cookie_secret = get_or_generate(config, ("misub", "docker", "env", "COOKIE_SECRET"), lambda: secrets.token_hex(32))
    misub_manifest_token = get_or_generate(config, ("misub", "docker", "env", "MANIFEST_TOKEN"), lambda: secrets.token_urlsafe(32))
    ech_token = get_or_generate(config, ("echWorkersCloudflare", "secrets", "ECH_TOKEN"), lambda: secrets.token_urlsafe(32))
    aggregator_shared_token = resolve_optional_secret(
        config,
        ("aggregator", "sharedToken"),
        "EASYPROXY_AGGREGATOR_SHARED_TOKEN",
        legacy_shared_token,
    )
    service_base_management_password = get_or_generate(
        config, ("serviceBase", "runtime", "management", "password"), lambda: secrets.token_urlsafe(24)
    )

    cloudflare_api_token = str(get_nested(config, "cloudflare", "apiToken", default="")).strip()
    if not cloudflare_api_token or is_placeholder(cloudflare_api_token):
        cloudflare_deploy_token = create_account_token(
            cf_session,
            account_id,
            "EasyProxy Deploy Cloudflare",
            [
                {
                    "effect": "allow",
                    "resources": {f"com.cloudflare.api.account.{account_id}": "*"},
                    "permission_groups": [
                        {"id": CF_PERMISSION_GROUPS["pages_write"]},
                        {"id": CF_PERMISSION_GROUPS["d1_write"]},
                        {"id": CF_PERMISSION_GROUPS["workers_scripts_write"]},
                    ],
                }
            ],
        )
        cloudflare_api_token = cloudflare_deploy_token["value"]

    ensure_bucket(cf_session, account_id, "aggregator")
    ensure_bucket(cf_session, account_id, "easyproxy-private")

    agg_access_key_id = str(get_nested(config, "aggregator", "r2", "accessKeyId", default="")).strip()
    agg_secret_access_key = str(get_nested(config, "aggregator", "r2", "secretAccessKey", default="")).strip()
    if not agg_access_key_id or not agg_secret_access_key:
        agg_token = create_account_token(
            cf_session,
            account_id,
            "EasyProxy Aggregator R2 Upload",
            [
                {
                    "effect": "allow",
                    "resources": {bucket_resource(account_id, "aggregator"): "*"},
                    "permission_groups": [{"id": CF_PERMISSION_GROUPS["r2_bucket_write"]}],
                }
            ],
        )
        agg_access_key_id, agg_secret_access_key = to_r2_credentials(agg_token)

    dist = ensure_dict(ensure_dict(config, "distribution"), "serviceBase")
    upload_access_key_id = str(dist.get("accessKeyId") or "").strip()
    upload_secret_access_key = str(dist.get("secretAccessKey") or "").strip()
    if not upload_access_key_id or not upload_secret_access_key:
        upload_token = create_account_token(
            cf_session,
            account_id,
            "EasyProxy Service Base R2 Upload",
            [
                {
                    "effect": "allow",
                    "resources": {bucket_resource(account_id, "easyproxy-private"): "*"},
                    "permission_groups": [{"id": CF_PERMISSION_GROUPS["r2_bucket_write"]}],
                }
            ],
        )
        upload_access_key_id, upload_secret_access_key = to_r2_credentials(upload_token)

    read_access_key_id = str(dist.get("readAccessKeyId") or "").strip()
    read_secret_access_key = str(dist.get("readSecretAccessKey") or "").strip()
    if not read_access_key_id or not read_secret_access_key:
        read_token = create_account_token(
            cf_session,
            account_id,
            "EasyProxy Service Base R2 Client Read",
            [
                {
                    "effect": "allow",
                    "resources": {bucket_resource(account_id, "easyproxy-private"): "*"},
                    "permission_groups": [{"id": CF_PERMISSION_GROUPS["r2_bucket_read"]}],
                }
            ],
        )
        read_access_key_id, read_secret_access_key = to_r2_credentials(read_token)

    ensure_owner_keypair(owner_public_key_path, owner_private_key_path, owner_bundle_path)
    owner_public_key = owner_public_key_path.read_text(encoding="utf-8").strip()

    set_nested(config, ("cloudflare", "accountId"), account_id)
    set_nested(config, ("cloudflare", "authEmail"), auth_email)
    set_nested(config, ("cloudflare", "globalApiKey"), global_api_key)
    set_nested(config, ("cloudflare", "apiToken"), cloudflare_api_token)

    set_nested(config, ("ghcr", "owner"), "aiaimimi0920")
    set_nested(config, ("ghcr", "platform"), "linux/amd64")
    set_nested(config, ("ghcr", "serviceImageName"), "easy-proxy-monorepo-service")
    set_nested(config, ("ghcr", "echWorkersImageName"), "ech-workers-monorepo")

    set_nested(config, ("misub", "projectRoot"), "upstreams/misub")
    set_nested(config, ("misub", "pages", "projectName"), misub_project_name)
    set_nested(config, ("misub", "pages", "branch"), misub_branch)
    set_nested(config, ("misub", "pages", "publicUrl"), misub_public_url)
    set_nested(config, ("misub", "pages", "callbackUrl"), misub_callback_url)
    set_nested(config, ("misub", "pages", "d1DatabaseName"), misub_d1_name)
    set_nested(config, ("misub", "pages", "d1DatabaseBinding"), misub_d1_binding)
    set_nested(config, ("misub", "pages", "verifyManifestProfileId"), misub_manifest_profile_id)
    set_nested(config, ("misub", "pages", "connectorProfileId"), misub_connector_profile_id)
    existing_misub_additional_subscriptions = get_nested(config, "misub", "pages", "additionalSubscriptions", default=[])
    if not isinstance(existing_misub_additional_subscriptions, list):
        existing_misub_additional_subscriptions = []
    set_nested(config, ("misub", "pages", "additionalSubscriptions"), existing_misub_additional_subscriptions)
    misub_pages = ensure_dict(ensure_dict(config, "misub"), "pages")
    misub_pages.pop("verifyConnectorProfileId", None)
    set_nested(config, ("misub", "docker", "env", "ADMIN_PASSWORD"), misub_admin_password)
    set_nested(config, ("misub", "docker", "env", "COOKIE_SECRET"), misub_cookie_secret)
    set_nested(config, ("misub", "docker", "env", "MANIFEST_TOKEN"), misub_manifest_token)
    set_nested(config, ("misub", "docker", "env", "MISUB_PUBLIC_URL"), misub_public_url)
    set_nested(config, ("misub", "docker", "env", "MISUB_CALLBACK_URL"), misub_callback_url)

    set_nested(config, ("aggregator", "projectRoot"), "upstreams/aggregator")
    set_nested(config, ("aggregator", "workflowFile"), "deploy-aggregator.yml")
    set_nested(config, ("aggregator", "ref"), "main")
    set_nested(config, ("aggregator", "configPath"), "deploy/upstreams/aggregator/config/config.actions.r2.json")
    set_nested(config, ("aggregator", "publicBaseUrl"), aggregator_public_base_url)
    set_nested(config, ("aggregator", "effectiveUrl"), aggregator_effective_url)
    set_nested(config, ("aggregator", "sharedToken"), aggregator_shared_token)
    set_nested(config, ("aggregator", "r2", "accessKeyId"), agg_access_key_id)
    set_nested(config, ("aggregator", "r2", "secretAccessKey"), agg_secret_access_key)
    set_nested(config, ("aggregator", "r2", "accountId"), account_id)
    for obsolete_key in ("githubRepo", "workflow", "secretName", "seedSubKey"):
        if isinstance(config.get("aggregator"), dict):
            config["aggregator"].pop(obsolete_key, None)

    set_nested(config, ("echWorkersCloudflare", "publicUrl"), ech_worker_public_url)
    preferred_entry_ips = get_nested(config, "echWorkersCloudflare", "preferredEntryIps", default=default_preferred_entry_ips)
    if not isinstance(preferred_entry_ips, list) or len(preferred_entry_ips) == 0:
        preferred_entry_ips = default_preferred_entry_ips
    set_nested(config, ("echWorkersCloudflare", "preferredEntryIps"), preferred_entry_ips)
    set_nested(config, ("echWorkersCloudflare", "secrets", "ECH_TOKEN"), ech_token)

    set_nested(config, ("serviceBase", "networkName"), service_base_network_name)
    existing_dns_servers = get_nested(config, "serviceBase", "dnsServers", default=service_base_dns_servers)
    if not isinstance(existing_dns_servers, list) or len(existing_dns_servers) == 0:
        existing_dns_servers = service_base_dns_servers
    set_nested(config, ("serviceBase", "dnsServers"), existing_dns_servers)
    set_nested(config, ("serviceBase", "runtime", "management", "password"), service_base_management_password)
    if shared_management_probe_targets:
        set_nested(config, ("serviceBase", "runtime", "management", "probe_targets"), shared_management_probe_targets)
        set_nested(config, ("serviceBase", "runtime", "management", "probe_target"), "")
    set_nested(config, ("serviceBase", "runtime", "source_sync", "manifest_url"), f"{misub_public_url}/api/manifest/{misub_manifest_profile_id}")
    set_nested(config, ("serviceBase", "runtime", "source_sync", "manifest_token"), misub_manifest_token)
    set_nested(config, ("serviceBase", "runtime", "source_sync", "fallback_subscriptions"), [aggregator_effective_url])
    set_nested(config, ("serviceBase", "runtime", "source_sync", "connector_runtime", "startup_timeout"), "30s")
    existing_subscriptions = get_nested(config, "serviceBase", "runtime", "subscriptions", default=[])
    if not isinstance(existing_subscriptions, list):
        existing_subscriptions = []
    set_nested(config, ("serviceBase", "runtime", "subscriptions"), existing_subscriptions)
    connectors = get_nested(config, "serviceBase", "runtime", "connectors", default=[])
    if not isinstance(connectors, list) or not connectors:
        connectors = [
            {
                "name": "ECH Worker Template",
                "input": f"{ech_worker_public_url}:443",
                "enabled": False,
                "template_only": True,
                "group": "ECH Connectors",
                "notes": "Local template for generating preferred Cloudflare entry IP connectors",
                "connector_type": "ech_worker",
                "connector_config": {
                    "local_protocol": "socks5",
                    "access_token": ech_token,
                },
            }
        ]
    else:
        connectors[0]["input"] = f"{ech_worker_public_url}:443"
        connector_cfg = connectors[0].get("connector_config")
        if not isinstance(connector_cfg, dict):
            connector_cfg = {}
            connectors[0]["connector_config"] = connector_cfg
        connector_cfg["access_token"] = ech_token
    set_nested(config, ("serviceBase", "runtime", "connectors"), connectors)

    set_nested(config, ("distribution", "serviceBase", "accountId"), account_id)
    set_nested(config, ("distribution", "serviceBase", "bucket"), "easyproxy-private")
    set_nested(config, ("distribution", "serviceBase", "endpoint"), f"https://{account_id}.r2.cloudflarestorage.com")
    set_nested(config, ("distribution", "serviceBase", "configObjectKey"), "service-base/config.yaml")
    set_nested(config, ("distribution", "serviceBase", "manifestObjectKey"), "service-base/manifest.json")
    set_nested(config, ("distribution", "serviceBase", "accessKeyId"), upload_access_key_id)
    set_nested(config, ("distribution", "serviceBase", "secretAccessKey"), upload_secret_access_key)
    set_nested(config, ("distribution", "serviceBase", "readAccessKeyId"), read_access_key_id)
    set_nested(config, ("distribution", "serviceBase", "readSecretAccessKey"), read_secret_access_key)

    root_config_yaml = yaml.safe_dump(config, allow_unicode=True, sort_keys=False)
    root_config_yaml_b64 = base64.b64encode(root_config_yaml.encode("utf-8")).decode("ascii")
    save_yaml(config_path, config)

    if not args.skip_github_sync:
        secrets_map = {
            "CLOUDFLARE_ACCOUNT_ID": account_id,
            "CLOUDFLARE_API_TOKEN": cloudflare_api_token,
            "CLOUDFLARE_AUTH_EMAIL": auth_email,
            "CLOUDFLARE_GLOBAL_API_KEY": global_api_key,
            "MISUB_ADMIN_PASSWORD": misub_admin_password,
            "MISUB_COOKIE_SECRET": misub_cookie_secret,
            "MISUB_MANIFEST_TOKEN": misub_manifest_token,
            "ECH_TOKEN": ech_token,
            "EASYPROXY_AGGREGATOR_GH_TOKEN": gh_token,
            "EASYPROXY_AGGREGATOR_R2_ACCESS_KEY_ID": agg_access_key_id,
            "EASYPROXY_AGGREGATOR_R2_SECRET_ACCESS_KEY": agg_secret_access_key,
            "EASYPROXY_AGGREGATOR_R2_ACCOUNT_ID": account_id,
            "EASYPROXY_R2_CONFIG_ACCOUNT_ID": account_id,
            "EASYPROXY_R2_CONFIG_BUCKET": "easyproxy-private",
            "EASYPROXY_R2_CONFIG_ENDPOINT": f"https://{account_id}.r2.cloudflarestorage.com",
            "EASYPROXY_R2_CONFIG_CONFIG_OBJECT_KEY": "service-base/config.yaml",
            "EASYPROXY_R2_CONFIG_MANIFEST_OBJECT_KEY": "service-base/manifest.json",
            "EASYPROXY_R2_CONFIG_UPLOAD_ACCESS_KEY_ID": upload_access_key_id,
            "EASYPROXY_R2_CONFIG_UPLOAD_SECRET_ACCESS_KEY": upload_secret_access_key,
            "EASYPROXY_R2_CONFIG_READ_ACCESS_KEY_ID": read_access_key_id,
            "EASYPROXY_R2_CONFIG_READ_SECRET_ACCESS_KEY": read_secret_access_key,
            "EASYPROXY_ROOT_CONFIG_YAML_B64": root_config_yaml_b64,
            "EASYPROXY_SERVICE_BASE_MANAGEMENT_PASSWORD": service_base_management_password,
            "EASYPROXY_IMPORT_CODE_OWNER_PUBLIC_KEY": owner_public_key,
        }
        if aggregator_shared_token:
            secrets_map["EASYPROXY_AGGREGATOR_SHARED_TOKEN"] = aggregator_shared_token
        variables_map = {
            "EASYPROXY_AGGREGATOR_PUBLIC_BASE_URL": aggregator_public_base_url,
            "EASYPROXY_AGGREGATOR_EFFECTIVE_URL": aggregator_effective_url,
            "EASYPROXY_ECH_PREFERRED_ENTRY_IPS": ",".join(str(item).strip() for item in preferred_entry_ips if str(item).strip()),
            "EASYPROXY_ECH_WORKER_PUBLIC_URL": ech_worker_public_url,
            "EASYPROXY_MISUB_CALLBACK_URL": misub_callback_url,
            "EASYPROXY_MISUB_ADDITIONAL_SUBSCRIPTIONS_JSON": json.dumps(existing_misub_additional_subscriptions, ensure_ascii=False),
            "EASYPROXY_MISUB_CONNECTOR_PROFILE_ID": misub_connector_profile_id,
            "EASYPROXY_MISUB_D1_DATABASE_BINDING": misub_d1_binding,
            "EASYPROXY_MISUB_D1_DATABASE_NAME": misub_d1_name,
            "EASYPROXY_MISUB_MANIFEST_PROFILE_ID": misub_manifest_profile_id,
            "EASYPROXY_MISUB_PUBLIC_URL": misub_public_url,
        }
        for name, value in secrets_map.items():
            set_github_secret(gh_token, args.repo, name, str(value))
        for name, value in variables_map.items():
            set_github_variable(gh_token, args.repo, name, str(value))
        delete_github_variable(gh_token, args.repo, "EASYPROXY_MISUB_VERIFY_CONNECTOR_PROFILE_ID")

    summary = {
        "configPath": str(config_path),
        "repo": args.repo,
        "cloudflareAccountId": account_id,
        "workersSubdomain": workers_subdomain,
        "misubPublicUrl": misub_public_url,
        "echWorkerPublicUrl": ech_worker_public_url,
        "aggregatorPublicBaseUrl": aggregator_public_base_url,
        "aggregatorSharedTokenConfigured": bool(aggregator_shared_token),
        "serviceBaseDistributionBucket": "easyproxy-private",
    }
    print(json.dumps(summary, ensure_ascii=False, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
