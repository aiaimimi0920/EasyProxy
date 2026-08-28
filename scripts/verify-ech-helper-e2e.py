#!/usr/bin/env python3

from __future__ import annotations

import argparse
import os
import socket
import subprocess
import sys
import tempfile
import time
from pathlib import Path
from urllib.parse import urlparse


def ensure(condition: bool, message: str) -> None:
    if not condition:
        raise RuntimeError(message)


def worker_address(value: str) -> str:
    parsed = urlparse(value if "://" in value else f"https://{value}")
    ensure(parsed.scheme in ("http", "https"), "Worker URL must use http or https")
    ensure(bool(parsed.hostname), "Worker URL has no hostname")
    ensure(parsed.path in ("", "/"), "Worker URL must not contain a path")
    port = parsed.port or (443 if parsed.scheme == "https" else 80)
    return f"{parsed.hostname}:{port}"


def reserve_port() -> int:
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        return int(listener.getsockname()[1])


def wait_for_listener(port: int, process: subprocess.Popen[bytes], timeout: float = 30) -> None:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        ensure(process.poll() is None, f"ECH helper exited with code {process.returncode}")
        try:
            with socket.create_connection(("127.0.0.1", port), timeout=1):
                return
        except OSError:
            time.sleep(0.25)
    raise RuntimeError("ECH helper did not open its local proxy listener")


def read_http_response(connection: socket.socket) -> bytes:
    response = bytearray()
    while len(response) < 1024 * 1024:
        block = connection.recv(65536)
        if not block:
            break
        response.extend(block)
        if b"\r\n\r\n" in response:
            break
    ensure(response.startswith(b"HTTP/1."), "Proxy did not return an HTTP response")
    return bytes(response)


def recv_exact(connection: socket.socket, size: int) -> bytes:
    value = bytearray()
    while len(value) < size:
        block = connection.recv(size - len(value))
        ensure(bool(block), "Proxy closed before completing a protocol response")
        value.extend(block)
    return bytes(value)


def verify_http_proxy(port: int, target_host: str) -> None:
    request = (
        f"GET http://{target_host}/ HTTP/1.1\r\n"
        f"Host: {target_host}\r\nConnection: close\r\n\r\n"
    ).encode("ascii")
    with socket.create_connection(("127.0.0.1", port), timeout=10) as connection:
        connection.settimeout(30)
        connection.sendall(request)
        read_http_response(connection)


def verify_socks5_proxy(port: int, target_host: str, target_port: int) -> None:
    encoded_host = target_host.encode("idna")
    ensure(len(encoded_host) <= 255, "SOCKS5 target hostname is too long")
    with socket.create_connection(("127.0.0.1", port), timeout=10) as connection:
        connection.settimeout(30)
        connection.sendall(b"\x05\x01\x00")
        ensure(recv_exact(connection, 2) == b"\x05\x00", "SOCKS5 authentication negotiation failed")
        connection.sendall(
            b"\x05\x01\x00\x03"
            + bytes([len(encoded_host)])
            + encoded_host
            + target_port.to_bytes(2, "big")
        )
        reply = recv_exact(connection, 10)
        ensure(reply[:4] == b"\x05\x00\x00\x01", "SOCKS5 CONNECT failed")
        connection.sendall(
            f"GET / HTTP/1.1\r\nHost: {target_host}\r\nConnection: close\r\n\r\n".encode("ascii")
        )
        read_http_response(connection)


def main() -> int:
    parser = argparse.ArgumentParser(description="Verify the Go ECH helper through HTTP and SOCKS5 real traffic.")
    parser.add_argument("--helper", required=True, type=Path)
    parser.add_argument("--worker-url", required=True)
    parser.add_argument("--server-ip", default="")
    parser.add_argument("--proxy-ip", default="")
    parser.add_argument("--target-host", default="example.com")
    parser.add_argument("--target-port", type=int, default=80)
    args = parser.parse_args()

    token = os.environ.get("ECH_TOKEN", "").strip()
    ensure(token != "", "ECH_TOKEN is required")
    ensure(args.helper.is_file(), f"ECH helper does not exist: {args.helper}")

    port = reserve_port()
    command = [
        str(args.helper),
        "-l",
        f"127.0.0.1:{port}",
        "-f",
        worker_address(args.worker_url),
    ]
    if args.server_ip:
        command.extend(["-ip", args.server_ip])
    if args.proxy_ip:
        command.extend(["-pyip", args.proxy_ip])

    environment = os.environ.copy()
    environment["ECH_TOKEN"] = token
    with tempfile.TemporaryFile() as log:
        process = subprocess.Popen(command, env=environment, stdout=log, stderr=subprocess.STDOUT)
        try:
            wait_for_listener(port, process)
            verify_http_proxy(port, args.target_host)
            verify_socks5_proxy(port, args.target_host, args.target_port)
        except Exception as exc:
            log.seek(0)
            output = log.read(16 * 1024).decode("utf-8", errors="replace")
            raise RuntimeError(f"{exc}\nECH helper log:\n{output}") from exc
        finally:
            process.terminate()
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=5)

    print("verified ECH helper HTTP and SOCKS5 tunnels")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as exc:  # pragma: no cover - CLI failure path
        print(str(exc), file=sys.stderr)
        raise SystemExit(1)
