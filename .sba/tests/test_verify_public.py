"""公开实际内容读回而非只信版本端点；无任何登录、写 API 或部署凭据。"""
import copy
import hashlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
from pathlib import Path
import sys
import threading
from urllib.parse import urlsplit

import pytest
import requests

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import verify_public as verification
from aggregator_publish import ARTIFACTS
from artifact_store import ArtifactError
from suite_config import parse_configuration
from test_aggregator_publish import BODIES, URI
from test_coordination import OLD
from test_suite_config import PUBLIC


@pytest.fixture
def wire(monkeypatch):
    config = parse_configuration(PUBLIC, repository="fixture/EasyProxy", environment="testing")
    run = "1" * 32
    bodies = {**BODIES, "subs/effective.txt": (URI + "\n").encode(),
              "subs/effective.json": json.dumps({"count": 1, "stable_available_uris": [URI]}).encode()}
    manifest = {"schema_version": 1, "run_id": run, "generated_at": "2026-10-26T00:00:00+00:00",
        "root_commit": OLD["sourceSha"], "application_version": OLD["applicationVersion"], "aggregator_commit": "e" * 40,
        "previous_run_id": "", "counts": {"nodes": 1, "sources": 1},
        "artifacts": [{"name": name, "stable_key": key, "release_key": f"releases/{run}/{key}",
                       "sha256": hashlib.sha256(bodies[key]).hexdigest(), "size": len(bodies[key])} for name, key in ARTIFACTS.items()]}
    record = {"config": config, "manifest": manifest, "bodies": bodies, "requests": [], "mode": "normal", "stableReads": 0}
    release = {"schemaVersion": 1, "applicationId": "easyproxy-cloud", **OLD}

    class Handler(BaseHTTPRequestHandler):
        protocol_version = "HTTP/1.1"

        def log_message(self, *_):
            pass

        def do_GET(self):
            record["requests"].append((self.command, self.path))
            status = 200
            if self.path == "/.well-known/easyproxy-release.json":
                value = {**release, "sourceSha": "f" * 40} if record["mode"] == "old-release" else release
                body = json.dumps(value).encode()
            elif self.path == "/aggregator/manifests/stable.json" or self.path.endswith("/manifest.json"):
                if self.path == "/aggregator/manifests/stable.json":
                    record["stableReads"] += 1
                body = json.dumps(record["manifest"]).encode()
                if record["mode"] == "changed-stable" and record["stableReads"] == 2:
                    body += b" "
                if record["mode"] == "changed-immutable-manifest" and self.path.endswith("/manifest.json"):
                    body += b" "
            elif self.path.startswith("/aggregator/"):
                key = self.path.removeprefix("/aggregator/")
                immutable = key.startswith("releases/")
                if immutable:
                    key = key.removeprefix(f"releases/{run}/")
                body = record["bodies"].get(key, b"missing")
                if record["mode"] == "artifact-drift" and key == "subs/effective.txt":
                    body = b"wrong"
                if record["mode"] == "immutable-drift" and immutable:
                    body = b"wrong"
            elif self.path == "/login":
                body = b'<html><script type="module" src="/assets/js/index-fixture.js"></script></html>'
                if record["mode"] == "foreign-script":
                    body = body.replace(b"/assets/js/index-fixture.js", b"https://forbidden.invalid/leak.js")
            elif self.path == "/assets/js/index-fixture.js":
                body = b"export default function actualFrontend() {}\n" * 10
                if record["mode"] == "html-as-javascript":
                    body = b"<html>not a bundle</html>"
            else:
                body, status = b"not found", 404
            if record["mode"] in ("server-error", "redirect"):
                status = 500 if record["mode"] == "server-error" else 307
                body = b"private-provider-diagnostic"
            self.send_response(status)
            self.send_header("Content-Length", str(len(body)))
            if record["mode"] == "redirect":
                self.send_header("Location", "https://forbidden.invalid/redirect")
            if record["mode"] == "cookie":
                self.send_header("Set-Cookie", "unexpected-session=fixture")
            self.end_headers()
            self.wfile.write(body)

    class Server(ThreadingHTTPServer):
        def handle_error(self, *_):
            if not isinstance(sys.exc_info()[1], (BrokenPipeError, ConnectionResetError, ConnectionAbortedError)):
                super().handle_error(*_)

    server = Server(("127.0.0.1", 0), Handler)
    server.daemon_threads = True
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    original = requests.adapters.HTTPAdapter.send

    def send(adapter, request, **kwargs):
        url = urlsplit(request.url)
        assert url.scheme == "https" and url.netloc in (config.worker_origin.removeprefix("https://"),
                                                        config.pages_origin.removeprefix("https://"))
        assert request.method == "GET" and "Authorization" not in request.headers and "Cookie" not in request.headers
        assert kwargs.get("proxies") == {} and kwargs.get("verify") is True and adapter.max_retries.total == 0
        request.url = f"http://127.0.0.1:{server.server_port}" + url.path
        return original(adapter, request, **kwargs)

    monkeypatch.setattr(requests.adapters.HTTPAdapter, "send", send)
    monkeypatch.setattr(verification, "inspect_checkout", lambda *_: [{"path": "upstreams/aggregator", "sourceSha": "e" * 40}])
    monkeypatch.setenv("HTTPS_PROXY", "http://must-not-use.invalid:1")
    monkeypatch.setenv("EASYPROXY_CLOUDFLARE_API_TOKEN", "must-not-be-used")
    yield record
    server.shutdown()
    server.server_close()
    thread.join(timeout=5)
    assert not thread.is_alive()


