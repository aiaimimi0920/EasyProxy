"""真实固定 Git + esbuild + HTTP multipart 发布；云端只在本机合成，绝不写真实账号。"""
import copy
from concurrent.futures import ThreadPoolExecutor
from dataclasses import replace
from email.parser import BytesParser
from email.policy import default
import hashlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import importlib.util
import json
import os
from pathlib import Path
import socket
import sys
import threading
from urllib.parse import urlsplit

import pytest
import requests

from test_coordination import coordination, MemoryStore, CHECKS
from test_sources import checkout, commit, git, sources

sys.modules.setdefault("coordination", coordination)
sys.modules.setdefault("sources", sources)
for name in ("cloudflare_http", "worker_api", "ech"):
    spec = importlib.util.spec_from_file_location(name, Path(__file__).parents[1] / (name + ".py"))
    module = importlib.util.module_from_spec(spec)
    sys.modules[name] = module
    spec.loader.exec_module(module)
ech = sys.modules["ech"]
api_module = sys.modules["worker_api"]

WORKER = "easyproxy-testing-ech"
INVENTORY = [{"kind": "worker", "name": WORKER, "id": WORKER},
             {"kind": "r2", "name": "easyproxy-testing", "id": "easyproxy-testing"}]
IDENTITY = {"repository": "fixture/easyproxy", "environment": "testing", "accountId": "c" * 32,
            "bucket": "easyproxy-testing", "purpose": "production"}
ECH_TOKEN = "fixture-ech-token"


