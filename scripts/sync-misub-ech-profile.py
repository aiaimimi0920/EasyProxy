#!/usr/bin/env python3

from __future__ import annotations

import argparse
import copy
import json
import os
import subprocess
import sys
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


def find_profile(profiles: list[dict[str, Any]], profile_ids: list[str]) -> dict[str, Any] | None:
    expected = {item.strip() for item in profile_ids if str(item).strip()}
    for profile in profiles:
        profile_custom_id = str(profile.get("customId", "")).strip()
        profile_id = str(profile.get("id", "")).strip()
        if profile_custom_id in expected or profile_id in expected:
            return profile
    return None


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


def profile_identifiers(profile: dict[str, Any]) -> set[str]:
    identifiers: set[str] = set()
    for value in (profile.get("id"), profile.get("customId")):
        item = str(value or "").strip()
        if not item:
            continue
        identifiers.add(item)
        identifiers.add(item.replace("_", "-"))
    return identifiers


def attach_sources_to_profiles(
    profiles: list[dict[str, Any]],
    profile_ids: list[str],
    source_ids: list[str],
    source_id_prefix: str,
) -> tuple[list[dict[str, Any]], list[str]]:
    requested_ids = normalize_string_array(profile_ids)
    expected = set(requested_ids)
    found: set[str] = set()
    prefix = f"{source_id_prefix}_"
    updated_profiles: list[dict[str, Any]] = []

    for profile in profiles:
        matched_ids = expected.intersection(profile_identifiers(profile))
        if not matched_ids:
            updated_profiles.append(profile)
            continue

        found.update(matched_ids)
        updated_profile = dict(profile)
        manual_nodes = [
            node_id
            for node_id in normalize_string_array(profile.get("manualNodes"))
            if not node_id.startswith(prefix)
        ]
        updated_profile["manualNodes"] = normalize_string_array(manual_nodes + source_ids)
        updated_profiles.append(updated_profile)

    missing = [profile_id for profile_id in requested_ids if profile_id not in found]
    return updated_profiles, missing


def validate_managed_connector_sources(
    sources: list[dict[str, Any]],
    expected_source_ids: list[str],
    worker_url: str,
    access_token: str,
) -> None:
    expected = set(expected_source_ids)
    managed_sources = [
        source
        for source in sources
        if str(source.get("id", "")).strip() in expected
    ]
    actual = {str(source.get("id", "")).strip() for source in managed_sources}
    ensure(actual == expected, "MiSub connector manifest is missing managed ECH sources")

    for source in managed_sources:
        options = source.get("options") if isinstance(source.get("options"), dict) else {}
        ensure(
            str(options.get("connector_type") or source.get("connector_type") or "").strip() == "ech_worker",
            "MiSub connector manifest returned a managed source with an unexpected connector type",
        )
        ensure(str(source.get("input", "")).strip() == worker_url, "MiSub connector manifest returned an unexpected worker URL")
        connector_config = options.get("connector_config") or source.get("connector_config") or {}
        ensure(
            str(connector_config.get("access_token", "")).strip() == access_token,
            "MiSub connector manifest returned an outdated access token",
        )


def normalize_existing_sources(
    misubs: list[dict[str, Any]],
    source_id_prefix: str,
) -> tuple[list[dict[str, Any]], list[str]]:
    existing_sources: list[dict[str, Any]] = []
    server_ips: list[str] = []
    prefix = f"{source_id_prefix}_"
    for source in misubs:
        source_id = str(source.get("id", "")).strip()
        if not source_id.startswith(prefix):
            continue
        existing_sources.append(source)
        connector_cfg = source_connector_config(source)
        server_ip = str(connector_cfg.get("server_ip", "")).strip()
        if server_ip:
            server_ips.append(server_ip)
    return existing_sources, server_ips


def source_connector_config(source: dict[str, Any]) -> dict[str, Any]:
    direct = source.get("connector_config")
    if isinstance(direct, dict) and direct:
        return direct
    options = source.get("options")
    nested = options.get("connector_config") if isinstance(options, dict) else None
    return nested if isinstance(nested, dict) else {}