def verify(wire, tmp_path):
    return verification.verify_public(wire["config"], OLD, tmp_path)


def test_independent_verification_reads_all_stable_and_immutable_bytes_without_credentials(wire, tmp_path):
    result = verify(wire, tmp_path)
    assert result["checks"] == {"release-versions-verified": True, "published-subscriptions-verified": True,
                                "pages-frontend-verified": True}
    assert result["counts"] == {"nodes": 1, "sources": 1} and wire["stableReads"] == 2
    assert len(wire["requests"]) == 21 and all(method == "GET" for method, _ in wire["requests"])
    assert not any("/api/login" in path or "_sba" in path for _, path in wire["requests"])


@pytest.mark.parametrize("mode", ["old-release", "changed-stable", "changed-immutable-manifest", "artifact-drift",
    "immutable-drift", "foreign-script", "html-as-javascript", "server-error", "redirect", "cookie"])
def test_versions_alone_do_not_mask_real_content_or_authentication_failure(wire, tmp_path, mode):
    wire["mode"] = mode
    with pytest.raises(verification.VerificationError) as failure:
        verify(wire, tmp_path)
    assert "private-provider-diagnostic" not in str(failure.value)
    assert all(method == "GET" for method, _ in wire["requests"])


@pytest.mark.parametrize("mutation", [lambda m: m.update(schema_version=True), lambda m: m.update(run_id="../../outside"),
    lambda m: m.update(root_commit="f" * 40), lambda m: m.update(aggregator_commit="f" * 40),
    lambda m: m["counts"].update(nodes=True), lambda m: m["artifacts"][0].update(size=True),
    lambda m: m["artifacts"][0].update(release_key="https://forbidden.invalid/artifact"),
    lambda m: m["artifacts"].pop()])
def test_manifest_identity_shape_and_paths_are_validated_before_artifact_requests(wire, tmp_path, mutation):
    mutation(wire["manifest"])
    with pytest.raises(verification.VerificationError):
        verify(wire, tmp_path)
    assert len(wire["requests"]) == 3


def test_effective_payload_must_match_published_node_count_even_with_updated_hash(wire, tmp_path):
    wire["manifest"]["counts"]["nodes"] = 2
    with pytest.raises(verification.VerificationError, match="EFFECTIVE"):
        verify(wire, tmp_path)


def test_response_limit_is_enforced_without_retaining_an_auth_cookie(wire):
    reader = verification.PublicReader(wire["config"])
    try:
        with pytest.raises(verification.VerificationError, match="SIZE"):
            reader.get(wire["config"].pages_origin, "/login", 3)
        assert len(reader.session.cookies) == 0
    finally:
        reader.close()
