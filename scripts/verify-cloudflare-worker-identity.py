#!/usr/bin/env python3

from __future__ import annotations

import argparse
import os
import sys

import requests


def ensure(condition: bool, message: str) -> None:
    if not condition:
        raise RuntimeError(message)


def auth_headers() -> dict[str, str]:
    token = os.environ.get("CLOUDFLARE_API_TOKEN", "").strip()
    if token:
        return {"Authorization": f"Bearer {token}"}
    email = os.environ.get("CLOUDFLARE_EMAIL", "").strip()
    api_key = os.environ.get("CLOUDFLARE_API_KEY", "").strip()
    ensure(email != "" and api_key != "", "Cloudflare API credentials are required")
    return {"X-Auth-Email": email, "X-Auth-Key": api_key}


def verify_identity(session, account_id: str, worker_name: str, mode: str) -> bool:
    endpoint = f"https://api.cloudflare.com/client/v4/accounts/{account_id}/workers/scripts/{worker_name}/settings"
    response = session.get(endpoint, headers=auth_headers(), timeout=30)
    if response.status_code == 404:
        ensure(mode == "bootstrap", f"Worker {worker_name!r} does not exist; update refuses to create it")
        return False
    response.raise_for_status()
    payload = response.json()
    ensure(payload.get("success") is True, "Cloudflare Worker identity lookup did not report success")
    return True


def main() -> int:
    parser = argparse.ArgumentParser(description="Verify exact Cloudflare Worker identity before deployment.")
    parser.add_argument("--account-id", required=True)
    parser.add_argument("--worker-name", required=True)
    parser.add_argument("--mode", choices=("bootstrap", "update", "verify"), required=True)
    args = parser.parse_args()

    exists = verify_identity(requests.Session(), args.account_id, args.worker_name, args.mode)
    print(f"worker={args.worker_name} exists={str(exists).lower()} mode={args.mode}")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as exc:  # pragma: no cover - CLI failure path
        print(str(exc), file=sys.stderr)
        raise SystemExit(1)
