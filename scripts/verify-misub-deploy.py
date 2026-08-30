#!/usr/bin/env python3

from __future__ import annotations

import argparse
import json
import os
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


def get_with_retry(
    session: requests.Session,
    label: str,
    url: str,
    *,
    attempts: int = 10,
    delay_seconds: float = 5,
    **kwargs,
) -> requests.Response:
    def request() -> requests.Response:
        response = session.get(url, **kwargs)
        response.raise_for_status()
        return response

    return retry(label, attempts, delay_seconds, request)


def main() -> int:
    parser = argparse.ArgumentParser(description="Verify MiSub Pages deployment by checking public and authenticated API routes.")
    parser.add_argument("--base-url", required=True)
    parser.add_argument("--manifest-profile-id", default="default")
    args = parser.parse_args()

    admin_password = os.environ.get("MISUB_ADMIN_PASSWORD", "")
    manifest_token = os.environ.get("MISUB_MANIFEST_TOKEN", "")
    ensure(admin_password != "", "MISUB_ADMIN_PASSWORD is required")
    ensure(manifest_token != "", "MISUB_MANIFEST_TOKEN is required")

    base_url = args.base_url.rstrip("/") + "/"
    session = requests.Session()

    root = get_with_retry(session, "MiSub root page", base_url, timeout=30)
    ensure("html" in root.text.lower(), "MiSub root page did not return HTML content")

    public_config_url = urljoin(base_url, "api/public_config")
    public_config = get_with_retry(session, "MiSub public config", public_config_url, timeout=30)
    try:
        public_payload = public_config.json()
    except json.JSONDecodeError as exc:
        raise RuntimeError(f"MiSub public config did not return JSON: {public_config_url}") from exc
    ensure(isinstance(public_payload, dict), "MiSub public config endpoint did not return a JSON object")

    login_url = urljoin(base_url, "api/login")
    def login_request():
        response = session.post(
            login_url,
            json={"password": admin_password},
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
    settings = get_with_retry(session, "MiSub settings", settings_url, timeout=30)
    try:
        settings_payload = settings.json()
    except json.JSONDecodeError as exc:
        raise RuntimeError(f"MiSub settings did not return JSON: {settings_url}") from exc
    ensure(isinstance(settings_payload, dict), "MiSub settings endpoint did not return JSON")
    ensure("mytoken" in settings_payload, "MiSub settings payload is missing expected keys")

    manifest = get_with_retry(
        session,
        "MiSub manifest",
        urljoin(base_url, f"api/manifest/{args.manifest_profile_id}"),
        headers={"Authorization": f"Bearer {manifest_token}"},
        timeout=30,
    )
    if manifest.status_code != 200:
        raise RuntimeError(f"Unexpected manifest response status: {manifest.status_code}")
    payload = manifest.json()
    ensure(payload.get("success") is True, "MiSub manifest endpoint did not report success")
    sources = payload.get("sources") or []
    ensure(isinstance(sources, list), "MiSub manifest payload does not contain a sources array")
    ensure(len(sources) > 0, "MiSub manifest payload did not return any sources")

    cron_url = urljoin(base_url, "api/cron/status")
    cron_status = get_with_retry(session, "MiSub cron status", cron_url, timeout=30)
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
