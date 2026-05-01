#!/usr/bin/env python3

from __future__ import annotations

import argparse
import json
import subprocess
import sys
import tempfile
from pathlib import Path
from typing import Any

import requests


def ensure(condition: bool, message: str) -> None:
    if not condition:
        raise RuntimeError(message)


def retry(label: str, attempts: int, delay_seconds: float, func):
    import time

    last_error: Exception | None = None
    for attempt in range(1, attempts + 1):
        try:
            return func()
        except Exception as exc:  # pragma: no cover - retry wrapper
            last_error = exc
            if attempt == attempts:
                break
            time.sleep(delay_seconds)
    raise RuntimeError(f"{label} failed after {attempts} attempts: {last_error}") from last_error


def find_profile(profiles: list[dict[str, Any]], profile_ids: list[str]) -> tuple[int, dict[str, Any] | None]:
    expected = {item.strip() for item in profile_ids if str(item).strip()}
    for index, profile in enumerate(profiles):
        profile_custom_id = str(profile.get("customId", "")).strip()
        profile_id = str(profile.get("id", "")).strip()
        if profile_custom_id in expected or profile_id in expected or profile_id.replace("_", "-") in expected:
            return index, profile
    return -1, None


def normalize_string_array(value: Any) -> list[str]:
    if not isinstance(value, list):
        return []
    seen: set[str] = set()
    normalized: list[str] = []
    for entry in value:
        item = str(entry or "").strip()
        if not item or item in seen:
            continue
        seen.add(item)
        normalized.append(item)
    return normalized


def is_connector_source(source: Any) -> bool:
    return isinstance(source, dict) and str(source.get("kind", "")).strip() == "connector"


def resolve_connector_node_ids(
    *,
    settings: dict[str, Any] | None,
    existing_profile: dict[str, Any] | None,
    sources: list[dict[str, Any]],
) -> list[str]:
    source_map = {
        str(source.get("id", "")).strip(): source
        for source in sources
        if isinstance(source, dict) and str(source.get("id", "")).strip()
    }

    configured_ids = normalize_string_array(
        (((settings or {}).get("aggregatorSync") or {}).get("defaultPublicProfileConnectorIds"))
    )
    configured_connector_ids = [
        source_id
        for source_id in configured_ids
        if is_connector_source(source_map.get(source_id))
    ]
    if configured_connector_ids:
        return configured_connector_ids

    existing_manual_nodes = normalize_string_array((existing_profile or {}).get("manualNodes"))
    return [
        source_id
        for source_id in existing_manual_nodes
        if is_connector_source(source_map.get(source_id))
    ]


def run_runtime_audit(
    *,
    audit_script: Path,
    subscriptions: list[str],
    docker_network_name: str,
    image: str,
    scenario_timeout_seconds: int,
) -> dict[str, Any]:
    with tempfile.TemporaryDirectory(prefix="easyproxy-misub-audit-") as temp_dir:
        output_path = Path(temp_dir) / "summary.json"
        command = [
            sys.executable,
            str(audit_script),
            "--audit-id",
            f"misub-runtime-{Path(temp_dir).name}",
            "--output-path",
            str(output_path),
            "--docker-network-name",
            docker_network_name,
            "--scenario-timeout-seconds",
            str(scenario_timeout_seconds),
            "--build-if-missing",
        ]
        if image.strip():
            command.extend(["--image", image.strip()])
        for url in subscriptions:
            command.extend(["--subscription", url])

        completed = subprocess.run(command, text=True, capture_output=True, check=False)
        if completed.returncode != 0:
            stderr = (completed.stderr or completed.stdout or "").strip()
            raise RuntimeError(f"runtime audit failed: {stderr}")

        return json.loads(output_path.read_text(encoding="utf-8"))


def build_runtime_source(uri: str, source_id: str, source_group: str, note: str, generated_at: str) -> dict[str, Any]:
    return {
        "id": source_id,
        "kind": "proxy_uri",
        "name": source_id,
        "enabled": True,
        "group": source_group,
        "notes": note,
        "input": uri,
        "url": uri,
        "probe_status": "verified",
        "detected_kind": "proxy_uri",
        "last_probe_at": generated_at,
        "probe_message": "Verified by shared EasyProxy runtime audit.",
        "probe_input": uri,
        "options": {
            "managed_by": "easyproxy_runtime_sources",
            "source_role": "runtime_effective_proxy",
            "sync_managed": True,
        },
    }


