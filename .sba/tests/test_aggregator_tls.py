"""真实本地 TLS/CONNECT 正反例；临时证书仅生成在 pytest 临时目录。"""
from contextlib import contextmanager
import importlib.util
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import os
from pathlib import Path
import select
import shutil
import socket
import socketserver
import ssl
import subprocess
import sys
import threading

import pytest

from test_aggregator_runtime import ROOT, approved_utils
import aggregator_runtime as runtime


@pytest.fixture(scope="module")
def certificates(tmp_path_factory):
    root = tmp_path_factory.mktemp("tls-certificates")
    openssl = shutil.which("openssl") or str(Path.home() / "scoop/apps/git/current/usr/bin/openssl.exe")
    assert Path(openssl).is_file(), "An explicit local OpenSSL is required for real TLS regression"
    for name in ("server", "untrusted-root"):
        result = subprocess.run([openssl, "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "1",
            "-subj", "/CN=localhost", "-addext", "subjectAltName=DNS:localhost",
            "-keyout", str(root / (name + ".key")), "-out", str(root / (name + ".pem"))],
            cwd=root, stdin=subprocess.DEVNULL, capture_output=True, timeout=30)
        assert result.returncode == 0, result.stderr.decode(errors="replace")
    return root


@pytest.fixture
def https_server(certificates):
    class Handler(BaseHTTPRequestHandler):
        def do_GET(self):
            self.send_response(200)
            self.send_header("Content-Length", "10")
            self.end_headers()
            self.wfile.write(b"TLS_VALID\n")

        def log_message(self, *args):
            pass

    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    context.load_cert_chain(certificates / "server.pem", certificates / "server.key")
    server.socket = context.wrap_socket(server.socket, server_side=True)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        yield server.server_port
    finally:
        server.shutdown()
        server.server_close()
        thread.join(5)


@contextmanager
def connect_proxy(port):
    seen = []

    class Handler(socketserver.StreamRequestHandler):
        def handle(self):
            line = self.rfile.readline(4096).decode("ascii")
            method, authority, _ = line.split()
            assert method == "CONNECT" and authority in (f"localhost:{port}", f"127.0.0.1:{port}")
            while self.rfile.readline(4096) not in (b"\r\n", b"\n", b""):
                pass
            seen.append(authority)
            with socket.create_connection(("127.0.0.1", port), timeout=3) as target:
                self.wfile.write(b"HTTP/1.1 200 Connection established\r\n\r\n")
                self.wfile.flush()
                try:
                    while True:
                        ready, _, _ = select.select([self.connection, target], [], [], 5)
                        if not ready:
                            return
                        for incoming in ready:
                            body = incoming.recv(65536)
                            if not body:
                                return
                            (target if incoming is self.connection else self.connection).sendall(body)
                except (OSError, TimeoutError):
                    return

    class Server(socketserver.ThreadingTCPServer):
        daemon_threads = True

    server = Server(("127.0.0.1", 0), Handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        yield "http://127.0.0.1:" + str(server.server_address[1]), seen
    finally:
        server.shutdown()
        server.server_close()
        thread.join(5)


@pytest.mark.parametrize("trusted,host,accepted", [(True, "localhost", True), (False, "localhost", False),
                                                  (True, "127.0.0.1", False)])
def test_actual_patched_source_https_validates_trust_and_hostname(tmp_path, monkeypatch, certificates,
                                                               https_server, trusted, host, accepted):
    approved_utils(tmp_path)
    for name in ("logger.py", "urlvalidator.py"):
        shutil.copyfile(ROOT / "upstreams/aggregator/subscribe" / name, tmp_path / "subscribe" / name)
    runtime.patch_source_tls(tmp_path.resolve())
    monkeypatch.setenv("SSL_CERT_FILE", str(certificates / ("server.pem" if trusted else "untrusted-root.pem")))
    for name in ("HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"):
        monkeypatch.delenv(name, raising=False)
    monkeypatch.syspath_prepend(str(tmp_path / "subscribe"))
    spec = importlib.util.spec_from_file_location("sba_tls_fixture_utils", tmp_path / "subscribe/utils.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    assert module.CTX.check_hostname and module.CTX.verify_mode == ssl.CERT_REQUIRED
    body = module.http_get(f"https://{host}:{https_server}/", retry=1, timeout=2)
    assert (body == "TLS_VALID\n") is accepted


@pytest.mark.parametrize("trusted,host,accepted", [(True, "localhost", True), (False, "localhost", False),
                                                  (True, "127.0.0.1", False)])
def test_actual_container_probe_checks_target_tls_inside_connect(monkeypatch, certificates, https_server, trusted, host, accepted):
    monkeypatch.syspath_prepend(str(ROOT))
    from scripts.easyproxy_source_audit_probe import CONTAINER_PROXY_PROBE
    env = {"PATH": os.defpath, "NO_PROXY": "never-bypass.invalid",
           "SSL_CERT_FILE": str(certificates / ("server.pem" if trusted else "untrusted-root.pem"))}
    if os.name == "nt":
        env["SYSTEMROOT"] = os.environ["SYSTEMROOT"]
    with connect_proxy(https_server) as (proxy, seen):
        result = subprocess.run([sys.executable, "-I", "-c", CONTAINER_PROXY_PROBE, proxy,
                                f"https://{host}:{https_server}/", "3"], env=env, capture_output=True, timeout=10)
    assert seen, (result.returncode, result.stdout, result.stderr)
    assert (result.returncode == 0 and result.stdout.strip() == b"200") is accepted, result.stderr
    if not accepted:
        assert result.returncode == 7


@pytest.mark.parametrize("trusted,host,accepted", [(True, "localhost", True), (False, "localhost", False),
                                                  (True, "127.0.0.1", False)])
def test_actual_host_curl_probe_checks_target_tls(monkeypatch, certificates, https_server, trusted, host, accepted):
    monkeypatch.syspath_prepend(str(ROOT))
    from scripts import easyproxy_source_audit_probe as probe
    curl = str(Path.home() / "scoop/apps/git/current/mingw64/bin/curl.exe") if os.name == "nt" else shutil.which("curl")
    assert curl and Path(curl).is_file(), "An explicit local curl is required for real TLS regression"
    monkeypatch.setattr(probe.shutil, "which", lambda *_: curl)
    cafile = str(certificates / ("server.pem" if trusted else "untrusted-root.pem"))
    # Schannel ignores CURL_CA_BUNDLE. Give the real executable the fixture trust
    # store explicitly, without changing machine trust or bypassing verification.
    actual_run = subprocess.run

    def run_with_fixture_ca(args, **kwargs):
        return actual_run([*args[:-1], "--cacert", cafile, args[-1]], **kwargs)

    monkeypatch.setattr(probe.subprocess, "run", run_with_fixture_ca)
    monkeypatch.setenv("NO_PROXY", "")
    monkeypatch.setenv("no_proxy", "")
    policy = {"request_timeout_seconds": 3, "accept_http_status_below_500": False,
              "http_probe_targets": [{"url": f"https://{host}:{https_server}/", "expected_status": [200]}]}
    with connect_proxy(https_server) as (proxy, seen):
        result = probe.probe_http_proxy(proxy, policy)
    assert seen, "curl must actually traverse the local CONNECT fixture"
    assert result["ok"] is accepted, result["attempts"][0].get("stderr", result)
    assert len(result["attempts"]) == 1