def select_server_ips(explicit: list[str], existing: list[str], drop_existing: bool) -> list[str]:
    selected = [str(item).strip() for item in explicit if str(item).strip()]
    if selected:
        return normalize_string_array(selected)
    if drop_existing:
        return []
    return normalize_string_array(existing)


def post_misub_state(session, base_url: str, payload: dict[str, Any], label: str) -> None:
    response = retry(label, 10, 5, lambda: session.post(base_url + "api/misubs", json=payload, timeout=60))
    response.raise_for_status()


def validate_manifest_profiles(
    session,
    base_url: str,
    manifest_token: str,
    profile_ids: list[str],
    source_ids: list[str],
    worker_url: str,
    access_token: str,
) -> None:
    for profile_id in normalize_string_array(profile_ids):
        response = retry(
            f"MiSub connector manifest {profile_id}",
            10,
            5,
            lambda current=profile_id: session.get(
                base_url + f"api/manifest/{current}",
                headers={"Authorization": f"Bearer {manifest_token}"},
                timeout=30,
            ),
        )
        response.raise_for_status()
        payload = response.json()
        ensure(payload.get("success") is True, f"MiSub manifest {profile_id} did not report success")
        validate_managed_connector_sources(
            payload.get("sources") or [], source_ids, worker_url, access_token
        )


def build_sources(
    worker_url: str,
    access_token: str,
    server_ips: list[str],
    local_protocol: str,
    source_id_prefix: str,
    source_name_prefix: str,
    source_group: str,
    notes_prefix: str,
) -> list[dict[str, Any]]:
    if not server_ips:
        server_ips = [""]

    sources: list[dict[str, Any]] = []
    for index, server_ip in enumerate(server_ips, start=1):
        connector_config: dict[str, Any] = {
            "local_protocol": local_protocol,
            "access_token": access_token,
        }
        if server_ip:
            connector_config["server_ip"] = server_ip

        notes = f"{notes_prefix} #{index}" if server_ip else "Managed ECH connector"
        source = {
            "id": f"{source_id_prefix}_{index}",
            "kind": "connector",
            "name": f"{source_name_prefix} {index}",
            "enabled": True,
            "group": source_group,
            "notes": notes,
            "input": worker_url,
            "url": worker_url,
            "connector_type": "ech_worker",
            "connector_config": connector_config,
            "options": {
                "connector_type": "ech_worker",
                "connector_config": connector_config,
            },
        }
        sources.append(source)
    return sources


def build_candidate_sources(sources: list[dict[str, Any]], candidate_prefix: str) -> list[dict[str, Any]]:
    candidates: list[dict[str, Any]] = []
    for index, source in enumerate(sources, start=1):
        candidate = copy.deepcopy(source)
        candidate["id"] = f"{candidate_prefix}{index}"
        candidate["name"] = f"{source['name']} Candidate"
        connector_config = source_connector_config(candidate)
        connector_config["easyproxy_candidate_source_id"] = candidate["id"]
        candidate["connector_config"] = connector_config
        candidate.setdefault("options", {})["connector_config"] = connector_config
        candidates.append(candidate)
    return candidates


