#!/usr/bin/env python3
"""Counted HTTP origin/proxy fixture for the isolated Local Server E2E."""

from __future__ import annotations

import argparse
import http.client
import json
import select
import signal
import socket
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlsplit


HOP_BY_HOP_HEADERS = {
    "connection",
    "keep-alive",
    "proxy-authenticate",
    "proxy-authorization",
    "te",
    "trailers",
    "transfer-encoding",
    "upgrade",
}


def emit(event: str, **fields: object) -> None:
    payload = {"event": event, "ts": time.time(), **fields}
    print(json.dumps(payload, ensure_ascii=True, sort_keys=True), flush=True)


def parse_listen(value: str) -> tuple[str, int]:
    text = value.strip()
    if text.startswith("["):
        host, _, port = text[1:].partition("]:")
    else:
        host, separator, port = text.rpartition(":")
        if not separator:
            raise ValueError(f"listen address must be host:port, got {value!r}")
    if not host or not port.isdigit():
        raise ValueError(f"listen address must be host:port, got {value!r}")
    return host, int(port)


class Counter:
    def __init__(self) -> None:
        self._lock = threading.Lock()
        self._targets: dict[str, int] = {}
        self._methods: dict[str, int] = {"CONNECT": 0, "HTTP": 0}

    def increment(self, target: str, method: str) -> None:
        with self._lock:
            self._targets[target] = self._targets.get(target, 0) + 1
            self._methods[method] = self._methods.get(method, 0) + 1
        emit("target_hit", target=target, method=method)

    def reset(self) -> None:
        with self._lock:
            self._targets.clear()
            for method in list(self._methods):
                self._methods[method] = 0

    def snapshot(self) -> dict[str, object]:
        with self._lock:
            return {"targets": dict(self._targets), "methods": dict(self._methods)}


class OriginHandler(BaseHTTPRequestHandler):
    server: ThreadingHTTPServer

    def do_GET(self) -> None:  # noqa: N802
        if self.path == "/counter":
            payload = json.dumps(self.server.counter.snapshot(), ensure_ascii=True).encode("utf-8")
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(payload)))
            self.send_header("Connection", "close")
            self.end_headers()
            self.wfile.write(payload)
            return
        self.server.counter.increment(self.server.fixture_name, "HTTP")
        payload = json.dumps(
            {"name": self.server.fixture_name, "path": self.path},
            ensure_ascii=True,
        ).encode("utf-8")
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.send_header("Connection", "close")
        self.end_headers()
        self.wfile.write(payload)

    do_HEAD = do_GET

    def do_POST(self) -> None:  # noqa: N802
        if self.path != "/counter/reset":
            self.send_error(404)
            return
        self.server.counter.reset()
        self.send_response(204)
        self.send_header("Connection", "close")
        self.end_headers()

    def log_message(self, format: str, *args: object) -> None:
        emit("origin_request", name=self.server.fixture_name, message=format % args)


class CounterHandler(BaseHTTPRequestHandler):
    server: ThreadingHTTPServer

    def do_GET(self) -> None:  # noqa: N802
        if self.path != "/counter":
            self.send_error(404)
            return
        payload = json.dumps(self.server.counter.snapshot(), ensure_ascii=True).encode("utf-8")
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.send_header("Connection", "close")
        self.end_headers()
        self.wfile.write(payload)

    def do_POST(self) -> None:  # noqa: N802
        if self.path != "/counter/reset":
            self.send_error(404)
            return
        self.server.counter.reset()
        self.send_response(204)
        self.send_header("Connection", "close")
        self.end_headers()

    def log_message(self, format: str, *args: object) -> None:
        emit("counter_request", message=format % args)


class ProxyHandler(BaseHTTPRequestHandler):
    server: ThreadingHTTPServer

    def do_CONNECT(self) -> None:  # noqa: N802
        target = self.path.strip()
        try:
            host, port = parse_listen(target)
            upstream = socket.create_connection((host, port), timeout=10)
        except Exception as exc:  # pragma: no cover - exercised by E2E failures
            self.send_error(502, str(exc))
            return

        self.server.counter.increment(target, "CONNECT")
        self.send_response(200, "Connection Established")
        self.end_headers()
        self.connection.settimeout(30)
        upstream.settimeout(30)
        self._tunnel(upstream)

    def do_GET(self) -> None:  # noqa: N802
        self._forward_http()

    def do_POST(self) -> None:  # noqa: N802
        self._forward_http()

    def do_HEAD(self) -> None:  # noqa: N802
        self._forward_http()

    def _forward_http(self) -> None:
        parsed = urlsplit(self.path)
        if not parsed.hostname:
            self.send_error(400, "absolute-form URL required")
            return
        port = parsed.port or (443 if parsed.scheme == "https" else 80)
        target = f"{parsed.hostname}:{port}"
        path = parsed.path or "/"
        if parsed.query:
            path += "?" + parsed.query
        body = b""
        content_length = self.headers.get("Content-Length")
        if content_length:
            body = self.rfile.read(int(content_length))
        headers = {
            key: value
            for key, value in self.headers.items()
            if key.lower() not in HOP_BY_HOP_HEADERS and key.lower() != "host"
        }
        headers["Host"] = parsed.netloc
        try:
            upstream = http.client.HTTPConnection(parsed.hostname, port, timeout=10)
            upstream.request(self.command, path, body=body, headers=headers)
            response = upstream.getresponse()
            payload = response.read()
        except Exception as exc:  # pragma: no cover - exercised by E2E failures
            self.send_error(502, str(exc))
            return
        finally:
            try:
                upstream.close()
            except UnboundLocalError:
                pass

        self.server.counter.increment(target, "HTTP")
        self.send_response(response.status, response.reason)
        for key, value in response.getheaders():
            if key.lower() not in HOP_BY_HOP_HEADERS:
                self.send_header(key, value)
        self.send_header("Connection", "close")
        self.end_headers()
        self.wfile.write(payload)

    def _tunnel(self, upstream: socket.socket) -> None:
        client = self.connection
        sockets = [client, upstream]
        try:
            try:
                while True:
                    readable, _, _ = select.select(sockets, [], [], 30)
                    if not readable:
                        break
                    for source in readable:
                        data = source.recv(65536)
                        if not data:
                            return
                        destination = upstream if source is client else client
                        destination.sendall(data)
            except (ConnectionAbortedError, ConnectionResetError, BrokenPipeError, OSError):
                return
        finally:
            upstream.close()

    def log_message(self, format: str, *args: object) -> None:
        emit("proxy_request", message=format % args)