@pytest.fixture
def api_wire(monkeypatch):
    record = {"requests": [], "writes": [], "settings": None, "mode": "normal", "bundle": None,
              "serverErrors": [], "publicReads": 0}
    monkeypatch.setattr(ech, "_propagation_delay", lambda: None)

    class Handler(BaseHTTPRequestHandler):
        protocol_version = "HTTP/1.1"

        def log_message(self, *_args):
            pass

        def send(self, result=None, status=200, raw=None):
            body = raw if raw is not None else json.dumps({"success": True, "errors": [], "result": result}).encode()
            self.send_response(status)
            self.send_header("Content-Length", str(len(body)))
            if status == 307:
                self.send_header("Location", "/forbidden-redirect")
            self.end_headers()
            self.wfile.write(body)

        def do_GET(self):
            record["requests"].append(("GET", self.path, dict(self.headers)))
            if self.path.endswith("/.well-known/easyproxy-release.json"):
                record["publicReads"] += 1
                if record["mode"] == "public-propagation" and record["publicReads"] <= 2:
                    self.send(status=503, raw=b"not yet propagated")
                    return
                bindings = {item["name"]: item for item in record["settings"]["bindings"]}
                payload = {"schemaVersion": 1, "applicationId": "easyproxy-cloud", "sourceSha": bindings["SBA_RELEASE_SHA"]["text"],
                           "applicationVersion": bindings["SBA_RELEASE_VERSION"]["text"]}
                if record["mode"] == "public-wrong-version":
                    payload["sourceSha"] = "f" * 40
                if record["mode"] == "public-boolean-schema":
                    payload["schemaVersion"] = True
                self.send(raw=json.dumps(payload).encode())
            elif self.path.endswith("/workers/subdomain"):
                self.send({"subdomain": "fixture-account"})
            elif self.path.endswith("/workers/scripts"):
                self.send([] if record["settings"] is None else [{"id": WORKER}])
            elif self.path.endswith("/settings"):
                if record["mode"].startswith("settings-404-"):
                    code = int(record["mode"].removeprefix("settings-404-"))
                    self.send(status=404, raw=json.dumps({"success": False, "errors": [{"code": code}]}).encode())
                    return
                value = copy.deepcopy(record["settings"])
                if record["writes"] and record["mode"] == "wrong-binding":
                    value["bindings"][0]["bucket_name"] = "unapproved-bucket"
                if record["writes"] and record["mode"] == "logs-disabled":
                    value["observability"]["enabled"] = False
                self.send(value)
            elif self.path.endswith("/subdomain"):
                self.send({"enabled": True, "previews_enabled": False})
            elif self.path.endswith("/workers/scripts/" + WORKER) and "downloadBundle" in record:
                body = record["downloadBundle"]
                self.send(raw=b'--' + b'a' * 32 + b'\r\nContent-Disposition: form-data; name="edge.mjs"\r\nContent-Type: text/plain\r\n\r\n'
                          + body + b'\r\n--' + b'a' * 32 + b'--\r\n')
            else:
                self.send(status=404, raw=b"private-provider-details")

        def mutate(self):
            body = self.rfile.read(int(self.headers["Content-Length"]))
            record["requests"].append((self.command, self.path, dict(self.headers)))
            record["writes"].append((self.command, self.path, body, dict(self.headers)))
            if record["mode"] == "server-error":
                self.send(status=500, raw=b"private-provider-details")
                return
            if record["mode"] == "redirect":
                self.send(status=307, raw=b"private-provider-details")
                return
            if self.command == "PUT":
                message = BytesParser(policy=default).parsebytes(
                    ("Content-Type: " + self.headers["Content-Type"] + "\r\nMIME-Version: 1.0\r\n\r\n").encode() + body)
                parts = {part.get_param("name", header="content-disposition"): part for part in message.iter_parts()}
                assert set(parts) == {"metadata", "edge.mjs"}
                record["settings"] = json.loads(parts["metadata"].get_payload(decode=True))
                record["bundle"] = parts["edge.mjs"].get_payload(decode=True)
                # Emulate Cloudflare's secret write-only representation.
                for binding in record["settings"]["bindings"]:
                    if binding["type"] == "secret_text":
                        assert binding.pop("text") == ECH_TOKEN
            else:
                assert json.loads(body) == {"enabled": True, "previews_enabled": False}
            if record["mode"] == "response-lost":
                self.close_connection = True
                self.connection.shutdown(socket.SHUT_RDWR)
                return
            if record["mode"] == "invalid-success":
                self.send(raw=b'{"success":true,"success":false,"errors":[],"result":{}}')
                return
            self.send({"id": WORKER, "etag": "fixture-etag"} if self.command == "PUT"
                      else {"enabled": True, "previews_enabled": False})

        do_PUT = mutate
        do_POST = mutate

    class Server(ThreadingHTTPServer):
        def handle_error(self, *_args):
            error = sys.exc_info()[1]
            if not isinstance(error, (BrokenPipeError, ConnectionResetError, ConnectionAbortedError)):
                record["serverErrors"].append(type(error).__name__)

    server = Server(("127.0.0.1", 0), Handler)
    server.daemon_threads = True
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    original_send = requests.adapters.HTTPAdapter.send

    def local_send(adapter, request, **kwargs):
        target = urlsplit(request.url)
        assert target.scheme == "https" and kwargs.get("verify") is True
        assert kwargs.get("proxies") == {} and adapter.max_retries.total == 0
        if target.netloc == "api.cloudflare.com":
            assert target.path.startswith(f"/client/v4/accounts/{IDENTITY['accountId']}/workers/")
            assert request.headers["Authorization"] == "Bearer fixture-api-token"
        else:
            assert target.netloc == WORKER + ".fixture-account.workers.dev"
            assert "Authorization" not in request.headers and "Cookie" not in request.headers
        request.url = f"http://127.0.0.1:{server.server_port}" + target.path
        return original_send(adapter, request, **kwargs)

    monkeypatch.setattr(requests.adapters.HTTPAdapter, "send", local_send)
    monkeypatch.setenv("HTTPS_PROXY", "http://must-not-be-used.invalid:1")
    yield record
    server.shutdown()
    server.server_close()
    thread.join(timeout=5)
    assert not thread.is_alive()
    assert record["serverErrors"] == []


@pytest.fixture
def candidate(checkout, tmp_path):
    root, _, _ = checkout
    source = Path(__file__).parents[2]
    for relative in ech.SOURCE_FILES:
        target = root / relative
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes((source / relative).read_bytes())
    git(root, "remote", "set-url", "origin", "https://github.com/fixture/easyproxy.git")
    release = {"sourceSha": commit(root), "applicationVersion": "1.1.0"}
    binary = os.environ.get("EASYPROXY_TEST_ESBUILD")
    if not binary:
        pytest.skip("real pinned esbuild requires EASYPROXY_TEST_ESBUILD")
    return release, ech.EchDeployment(root, Path(binary), tmp_path / "edge-build", WORKER, copy.deepcopy(INVENTORY))


