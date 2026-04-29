#!/usr/bin/env python3

from __future__ import annotations

import argparse
import json
import sys

import requests
import yaml


def verify(base_url: str, timeout: int) -> dict[str, object]:
    relay_url = f"{base_url.rstrip('/')}/issue91"
    response = requests.get(relay_url, timeout=timeout)
    response.raise_for_status()
    payload = yaml.safe_load(response.text)
    if not isinstance(payload, dict):
        raise RuntimeError("relay response is not a YAML mapping")
    proxies = payload.get("proxies")
    if not isinstance(proxies, list):
        raise RuntimeError("relay response does not contain a Clash proxies list")
    count = len(proxies)
    if count <= 0:
        raise RuntimeError("relay response contained zero proxies")
    return {
        "relay_url": relay_url,
        "proxy_count": count,
    }


def main() -> int:
    parser = argparse.ArgumentParser(description="Verify the Cloudflare relay for issue91 shared seed.")
    parser.add_argument("--base-url", required=True, help="Base URL of the relay worker, e.g. https://worker.example.workers.dev")
    parser.add_argument("--timeout", type=int, default=60, help="HTTP timeout in seconds")
    args = parser.parse_args()

    result = verify(args.base_url, args.timeout)
    print(json.dumps(result, ensure_ascii=False, indent=2))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as exc:  # pragma: no cover - CLI failure path
        print(str(exc), file=sys.stderr)
        raise SystemExit(1)
