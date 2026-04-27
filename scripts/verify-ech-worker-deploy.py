#!/usr/bin/env python3

from __future__ import annotations

import argparse
import time
import sys
from urllib.parse import urlparse, urlunparse

import requests
import websockets.sync.client


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


def to_websocket_url(base_url: str) -> str:
    parsed = urlparse(base_url)
    if parsed.scheme == "https":
        scheme = "wss"
    elif parsed.scheme == "http":
        scheme = "ws"
    else:
        raise RuntimeError(f"Unsupported worker URL scheme: {parsed.scheme}")
    return urlunparse((scheme, parsed.netloc, parsed.path or "/", "", "", ""))


def main() -> int:
    parser = argparse.ArgumentParser(description="Verify ech-workers-cloudflare deployment with HTTP and WebSocket probes.")
    parser.add_argument("--base-url", required=True)
    parser.add_argument("--token", required=True)
    args = parser.parse_args()

    response = retry("ECH worker HTTP probe", 10, 5, lambda: requests.get(args.base_url, timeout=30))
    response.raise_for_status()
    ensure("WebSocket Proxy Server" in response.text, "Worker root response did not match expected banner")

    ws_url = to_websocket_url(args.base_url)
    def open_ws():
        return websockets.sync.client.connect(ws_url, subprotocols=[args.token], open_timeout=20, close_timeout=5)

    websocket = retry("ECH worker WebSocket probe", 10, 5, open_ws)
    with websocket:
        ensure(websocket.subprotocol == args.token, "Worker WebSocket handshake did not echo the expected subprotocol")

    print(f"verified {args.base_url}")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as exc:  # pragma: no cover - CLI failure path
        print(str(exc), file=sys.stderr)
        raise SystemExit(1)