def held(release, action="deploy", inventory=None):
    store = MemoryStore()
    identity = {**IDENTITY, "purpose": "preview" if action == "preview" else "production"}
    owner = coordination.Coordinator(store, identity, ech.inventory_digest(INVENTORY if inventory is None else inventory))
    old = {"sourceSha": "a" * 40, "applicationVersion": "1.0.0"}
    if action == "update":
        first = owner.begin("deploy", old)
        owner.commit_verified(first, CHECKS["deploy"])
    return owner, owner.begin(action, release, previous=old if action == "update" else None), store, old


def publish(owner, permit, settings):
    return ech.publish_ech(owner, permit, settings, api_token="fixture-api-token", ech_token=ECH_TOKEN)


@pytest.mark.parametrize("code", [10007, 10000, 10090])
def test_pristine_worker_absence_requires_exact_provider_404_code_without_writes(api_wire, code):
    api_wire["mode"] = "settings-404-" + str(code)
    client = api_module.WorkerAPI(IDENTITY["accountId"], "fixture-api-token")
    try:
        if code == 10007:
            assert client.settings_or_absent(WORKER) is None
        else:
            with pytest.raises(api_module.WorkerError, match="RESPONSE_UNKNOWN"):
                client.settings_or_absent(WORKER)
    finally:
        client.close()
    assert len(api_wire["requests"]) == 1 and api_wire["writes"] == []


@pytest.mark.parametrize("system,name", [("win32", "esbuild.exe"), ("linux", "esbuild")])
def test_compiler_platform_selects_only_exact_x64_record(monkeypatch, system, name):
    monkeypatch.setattr(ech.sys, "platform", system)
    monkeypatch.setattr(ech.platform, "machine", lambda: "AMD64" if system == "win32" else "x86_64")
    selected, digest = ech._compiler_tool()
    assert selected == name and len(digest) == 64
    assert digest != ech.ESBUILD_TOOLS["linux" if system == "win32" else "win32"][1]


@pytest.mark.parametrize("system,machine", [("darwin", "x86_64"), ("linux", "aarch64"), ("win32", "ARM64")])
def test_unsupported_compiler_platform_never_falls_back(monkeypatch, system, machine):
    monkeypatch.setattr(ech.sys, "platform", system)
    monkeypatch.setattr(ech.platform, "machine", lambda: machine)
    with pytest.raises(api_module.WorkerError, match="TOOLCHAIN"):
        ech._compiler_tool()


@pytest.mark.parametrize("action", ["deploy", "update", "preview", "public-propagation"])
def test_real_fixed_build_to_http_publication_keeps_suite_writer(candidate, api_wire, action):
    release, settings = candidate
    if action == "public-propagation":
        api_wire["mode"] = action
        action = "deploy"
    owner, permit, store, old = held(release, action)
    if action == "update":
        api_wire["settings"] = ech.metadata(owner, old, ECH_TOKEN)
    result = publish(owner, permit, settings)
    assert result["checks"] == {"ech-published": True, "ech-bindings-verified": True, "ech-version-verified": True}
    assert result["sourceSha"] == release["sourceSha"] and result["applicationVersion"] == "1.1.0"
    assert result["aggregatorBaseUrl"] == f"https://{WORKER}.fixture-account.workers.dev/aggregator"
    bundle = (settings.workdir / "edge.mjs").read_bytes()
    compiler = settings.workdir / ech._compiler_tool()[0]
    assert compiler.read_bytes() == settings.esbuild.read_bytes()
    if sys.platform == "linux":
        assert compiler.stat().st_mode & 0o777 == 0o700
    assert api_wire["bundle"] == bundle and ECH_TOKEN.encode() not in bundle
    assert result["bundleSha256"] == hashlib.sha256(bundle).hexdigest()
    assert [(item[0], item[1].split("/")[-1]) for item in api_wire["writes"]] == [("PUT", WORKER), ("POST", "subdomain")]
    assert len(store.writes) == (3 if action == "update" else 1)
    assert len(store.stages) == 1
    assert json.loads(store.body)["writer"]["operationId"] == permit.operation_id
    assert json.loads(store.body)["active"] == (old if action == "update" else None)
    assert "suite-published" not in result["checks"] and "service-ready" not in result["checks"]
    assert ECH_TOKEN not in json.dumps(result) and "fixture-api-token" not in json.dumps(result)
    with pytest.raises(coordination.CoordinationError, match="BUSY"):
        owner.current_release()


