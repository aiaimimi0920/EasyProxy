#!/usr/bin/env python3

from __future__ import annotations

import argparse
import base64
import os
import sys
import time
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


def open_websocket(ws_url: str, token: str):
    return websockets.sync.client.connect(
        ws_url,
        subprotocols=[token],
        open_timeout=20,
        close_timeout=5,
    )


def verify_token_accepted(ws_url: str, token: str, label: str) -> None:
    websocket = retry(label, 10, 5, lambda: open_websocket(ws_url, token))
    with websocket:
        ensure(websocket.subprotocol == token, f"{label} did not echo the expected subprotocol")


def verify_token_rejected(ws_url: str, token: str, label: str) -> None:
    try:
        websocket = open_websocket(ws_url, token)
    except Exception as exc:
        status = getattr(exc, "status_code", None)
        response = getattr(exc, "response", None)
        if status is None and response is not None:
            status = getattr(response, "status_code", None)
        ensure(status == 401, f"{label} failed for an unexpected reason: {exc}")
        return
    websocket.close()
    raise RuntimeError(f"{label} unexpectedly established a WebSocket")


def verify_tcp_tunnel(ws_url: str, token: str, target: str, host_header: str) -> None:
    request = (
        f"GET / HTTP/1.1\r\nHost: {host_header}\r\n"
        "Connection: close\r\nUser-Agent: EasyProxy-ECH-Verify\r\n\r\n"
    ).encode("ascii")
    command = f"CONNECT:{target}|{base64.b64encode(request).decode('ascii')}|"
    websocket = retry("ECH worker TCP tunnel", 5, 5, lambda: open_websocket(ws_url, token))
    response = bytearray()
    with websocket:
        websocket.send(command)
        deadline = time.monotonic() + 30
        while time.monotonic() < deadline:
            message = websocket.recv(timeout=max(0.1, deadline - time.monotonic()))
            if message == "CONNECTED":
                continue
            if isinstance(message, str):
                ensure(not message.startswith("ERROR:"), message)
                if message == "CLOSE":
                    break
                response.extend(message.encode("utf-8"))
            else:
                response.extend(message)
            if b"HTTP/1." in response and b"\r\n\r\n" in response:
                break
    ensure(response.startswith(b"HTTP/1."), "ECH worker tunnel did not return an HTTP response")


def main() -> int:
    parser = argparse.ArgumentParser(description="Verify ech-workers-cloudflare deployment with HTTP and WebSocket probes.")
    parser.add_argument("--base-url", required=True)
    parser.add_argument("--tunnel-target", default="example.com:80")
    parser.add_argument("--tunnel-host", default="example.com")
    parser.add_argument(
        "--previous-token",
        choices=("skip", "accepted", "rejected"),
        default="skip",
        help="Expected state for ECH_TOKEN_PREVIOUS during a rotation.",
    )
    args = parser.parse_args()

    token = os.environ.get("ECH_TOKEN", "")
    ensure(token != "", "ECH_TOKEN is required")

    response = retry("ECH worker HTTP probe", 10, 5, lambda: requests.get(args.base_url, timeout=30))
    response.raise_for_status()
    ensure("WebSocket Proxy Server" in response.text, "Worker root response did not match expected banner")

    ws_url = to_websocket_url(args.base_url)
    verify_token_accepted(ws_url, token, "ECH worker current-token probe")
    verify_tcp_tunnel(ws_url, token, args.tunnel_target, args.tunnel_host)

    rejected_token = "easyproxy-invalid-token"
    while rejected_token in {token, os.environ.get("ECH_TOKEN_PREVIOUS", "")}:
        rejected_token += "-x"
    verify_token_rejected(ws_url, rejected_token, "ECH worker invalid-token probe")

    if args.previous_token != "skip":
        previous = os.environ.get("ECH_TOKEN_PREVIOUS", "")
        ensure(previous != "", "ECH_TOKEN_PREVIOUS is required for previous-token verification")
        if args.previous_token == "accepted":
            verify_token_accepted(ws_url, previous, "ECH worker previous-token probe")
            verify_tcp_tunnel(ws_url, previous, args.tunnel_target, args.tunnel_host)
        else:
            verify_token_rejected(ws_url, previous, "ECH worker revoked-token probe")

    print(f"verified {args.base_url}")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as exc:  # pragma: no cover - CLI failure path
        print(str(exc), file=sys.stderr)
        raise SystemExit(1)