def main() -> int:
    parser = argparse.ArgumentParser(description="Synchronize the MiSub ECH connector test profile with the current worker URL/token.")
    parser.add_argument("--base-url", required=True)
    parser.add_argument("--profile-id", default="easyproxies-ech-runtime")
    parser.add_argument("--legacy-profile-id", default="easyproxies-ech-test")
    parser.add_argument("--profile-name", default="EasyProxies ECH Runtime")
    parser.add_argument(
        "--profile-description",
        default="Private profile for routing EasyProxy-managed ECH connector sources through the current self-hosted ECH Worker",
    )
    parser.add_argument("--worker-url", required=True)
    parser.add_argument("--local-protocol", default="socks5")
    parser.add_argument("--source-id-prefix", default="conn_ech_workers_pref")
    parser.add_argument("--source-name-prefix", default="ECH Worker Preferred")
    parser.add_argument("--source-group", default="ECH Connectors")
    parser.add_argument("--notes-prefix", default="Preferred Cloudflare entry IP")
    parser.add_argument(
        "--drop-server-ips",
        action="store_true",
        help="Discard existing managed server_ip values. Preservation is the safe default.",
    )
    parser.add_argument("--server-ip", action="append", default=[])
    parser.add_argument("--remove-profile-id", action="append", default=[])
    parser.add_argument("--attach-profile-id", action="append", default=[])
    parser.add_argument("--candidate-easyproxy-base-url", default="")
    parser.add_argument("--candidate-easyproxy-proxy-url", default="")
    parser.add_argument("--candidate-easyproxy-profile-id", default="")
    parser.add_argument("--candidate-easyproxy-wait-seconds", type=int, default=600)
    args = parser.parse_args()

    admin_password = os.environ.get("MISUB_ADMIN_PASSWORD", "")
    manifest_token = os.environ.get("MISUB_MANIFEST_TOKEN", "")
    access_token = os.environ.get("ECH_TOKEN", "")
    ensure(admin_password != "", "MISUB_ADMIN_PASSWORD is required")
    ensure(manifest_token != "", "MISUB_MANIFEST_TOKEN is required")
    ensure(access_token != "", "ECH_TOKEN is required")

    base_url = args.base_url.rstrip("/") + "/"
    session = requests.Session()

    def login_request():
        response = session.post(
            base_url + "api/login",
            json={"password": admin_password},
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

    misubs = payload.get("misubs") or []
    profiles = payload.get("profiles") or []
    ensure(isinstance(misubs, list), "MiSub /api/data did not return a misubs array")
    ensure(isinstance(profiles, list), "MiSub /api/data did not return a profiles array")

    profile = find_profile(profiles, [args.profile_id, args.legacy_profile_id])

    existing_sources, existing_server_ips = normalize_existing_sources(misubs, args.source_id_prefix)
    candidate_prefix = f"{args.source_id_prefix}_candidate_"
    existing_sources = [
        source
        for source in existing_sources
        if not str(source.get("id", "")).strip().startswith(candidate_prefix)
    ]
    existing_server_ips = [
        str(source_connector_config(source).get("server_ip", "")).strip()
        for source in existing_sources
        if str(source_connector_config(source).get("server_ip", "")).strip()
    ]
    selected_server_ips = select_server_ips(args.server_ip, existing_server_ips, args.drop_server_ips)
    new_sources = build_sources(
        worker_url=args.worker_url,
        access_token=access_token,
        server_ips=selected_server_ips,
        local_protocol=args.local_protocol,
        source_id_prefix=args.source_id_prefix,
        source_name_prefix=args.source_name_prefix,
        source_group=args.source_group,
        notes_prefix=args.notes_prefix,
    )

    retained_misubs = [
        source
        for source in misubs
        if not str(source.get("id", "")).strip().startswith(f"{args.source_id_prefix}_")
    ]
    updated_misubs = retained_misubs + new_sources

    existing_manual_nodes = (profile or {}).get("manualNodes") or []
    filtered_manual_nodes = [
        node_id
        for node_id in existing_manual_nodes
        if not str(node_id).strip().startswith(f"{args.source_id_prefix}_")
    ]
    filtered_manual_nodes.extend(source["id"] for source in new_sources)

    updated_profile = dict(profile or {})
    updated_profile["id"] = args.profile_id.replace("-", "_")
    updated_profile["customId"] = args.profile_id
    updated_profile["name"] = args.profile_name
    updated_profile["enabled"] = bool(updated_profile.get("enabled", True))
    updated_profile["subscriptions"] = list(updated_profile.get("subscriptions") or [])
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
    updated_profile["manualNodes"] = filtered_manual_nodes

    profile_ids_to_remove = {str(item).strip() for item in args.remove_profile_id if str(item).strip()}
    updated_profiles = []
    for candidate in profiles:
        candidate_custom_id = str(candidate.get("customId", "")).strip()
        candidate_id = str(candidate.get("id", "")).strip()
        if candidate_custom_id in profile_ids_to_remove or candidate_id in {item.replace("-", "_") for item in profile_ids_to_remove}:
            continue
        if (
            candidate is profile
            or candidate_custom_id in {args.profile_id, args.legacy_profile_id}
            or candidate_id in {args.profile_id.replace("-", "_"), args.legacy_profile_id.replace("-", "_")}
        ):
            updated_profiles.append(updated_profile)
        else:
            updated_profiles.append(candidate)
    if profile is None:
        updated_profiles.append(updated_profile)

    managed_source_ids = [source["id"] for source in new_sources]
    updated_profiles, missing_attach_profiles = attach_sources_to_profiles(
        updated_profiles,
        args.attach_profile_id,
        managed_source_ids,
        args.source_id_prefix,
    )
    ensure(
        not missing_attach_profiles,
        f"MiSub attach profiles not found: {', '.join(missing_attach_profiles)}",
    )

    candidate_sources = build_candidate_sources(new_sources, candidate_prefix)
    candidate_source_ids = [source["id"] for source in candidate_sources]
    old_source_ids = [str(source.get("id", "")).strip() for source in existing_sources]
    candidate_profiles, missing_candidate_profiles = attach_sources_to_profiles(
        updated_profiles,
        [args.profile_id] + args.attach_profile_id,
        old_source_ids + candidate_source_ids,
        args.source_id_prefix,
    )
    ensure(not missing_candidate_profiles, "MiSub candidate profiles could not be prepared")
    candidate_misubs = [
        source
        for source in misubs
        if not str(source.get("id", "")).strip().startswith(candidate_prefix)
    ] + candidate_sources
    candidate_payload = {"misubs": candidate_misubs, "profiles": candidate_profiles}

    update_payload = {
        "misubs": updated_misubs,
        "profiles": updated_profiles,
    }
    original_payload = {"misubs": misubs, "profiles": profiles}
    try:
        post_misub_state(session, base_url, candidate_payload, "MiSub candidate connector update")
        validate_manifest_profiles(
            session,
            base_url,
            manifest_token,
            [args.profile_id] + args.attach_profile_id,
            candidate_source_ids,
            args.worker_url,
            access_token,
        )
        candidate_checks = [
            args.candidate_easyproxy_base_url,
            args.candidate_easyproxy_proxy_url,
            args.candidate_easyproxy_profile_id,
        ]
        ensure(all(candidate_checks) or not any(candidate_checks), "Both candidate EasyProxy URLs are required")
        if all(candidate_checks):
            subprocess.run(
                [
                    sys.executable,
                    str(Path(__file__).with_name("verify-easyproxy-ech-sync.py")),
                    "--base-url", args.candidate_easyproxy_base_url,
                    "--proxy-url", args.candidate_easyproxy_proxy_url,
                    "--worker-url", args.worker_url,
                    "--profile-id", args.candidate_easyproxy_profile_id,
                    "--wait-seconds", str(args.candidate_easyproxy_wait_seconds),
                ],
                check=True,
            )

        post_misub_state(session, base_url, update_payload, "MiSub profile update")
        validate_manifest_profiles(
            session,
            base_url,
            manifest_token,
            [args.profile_id] + args.attach_profile_id,
            managed_source_ids,
            args.worker_url,
            access_token,
        )
    except Exception as original_error:
        try:
            post_misub_state(session, base_url, original_payload, "MiSub connector rollback")
        except Exception as rollback_error:
            raise RuntimeError(
                f"MiSub connector update failed and rollback also failed: {rollback_error}"
            ) from original_error
        raise RuntimeError(
            f"MiSub connector update failed; original state restored: {original_error}"
        ) from original_error

    summary = {
        "profile_id": args.profile_id,
        "worker_url": args.worker_url,
        "preserve_server_ips": not args.drop_server_ips,
        "removed_profile_ids": sorted(profile_ids_to_remove),
        "attached_profile_ids": normalize_string_array(args.attach_profile_id),
        "source_count": len(new_sources),
        "server_ips": selected_server_ips,
        "validated_candidate_source_ids": candidate_source_ids,
        "updated_source_ids": [source["id"] for source in new_sources],
    }
    print(json.dumps(summary, ensure_ascii=False, indent=2))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as exc:  # pragma: no cover - CLI failure path
        print(str(exc), file=sys.stderr)
        raise SystemExit(1)