@pytest.mark.parametrize("failure", [None, "settings", "bundle", "server-error", "response-lost"])
@pytest.mark.parametrize("assets", [False, True, "diagnostic"])
def test_exact_pages_resume_publishes_only_new_worker_revision_without_subdomain_rewrite(candidate, api_wire, monkeypatch, tmp_path, failure, assets):
    import pages_repair
    release, settings = candidate
    owner, permit, store, _ = held(release)
    bundle = ech.build_bundle(settings.root, settings.esbuild, tmp_path / "previous-worker")
    monkeypatch.setattr(pages_repair, "WORKER_BUNDLE_SHA256", hashlib.sha256(bundle).hexdigest())
    api_wire["downloadBundle"] = b"changed" if failure == "bundle" else bundle
    parent = pages_repair.ASSETS_PARENT_RELEASE if assets else pages_repair.PAGES_PARENT_RELEASE
    if assets == "diagnostic": parent = pages_repair.PAGES_DIAGNOSTIC_PARENT_RELEASE
    api_wire["settings"] = ech.metadata(owner, parent, ECH_TOKEN)
    if failure == "settings": api_wire["settings"]["bindings"][0]["bucket_name"] = "other-bucket"
    api_wire["mode"] = failure or "normal"
    resume = pages_repair.PagesResume(permit.operation_id, hashlib.sha256(permit.state_bytes).hexdigest(), parent["sourceSha"])
    def run():
        return ech.publish_ech(owner, permit, settings, api_token="fixture-api-token", ech_token=ECH_TOKEN, resume=resume)
    if failure:
        with pytest.raises(api_module.WorkerError): run()
        assert len(api_wire["writes"]) == (0 if failure in ("settings", "bundle") else 1)
    else:
        assert run()["checks"]["ech-published"] is True
        assert [row[0] for row in api_wire["writes"]] == ["PUT"]
    assert json.loads(store.body)["writer"]["operationId"] == permit.operation_id
    before = len(api_wire["writes"])
    with pytest.raises((api_module.WorkerError, coordination.CoordinationError)): run()
    assert len(api_wire["writes"]) == before


@pytest.mark.parametrize("mode", ["server-error", "response-lost", "redirect", "invalid-success", "wrong-binding",
                                  "logs-disabled", "public-wrong-version", "public-boolean-schema"])
def test_unknown_publication_never_retries_restores_or_unlocks(candidate, api_wire, mode):
    release, settings = candidate
    owner, permit, store, _ = held(release)
    api_wire["mode"] = mode
    with pytest.raises(api_module.WorkerError) as error:
        publish(owner, permit, settings)
    assert len(api_wire["writes"]) == (2 if mode.startswith("public-") else 1)
    if mode.startswith("public-"):
        assert api_wire["publicReads"] == 12
    assert all(method in ("GET", "PUT", "POST") for method, _, _ in api_wire["requests"])
    assert len(store.writes) == 1 and json.loads(store.body)["writer"]["operationId"] == permit.operation_id
    assert "private-provider-details" not in str(error.value) and ECH_TOKEN not in str(error.value)


@pytest.mark.parametrize("mode", ["inventory", "wrong-worker", "wrong-bucket", "unowned-worker", "old-release-drift",
                                  "dirty-source", "wrong-toolchain", "placeholder-token", "placeholder-ech"])