def main() -> int:
    parser = argparse.ArgumentParser(description="Synchronize MiSub runtime proxy profile from shared EasyProxy source audit results.")
    parser.add_argument("--base-url", required=True)
    parser.add_argument("--admin-password", required=True)
    parser.add_argument("--manifest-token", required=True)
    parser.add_argument("--profile-id", default="aggregator-global")
    parser.add_argument("--profile-name", default="Aggregator Global")
    parser.add_argument(
        "--profile-description",
        default="Managed runtime profile containing only proxy lines that passed the shared EasyProxy availability audit.",
    )
    parser.add_argument("--source-id-prefix", default="proxy_runtime_node")
    parser.add_argument("--source-group", default="Managed Runtime Proxies")
    parser.add_argument("--subscription-url", action="append", default=[])
    parser.add_argument("--audit-script", default=str(Path(__file__).resolve().parent / "easyproxy_source_audit.py"))
    parser.add_argument("--docker-network-name", default="EasyAiMi")
    parser.add_argument("--image", default="")
    parser.add_argument("--scenario-timeout-seconds", type=int, default=720)
    args = parser.parse_args()

    subscription_urls = normalize_string_array(args.subscription_url)
    ensure(subscription_urls, "At least one --subscription-url is required")

    base_url = args.base_url.rstrip("/") + "/"
    session = requests.Session()

    def login_request():
        response = session.post(
            base_url + "api/login",
            json={"password": args.admin_password},
            timeout=30,
        )
        if response.status_code == 401:
            raise RuntimeError("MiSub login returned 401")
        response.raise_for_status()
        payload = response.json()
        ensure(bool(payload.get("success")), "MiSub login did not report success")
        return payload

    retry("MiSub login", 10, 5, login_request)
    data_response = retry("MiSub data fetch", 10, 5, lambda: session.get(base_url + "api/data", timeout=30))
    data_response.raise_for_status()
    payload = data_response.json()
    settings_response = retry("MiSub settings fetch", 10, 5, lambda: session.get(base_url + "api/settings", timeout=30))
    settings_response.raise_for_status()
    settings_payload = settings_response.json()

    misubs = payload.get("misubs") or []
    profiles = payload.get("profiles") or []
    settings = settings_payload if isinstance(settings_payload, dict) else {}
    ensure(isinstance(misubs, list), "MiSub /api/data did not return a misubs array")
    ensure(isinstance(profiles, list), "MiSub /api/data did not return a profiles array")

    audit_summary = run_runtime_audit(
        audit_script=Path(args.audit_script),
        subscriptions=subscription_urls,
        docker_network_name=args.docker_network_name,
        image=args.image,
        scenario_timeout_seconds=args.scenario_timeout_seconds,
    )
    stable_uris = normalize_string_array((audit_summary.get("nodes") or {}).get("stable_available_uris"))
    ensure(stable_uris, "shared runtime audit returned no stable proxy URIs")
    generated_at = str(audit_summary.get("audit_id") or "")

    retained_sources = []
    for source in misubs:
        source_id = str(source.get("id", "")).strip()
        managed_by = str(((source.get("options") or {}).get("managed_by") or "")).strip()
        if source_id.startswith(f"{args.source_id_prefix}_") or managed_by == "easyproxy_runtime_sources":
            continue
        retained_sources.append(source)

    runtime_source_ids: list[str] = []
    runtime_sources: list[dict[str, Any]] = []
    joined_sources = ", ".join(subscription_urls)
    for index, uri in enumerate(stable_uris, start=1):
        source_id = f"{args.source_id_prefix}_{index}"
        runtime_source_ids.append(source_id)
        runtime_sources.append(
            build_runtime_source(
                uri=uri,
                source_id=source_id,
                source_group=args.source_group,
                note=f"Managed by shared EasyProxy runtime audit | upstreams={joined_sources}",
                generated_at=generated_at,
            )
        )

    updated_sources = retained_sources + runtime_sources

    profile_index, existing_profile = find_profile(profiles, [args.profile_id])
    updated_profile = dict(existing_profile or {})
    updated_profile["id"] = args.profile_id.replace("-", "_")
    updated_profile["customId"] = args.profile_id
    updated_profile["name"] = args.profile_name
    updated_profile["enabled"] = bool(updated_profile.get("enabled", True))
    updated_profile["subscriptions"] = []
    connector_node_ids = resolve_connector_node_ids(
        settings=settings,
        existing_profile=existing_profile,
        sources=updated_sources,
    )
    updated_profile["manualNodes"] = runtime_source_ids + connector_node_ids
    updated_profile["expiresAt"] = str(updated_profile.get("expiresAt", "") or "")
    updated_profile["isPublic"] = bool(updated_profile.get("isPublic", False))
    updated_profile["description"] = args.profile_description
    if "prefixSettings" not in updated_profile or not isinstance(updated_profile.get("prefixSettings"), dict):
        updated_profile["prefixSettings"] = {
            "enableManualNodes": None,
            "enableSubscriptions": None,
            "manualNodePrefix": "",
            "prependGroupName": None,
        }
    if "nodeTransform" not in updated_profile:
        updated_profile["nodeTransform"] = None
    updated_profile["options"] = {
        **(updated_profile.get("options") if isinstance(updated_profile.get("options"), dict) else {}),
        "managed_by": "easyproxy_runtime_profile",
        "sync_managed": True,
        "source_role": "runtime_effective_profile",
    }

    updated_profiles = list(profiles)
    if profile_index >= 0:
        updated_profiles[profile_index] = updated_profile
    else:
        updated_profiles.insert(0, updated_profile)

    update_payload = {
        "misubs": updated_sources,
        "profiles": updated_profiles,
    }
    update_response = retry(
        "MiSub runtime profile update",
        10,
        5,
        lambda: session.post(base_url + "api/misubs", json=update_payload, timeout=60),
    )
    update_response.raise_for_status()

    manifest_response = retry(
        "MiSub runtime manifest",
        10,
        5,
        lambda: session.get(
            base_url + f"api/manifest/{args.profile_id}",
            headers={"Authorization": f"Bearer {args.manifest_token}"},
            timeout=30,
        ),
    )
    manifest_response.raise_for_status()
    manifest_payload = manifest_response.json()
    ensure(manifest_payload.get("success") is True, "MiSub manifest endpoint did not report success")
    manifest_sources = manifest_payload.get("sources") or []
    ensure(manifest_sources, "MiSub runtime manifest did not return any sources")
    ensure(
        all(str(source.get("kind", "")).strip() == "proxy_uri" for source in manifest_sources),
        "MiSub runtime manifest returned non-proxy sources",
    )

    summary = {
        "profile_id": args.profile_id,
        "subscription_urls": subscription_urls,
        "source_count": len(runtime_sources),
        "stable_uri_count": len(stable_uris),
        "stable_uris": stable_uris[:20],
    }
    print(json.dumps(summary, ensure_ascii=False, indent=2))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as exc:  # pragma: no cover - CLI failure path
        print(str(exc), file=sys.stderr)
        raise SystemExit(1)
