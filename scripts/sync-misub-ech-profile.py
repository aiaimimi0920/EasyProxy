#!/usr/bin/env python3

from __future__ import annotations

import argparse
import json
import sys
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


def find_profile(profiles: list[dict[str, Any]], profile_id: str) -> dict[str, Any] | None:
    for profile in profiles:
        if str(profile.get("customId", "")).strip() == profile_id or str(profile.get("id", "")).strip() == profile_id:
            return profile
    return None


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
        connector_cfg = source.get("connector_config") or {}
        server_ip = str(connector_cfg.get("server_ip", "")).strip()
        if server_ip:
            server_ips.append(server_ip)
    return existing_sources, server_ips


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


def main() -> int:
    parser = argparse.ArgumentParser(description="Synchronize the MiSub ECH connector test profile with the current worker URL/token.")
    parser.add_argument("--base-url", required=True)
    parser.add_argument("--admin-password", required=True)
    parser.add_argument("--manifest-token", required=True)
    parser.add_argument("--profile-id", default="easyproxies-ech-test")
    parser.add_argument("--worker-url", required=True)
    parser.add_argument("--access-token", required=True)
    parser.add_argument("--local-protocol", default="socks5")
    parser.add_argument("--source-id-prefix", default="conn_ech_workers_pref")
    parser.add_argument("--source-name-prefix", default="ECH Worker Preferred")
    parser.add_argument("--source-group", default="ECH Connectors")
    parser.add_argument("--notes-prefix", default="Preferred Cloudflare entry IP")
    args = parser.parse_args()

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

    misubs = payload.get("misubs") or []
    profiles = payload.get("profiles") or []
    ensure(isinstance(misubs, list), "MiSub /api/data did not return a misubs array")
    ensure(isinstance(profiles, list), "MiSub /api/data did not return a profiles array")

    profile = find_profile(profiles, args.profile_id)
    ensure(profile is not None, f"MiSub profile not found: {args.profile_id}")

    existing_sources, existing_server_ips = normalize_existing_sources(misubs, args.source_id_prefix)
    new_sources = build_sources(
        worker_url=args.worker_url,
        access_token=args.access_token,
        server_ips=existing_server_ips,
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

    existing_manual_nodes = profile.get("manualNodes") or []
    filtered_manual_nodes = [
        node_id
        for node_id in existing_manual_nodes
        if not str(node_id).strip().startswith(f"{args.source_id_prefix}_")
    ]
    filtered_manual_nodes.extend(source["id"] for source in new_sources)

    updated_profile = dict(profile)
    updated_profile["manualNodes"] = filtered_manual_nodes

    updated_profiles = []
    for candidate in profiles:
        if candidate is profile or str(candidate.get("customId", "")).strip() == args.profile_id or str(candidate.get("id", "")).strip() == args.profile_id:
            updated_profiles.append(updated_profile)
        else:
            updated_profiles.append(candidate)

    update_payload = {
        "misubs": updated_misubs,
        "profiles": updated_profiles,
    }
    update_response = retry(
        "MiSub profile update",
        10,
        5,
        lambda: session.post(base_url + "api/misubs", json=update_payload, timeout=60),
    )
    update_response.raise_for_status()

    manifest_response = retry(
        "MiSub connector manifest",
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

    sources = manifest_payload.get("sources") or []
    connector_sources = [source for source in sources if str(source.get("kind", "")).strip() == "connector"]
    ensure(connector_sources, "MiSub connector manifest did not return any connector sources")
    for source in connector_sources:
        ensure(str(source.get("input", "")).strip() == args.worker_url, "MiSub connector manifest returned an unexpected worker URL")
        connector_config = source.get("options", {}).get("connector_config") or source.get("connector_config") or {}
        ensure(
            str(connector_config.get("access_token", "")).strip() == args.access_token,
            "MiSub connector manifest returned an outdated access token",
        )

    summary = {
        "profile_id": args.profile_id,
        "worker_url": args.worker_url,
        "source_count": len(new_sources),
        "server_ips": existing_server_ips,
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
