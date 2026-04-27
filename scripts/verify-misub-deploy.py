#!/usr/bin/env python3

from __future__ import annotations

import argparse
import json
import time
import sys
from urllib.parse import urljoin

import requests


def ensure(condition: bool, message: str) -> None:
    if not condition:
        raise RuntimeError(message)


def retry(label: str, attempts: int, delay_seconds: float, func):
    last_error = None
    for attempt in range(1, attempts + 1):
        try:
            return func()
        except Exception as exc:  # pragma: no cover - retry wrapper
            last_error = exc
            if attempt == attempts:
                break
            time.sleep(delay_seconds)
    raise RuntimeError(f"{label} failed after {attempts} attempts: {last_error}") from last_error


def main() -> int:
    parser = argparse.ArgumentParser(description="Verify MiSub Pages deployment by checking public and authenticated API routes.")
    parser.add_argument("--base-url", required=True)
    parser.add_argument("--admin-password", required=True)
    parser.add_argument("--manifest-token", required=True)
    parser.add_argument("--manifest-profile-id", default="default")
    args = parser.parse_args()

    base_url = args.base_url.rstrip("/") + "/"
    session = requests.Session()

    root = retry("MiSub root page", 10, 5, lambda: session.get(base_url, timeout=30))
    root.raise_for_status()
    ensure("html" in root.text.lower(), "MiSub root page did not return HTML content")

    public_config_url = urljoin(base_url, "api/public_config")
    public_config = retry("MiSub public config", 10, 5, lambda: session.get(public_config_url, timeout=30))
    public_config.raise_for_status()
    try:
        public_payload = public_config.json()
    except json.JSONDecodeError as exc:
        raise RuntimeError(f"MiSub public config did not return JSON: {public_config_url}") from exc
    ensure(isinstance(public_payload, dict), "MiSub public config endpoint did not return a JSON object")

    login_url = urljoin(base_url, "api/login")
    def login_request():
        response = session.post(
            login_url,
            json={"password": args.admin_password},
            timeout=30,
        )
        if response.status_code == 401:
            raise RuntimeError(f"MiSub login returned 401: {login_url}")
        response.raise_for_status()
        return response

    login = retry("MiSub login", 12, 10, login_request)
    try:
        login_payload = login.json()
    except json.JSONDecodeError as exc:
        raise RuntimeError(f"MiSub login did not return JSON: {login_url}") from exc
    ensure(bool(login_payload.get("success")), "MiSub login did not report success")

    settings_url = urljoin(base_url, "api/settings")
    settings = retry("MiSub settings", 10, 5, lambda: session.get(settings_url, timeout=30))
    settings.raise_for_status()
    try:
        settings_payload = settings.json()
    except json.JSONDecodeError as exc:
        raise RuntimeError(f"MiSub settings did not return JSON: {settings_url}") from exc
    ensure(isinstance(settings_payload, dict), "MiSub settings endpoint did not return JSON")
    ensure("mytoken" in settings_payload, "MiSub settings payload is missing expected keys")

    manifest = retry(
        "MiSub manifest",
        10,
        5,
        lambda: session.get(
            urljoin(base_url, f"api/manifest/{args.manifest_profile_id}"),
            headers={"Authorization": f"Bearer {args.manifest_token}"},
            timeout=30,
        ),
    )
    if manifest.status_code not in (200, 404):
        raise RuntimeError(f"Unexpected manifest response status: {manifest.status_code}")
    if manifest.status_code == 200:
        payload = manifest.json()
        ensure(payload.get("success") is True, "MiSub manifest endpoint did not report success")
    else:
        try:
            payload = manifest.json()
        except json.JSONDecodeError:
            payload = {}
        ensure(
            str(payload.get("error", "")).strip() not in ("MANIFEST_TOKEN is not configured", "Unauthorized"),
            "MiSub manifest verification failed due to auth/runtime configuration",
        )

    cron_url = urljoin(base_url, "api/cron/status")
    cron_status = retry("MiSub cron status", 10, 5, lambda: session.get(cron_url, timeout=30))
    cron_status.raise_for_status()
    try:
        cron_payload = cron_status.json()
    except json.JSONDecodeError as exc:
        raise RuntimeError(f"MiSub cron status did not return JSON: {cron_url}") from exc
    ensure(isinstance(cron_payload, dict), "MiSub cron status endpoint did not return JSON")

    print(f"verified {base_url}")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as exc:  # pragma: no cover - CLI failure path
        print(str(exc), file=sys.stderr)
        raise SystemExit(1)
