#!/usr/bin/env python3

from __future__ import annotations

import argparse
import base64
import os
import secrets
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


def build_dns_tcp_query(hostname: str, transaction_id: bytes | None = None) -> tuple[bytes, bytes]:
    transaction_id = transaction_id or secrets.token_bytes(2)
    ensure(len(transaction_id) == 2, "DNS transaction ID must contain two bytes")
    labels = hostname.rstrip(".").encode("idna").split(b".")
    ensure(all(0 < len(label) <= 63 for label in labels), "Invalid DNS tunnel hostname")
    question = b"".join(bytes((len(label),)) + label for label in labels) + b"\x00\x00\x01\x00\x01"
    header = transaction_id + b"\x01\x00\x00\x01\x00\x00\x00\x00\x00\x00"
    packet = header + question
    return len(packet).to_bytes(2, "big") + packet, transaction_id


def dns_tcp_response_complete(response: bytearray) -> bool:
    return len(response) >= 2 and len(response) >= 2 + int.from_bytes(response[:2], "big")


def validate_dns_tcp_response(response: bytes, transaction_id: bytes) -> None:
    ensure(len(response) >= 14, "ECH worker DNS tunnel returned a truncated response")
    packet_length = int.from_bytes(response[:2], "big")
    ensure(packet_length >= 12, "ECH worker DNS tunnel returned an invalid packet length")
    ensure(len(response) >= packet_length + 2, "ECH worker DNS tunnel returned an incomplete packet")
    packet = response[2 : packet_length + 2]
    ensure(packet[:2] == transaction_id, "ECH worker DNS tunnel returned a mismatched transaction")
    flags = int.from_bytes(packet[2:4], "big")
    ensure(flags & 0x8000 != 0, "ECH worker DNS tunnel response is not marked as a response")
    ensure(flags & 0x000F == 0, f"ECH worker DNS tunnel returned DNS error {flags & 0x000F}")


def verify_tcp_tunnel(ws_url: str, token: str, target: str, hostname: str) -> None:
    request, transaction_id = build_dns_tcp_query(hostname)
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
            if dns_tcp_response_complete(response):
                break
    validate_dns_tcp_response(response, transaction_id)


def main() -> int:
    parser = argparse.ArgumentParser(description="Verify ech-workers-cloudflare deployment with HTTP and WebSocket probes.")
    parser.add_argument("--base-url", required=True)
    parser.add_argument("--tunnel-target", default="8.8.8.8:53")
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