def test_preflight_rejects_unapproved_state_before_any_write(candidate, api_wire, mode, tmp_path):
    release, settings = candidate
    action = "update" if mode == "old-release-drift" else "deploy"
    owner, permit, store, old = held(release, action)
    if mode in ("inventory", "wrong-worker", "wrong-bucket"):
        inventory = copy.deepcopy(INVENTORY)
        inventory[0]["id"] = "wrong" if mode == "inventory" else inventory[0]["id"]
        settings = replace(settings, inventory=inventory, worker_name="unapproved-ech" if mode == "wrong-worker" else WORKER)
        if mode == "wrong-bucket":
            settings.inventory[1]["id"] = "unapproved-bucket"
            owner, permit, store, old = held(release, action, inventory=settings.inventory)
    if mode in ("unowned-worker", "old-release-drift"):
        api_wire["settings"] = ech.metadata(owner, old, ECH_TOKEN)
        if mode == "old-release-drift":
            api_wire["settings"]["tags"] = ["unowned-tag"]
    if mode == "dirty-source":
        (settings.root / ".sba/edge.mjs").write_text("not approved", encoding="utf-8")
    if mode == "wrong-toolchain":
        binary = tmp_path / "fake-esbuild.exe"
        binary.write_bytes(b"must not execute")
        settings = replace(settings, esbuild=binary)
    kwargs = {"api_token": "verification-not-applicable" if mode == "placeholder-token" else "fixture-api-token",
              "ech_token": "verification-not-applicable" if mode == "placeholder-ech" else ECH_TOKEN}
    with pytest.raises((api_module.WorkerError, sources.SourceError)):
        ech.publish_ech(owner, permit, settings, **kwargs)
    assert api_wire["writes"] == []
    assert json.loads(store.body)["writer"]["operationId"] == permit.operation_id


def test_inventory_hash_is_order_independent_and_rejects_duplicates():
    assert ech.inventory_digest(INVENTORY) == ech.inventory_digest(list(reversed(INVENTORY)))
    with pytest.raises(coordination.CoordinationError, match="INVENTORY"):
        ech.inventory_digest(INVENTORY + INVENTORY[:1])


def test_verify_has_no_write_client_with_placeholder_credentials():
    with pytest.raises(api_module.WorkerError, match="CREDENTIALS"):
        api_module.WorkerAPI(IDENTITY["accountId"], "verification-not-applicable")


def test_same_permit_cannot_replay_an_unknown_publication_in_a_new_directory(candidate, api_wire):
    release, settings = candidate
    owner, permit, store, _ = held(release)
    api_wire["mode"] = "server-error"
    with pytest.raises(api_module.WorkerError, match="REQUEST_UNKNOWN"):
        publish(owner, permit, settings)
    api_wire["mode"] = "normal"
    second = replace(settings, workdir=settings.workdir.with_name("second-attempt"))
    with pytest.raises(coordination.CoordinationError, match="STAGE_ALREADY_ATTEMPTED"):
        publish(owner, permit, second)
    assert len(api_wire["writes"]) == len(store.stages) == 1


def test_concurrent_reentry_of_same_publication_has_one_writer(candidate, api_wire):
    release, settings = candidate
    owner, permit, store, _ = held(release)

    def attempt(index):
        try:
            return publish(owner, permit, replace(settings, workdir=settings.workdir.with_name(f"concurrent-{index}")))
        except (coordination.CoordinationError, api_module.WorkerError):
            return None
    with ThreadPoolExecutor(max_workers=2) as pool:
        results = list(pool.map(attempt, range(2)))
    assert sum(result is not None for result in results) == 1
    assert len(api_wire["writes"]) == 2 and len(store.stages) == 1


def test_esbuild_cannot_publish_approved_imports_reading_outside_source(candidate, api_wire):
    release, settings = candidate
    outside = settings.root.parent / "outside.js"
    outside.write_text("console.log('must-not-enter-the-bundle');", encoding="utf-8")
    entry = settings.root / ".sba/edge.mjs"
    entry.write_text(entry.read_text(encoding="utf-8") + '\nimport "../../outside.js";\n', encoding="utf-8")
    release["sourceSha"] = commit(settings.root)
    owner, permit, _, _ = held(release)
    with pytest.raises(api_module.WorkerError, match="SOURCE"):
        publish(owner, permit, settings)
    assert api_wire["writes"] == []


def test_linked_build_input_is_rejected_before_running_compiler(candidate, api_wire, monkeypatch):
    release, settings = candidate
    entry = settings.root / ".sba/edge.mjs"
    original = Path.is_symlink
    monkeypatch.setattr(Path, "is_symlink", lambda path: path == entry or original(path))
    owner, permit, _, _ = held(release)
    with pytest.raises(api_module.WorkerError, match="SOURCE"):
        publish(owner, permit, settings)
    assert not settings.workdir.exists() and api_wire["writes"] == []
