#!/usr/bin/env python3

from __future__ import annotations

import argparse
import os
import sys
import time
from typing import Any
from urllib.parse import unquote, urlparse

import requests


def ensure(condition: bool, message: str) -> None:
    if not condition:
        raise RuntimeError(message)


def fetch_json(session: requests.Session, url: str, headers: dict[str, str]) -> dict[str, Any]:
    response = session.get(url, headers=headers, timeout=30)
    response.raise_for_status()
    payload = response.json()
    ensure(isinstance(payload, dict), f"{url} did not return a JSON object")
    return payload


def synchronized(
    status: dict[str, Any],
    settings: dict[str, Any],
    connectors: dict[str, Any],
    worker_url: str,
    token: str,
    profile_id: str,
) -> bool:
    if not status.get("manifest_healthy") or status.get("last_error"):
        return False
    if int(status.get("connector_instance_count") or 0) < 1:
        return False
    manifest_path = unquote(urlparse(str(settings.get("source_sync_manifest_url") or "")).path)
    if not manifest_path.rstrip("/").endswith(f"/api/manifest/{profile_id}"):
        return False
    values = connectors.get("connectors")
    if not isinstance(values, list):
        return False
    ech = [item for item in values if isinstance(item, dict) and item.get("connector_type") == "ech_worker"]
    return any(
        str(item.get("input") or "").strip() == worker_url
        and isinstance(item.get("connector_config"), dict)
        and str(item["connector_config"].get("access_token") or "").strip() == token
        for item in ech
    )


def main() -> int:
    parser = argparse.ArgumentParser(
        description="Verify that a dedicated EasyProxy validation instance synchronized an ECH connector."
    )
    parser.add_argument("--base-url", required=True)
    parser.add_argument("--proxy-url", required=True)
    parser.add_argument("--worker-url", required=True)
    parser.add_argument("--profile-id", required=True)
    parser.add_argument("--target-url", default="http://example.com/")
    parser.add_argument("--target-text", default="Example Domain")
    parser.add_argument("--wait-seconds", type=int, default=600)
    args = parser.parse_args()

    password = os.environ.get("EASYPROXY_MANAGEMENT_PASSWORD", "").strip()
    token = os.environ.get("ECH_TOKEN", "").strip()
    ensure(password != "", "EASYPROXY_MANAGEMENT_PASSWORD is required")
    ensure(token != "", "ECH_TOKEN is required")
    ensure(args.wait_seconds > 0, "--wait-seconds must be positive")

    base_url = args.base_url.rstrip("/")
    headers = {"Authorization": f"Bearer {password}"}
    session = requests.Session()
    deadline = time.monotonic() + args.wait_seconds
    last_status: dict[str, Any] = {}
    while time.monotonic() < deadline:
        last_status = fetch_json(session, base_url + "/api/source-sync/status", headers)
        settings = fetch_json(session, base_url + "/api/settings", headers)
        connectors = fetch_json(session, base_url + "/api/connectors/config", headers)
        if synchronized(last_status, settings, connectors, args.worker_url, token, args.profile_id):
            break
        time.sleep(10)
    else:
        summary = {
            "manifest_healthy": bool(last_status.get("manifest_healthy")),
            "last_error_set": bool(last_status.get("last_error")),
            "connector_instance_count": int(last_status.get("connector_instance_count") or 0),
        }
        raise RuntimeError(f"EasyProxy did not synchronize the candidate ECH connector: {summary}")

    response = requests.get(
        args.target_url,
        proxies={"http": args.proxy_url, "https": args.proxy_url},
        timeout=60,
    )
    response.raise_for_status()
    ensure(response.status_code == 200, "EasyProxy validation target did not return HTTP 200")
    ensure(args.target_text in response.text, "EasyProxy validation target marker was not found")
    print("verified EasyProxy ECH connector sync and real proxy traffic")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as exc:  # pragma: no cover - CLI failure path
        print(str(exc), file=sys.stderr)
        raise SystemExit(1)