def make_server(server_class, handler_class, listen: str, **attributes):
    host, port = parse_listen(listen)
    server = server_class((host, port), handler_class)
    for key, value in attributes.items():
        setattr(server, key, value)
    return server


def run_servers(servers: list[ThreadingHTTPServer], stop: threading.Event) -> None:
    threads = [threading.Thread(target=server.serve_forever, daemon=True) for server in servers]
    for thread in threads:
        thread.start()
    try:
        stop.wait()
    finally:
        for server in servers:
            server.shutdown()
            server.server_close()
        for thread in threads:
            thread.join(timeout=5)


def start_fixture(kind: str, listen: str, name: str = "direct", counter_listen: str = ""):
    stop = threading.Event()
    counter = Counter()
    if kind == "origin":
        server = make_server(ThreadingHTTPServer, OriginHandler, listen, fixture_name=name, counter=counter)
        servers = [server]
    elif kind == "proxy":
        proxy = make_server(ThreadingHTTPServer, ProxyHandler, listen, counter=counter)
        counter_server = make_server(ThreadingHTTPServer, CounterHandler, counter_listen, counter=counter)
        servers = [proxy, counter_server]
    else:
        raise ValueError(f"unknown fixture kind: {kind}")

    signal.signal(signal.SIGTERM, lambda *_: stop.set())
    signal.signal(signal.SIGINT, lambda *_: stop.set())
    emit("fixture_ready", kind=kind, listen=listen, counter_listen=counter_listen or None)
    run_servers(servers, stop)


def self_test() -> None:
    origin_counter = Counter()
    origin = make_server(
        ThreadingHTTPServer,
        OriginHandler,
        "127.0.0.1:0",
        fixture_name="direct",
        counter=origin_counter,
    )
    origin_thread = threading.Thread(target=origin.serve_forever, daemon=True)
    origin_thread.start()
    origin_port = origin.server_address[1]
    counter = Counter()
    proxy = make_server(ThreadingHTTPServer, ProxyHandler, "127.0.0.1:0", counter=counter)
    counter_server = make_server(ThreadingHTTPServer, CounterHandler, "127.0.0.1:0", counter=counter)
    servers = [proxy, counter_server]
    threads = [threading.Thread(target=server.serve_forever, daemon=True) for server in servers]
    for thread in threads:
        thread.start()
    proxy_port = proxy.server_address[1]
    target = f"127.0.0.1:{origin_port}"
    try:
        connection = http.client.HTTPConnection("127.0.0.1", proxy_port, timeout=10)
        connection.request("GET", f"http://{target}/absolute")
        response = connection.getresponse()
        if response.status != 200:
            raise AssertionError(f"absolute-form status = {response.status}")
        response.read()
        connection.close()

        tunnel = socket.create_connection(("127.0.0.1", proxy_port), timeout=10)
        tunnel.sendall(f"CONNECT {target} HTTP/1.1\r\nHost: {target}\r\n\r\n".encode("ascii"))
        header = tunnel.recv(4096)
        if b" 200 " not in header:
            raise AssertionError(f"CONNECT response = {header!r}")
        tunnel.sendall(b"GET /connect HTTP/1.1\r\nHost: direct\r\nConnection: close\r\n\r\n")
        response_bytes = tunnel.recv(65536)
        if b"200" not in response_bytes:
            raise AssertionError(f"tunnel response = {response_bytes!r}")
        tunnel.close()

        snapshot = counter.snapshot()
        if snapshot["methods"] != {"CONNECT": 1, "HTTP": 1}:
            raise AssertionError(f"counter methods = {snapshot['methods']!r}")
        if snapshot["targets"].get(target) != 2:
            raise AssertionError(f"counter targets = {snapshot['targets']!r}")
        if origin_counter.snapshot()["methods"]["HTTP"] != 2:
            raise AssertionError(f"origin counter = {origin_counter.snapshot()!r}")
        print(json.dumps({"connect": 1, "http": 1, "target": target}, sort_keys=True))
    finally:
        for server in servers + [origin]:
            server.shutdown()
            server.server_close()
        for thread in threads + [origin_thread]:
            thread.join(timeout=5)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--self-test", action="store_true")
    subparsers = parser.add_subparsers(dest="kind")
    origin = subparsers.add_parser("origin")
    origin.add_argument("--listen", required=True)
    origin.add_argument("--name", default="direct")
    proxy = subparsers.add_parser("proxy")
    proxy.add_argument("--listen", required=True)
    proxy.add_argument("--counter-listen", required=True)
    args = parser.parse_args()
    if args.self_test:
        self_test()
        return 0
    if args.kind == "origin":
        start_fixture("origin", args.listen, name=args.name)
        return 0
    if args.kind == "proxy":
        start_fixture("proxy", args.listen, counter_listen=args.counter_listen)
        return 0
    parser.error("choose origin, proxy, or --self-test")
    return 2


if __name__ == "__main__":
    raise SystemExit(main())
