"""真实 HTTP wire、合成 provider；不连接真实账号，不把合成发布算云端验收。"""
import base64
import copy
from concurrent.futures import ThreadPoolExecutor
from dataclasses import replace
from email.parser import BytesParser
from email.policy import default
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

from test_ech import coordination, MemoryStore, CHECKS, commit, git, checkout

for module_name in ("pages_api", "pages_build", "pages"):
    spec = importlib.util.spec_from_file_location(module_name, Path(__file__).parents[1] / (module_name + ".py"))
    module = importlib.util.module_from_spec(spec)
    sys.modules[module_name] = module
    spec.loader.exec_module(module)
pages = sys.modules["pages"]
api_module = sys.modules["pages_api"]
build = sys.modules["pages_build"]
PROJECT = "easyproxy-testing-misub"
PROJECT_ID = "11111111-1111-1111-1111-111111111111"
DATABASE_ID = "22222222-2222-2222-2222-222222222222"
DEPLOYMENT_ID = "33333333-3333-3333-3333-333333333333"
IDENTITY = {"repository": "fixture/easyproxy", "environment": "testing", "accountId": "c" * 32,
            "bucket": "easyproxy-testing", "purpose": "production"}
INVENTORY = [{"kind": "pages", "name": PROJECT, "id": PROJECT},
             {"kind": "d1", "name": "easyproxy-testing-db", "id": DATABASE_ID},
             {"kind": "r2", "name": IDENTITY["bucket"], "id": IDENTITY["bucket"]}]
SECRETS = {"ADMIN_PASSWORD": "fixture-admin-password", "COOKIE_SECRET": "fixture-cookie-secret",
           "MANIFEST_TOKEN": "fixture-manifest-token"}
ARTIFACT = build.PagesArtifact((build.Asset("/index.html", b"<html>fixture</html>", "a" * 32, "text/html"),),
                               b"synthetic nested multipart (real build tested separately)",
                               b'{"version":1,"include":["/*"],"exclude":[]}')


def held(release, action="deploy", inventory=None):
    store = MemoryStore()
    owner = coordination.Coordinator(store, {**IDENTITY, "purpose": "preview" if action == "preview" else "production"},
                                     pages.inventory_digest(INVENTORY if inventory is None else inventory))
    old = {"sourceSha": "a" * 40, "applicationVersion": "1.0.0"}
    if action == "update":
        first = owner.begin("deploy", old)
        owner.commit_verified(first, CHECKS["deploy"])
    return owner, owner.begin(action, release, previous=old if action == "update" else None), store, old


def deployment(release):
    return {"id": DEPLOYMENT_ID, "project_id": PROJECT_ID, "project_name": PROJECT, "uses_functions": True,
            "environment": "production", "deployment_trigger": {"metadata": {"branch": "main", "commit_hash": release["sourceSha"]}},
            "latest_stage": {"name": "deploy", "status": "success", "started_on": "synthetic"}}


def project(config, canonical=None):
    value = {"id": PROJECT_ID, "name": PROJECT, "subdomain": PROJECT + ".pages.dev", "production_branch": "main",
             "source": None, "deployment_configs": copy.deepcopy(config), "canonical_deployment": canonical}
    for env in value["deployment_configs"].values():
        for row in env["env_vars"].values():
            if row["type"] == "secret_text":
                row.pop("value")
    return value


@pytest.fixture
def candidate(tmp_path, monkeypatch):
    # These are protocol tests with an explicit synthetic build seam. Real Git,
    # npm, Functions compilation and workerd are exercised separately below.
    root = tmp_path / "application"
    root.mkdir()
    release = {"sourceSha": "b" * 40, "applicationVersion": "1.1.0"}
    settings = pages.PagesDeployment(root, tmp_path / "node.exe", tmp_path / "npm-cli.js", tmp_path / "pages-build", PROJECT,
                                     copy.deepcopy(INVENTORY))
    monkeypatch.setattr(pages, "build_pages", lambda *_args: ARTIFACT)
    return release, settings


@pytest.fixture
def api_wire(monkeypatch):
    record = {"requests": [], "writes": [], "project": None, "mode": "normal", "assets": set(), "deployments": 0,
              "polls": 0, "publicReads": 0, "serverErrors": []}
    monkeypatch.setattr(pages, "_propagation_delay", lambda: None)

    class Handler(BaseHTTPRequestHandler):
        protocol_version = "HTTP/1.1"

        def log_message(self, *_args):
            pass

        def send(self, value=None, status=200, raw=None):
            raw = raw if raw is not None else json.dumps({"success": True, "errors": [], "result": value}).encode()
            self.send_response(status)
            self.send_header("Content-Length", str(len(raw)))
            if status == 307:
                self.send_header("Location", "/forbidden-redirect")
            self.end_headers()
            self.wfile.write(raw)

        def do_GET(self):
            record["requests"].append(("GET", self.path))
            if self.path.endswith("easyproxy-release.json"):
                record["publicReads"] += 1
                if record["mode"] == "public-delay" and record["publicReads"] < 3:
                    self.send(status=503, raw=b"propagating")
                    return
                env = record["project"]["deployment_configs"]["production"]["env_vars"]
                value = {"schemaVersion": 1, "applicationId": "easyproxy-cloud", "sourceSha": env["SBA_RELEASE_SHA"]["value"],
                         "applicationVersion": env["SBA_RELEASE_VERSION"]["value"]}
                if record["mode"] == "public-wrong":
                    value["sourceSha"] = "f" * 40
                if record["mode"] == "public-bool":
                    value["schemaVersion"] = True
                self.send(raw=json.dumps(value).encode())
            elif self.path.endswith("/upload-token"):
                self.send({"jwt": "fixture-upload-jwt"})
            elif "/deployments/" in self.path:
                record["polls"] += 1
                value = copy.deepcopy(record["project"]["canonical_deployment"])
                if record["mode"] == "active-forever" or record["polls"] == 1:
                    value["latest_stage"]["status"] = "active"
                    if record["mode"] == "functions-late":
                        value["uses_functions"] = None
                if record["mode"] == "deployment-failure":
                    value["latest_stage"]["status"] = "failure"
                if record["mode"] == "deployment-wrong":
                    value["project_id"] = DATABASE_ID
                self.send(value)
            elif record["project"] is None:
                self.send(status=404, raw=b'{"success":false,"errors":[{"code":8000007}],"result":null}')
            else:
                value = copy.deepcopy(record["project"])
                if record["writes"] and record["mode"] == "config-drift":
                    value["deployment_configs"]["production"]["d1_databases"]["MISUB_DB"]["id"] = PROJECT_ID
                self.send(value)

        def mutate(self):
            raw = self.rfile.read(int(self.headers["Content-Length"]))
            record["requests"].append((self.command, self.path))
            record["writes"].append((self.command, self.path, raw))
            if record["mode"] == "server-error":
                self.send(status=500, raw=b"private-provider-details")
                return
            if record["mode"] == "redirect":
                self.send(status=307, raw=b"private-provider-details")
                return
            if self.path.endswith("/deployments"):
                record["deployments"] += 1
                message = BytesParser(policy=default).parsebytes(
                    ("Content-Type: " + self.headers["Content-Type"] + "\r\n\r\n").encode() + raw)
                parts = {p.get_param("name", header="content-disposition"): p.get_payload(decode=True) for p in message.iter_parts()}
                assert set(parts) == {"manifest", "branch", "commit_hash", "commit_message", "commit_dirty", "_worker.bundle", "_routes.json"}
                assert parts["_worker.bundle"] == ARTIFACT.worker_bundle and parts["_routes.json"] == ARTIFACT.routes
                assert json.loads(parts["manifest"]) == {a.path: a.hash for a in ARTIFACT.assets}
                assert parts["branch"] == b"main" and parts["commit_dirty"] == b"false"
                value = deployment({"sourceSha": parts["commit_hash"].decode()})
                record["project"]["canonical_deployment"] = copy.deepcopy(value)
                if record["mode"] == "functions-late":
                    value["uses_functions"] = None
            else:
                body = json.loads(raw)
                if self.path.endswith("/projects"):
                    assert body["name"] == PROJECT and body["production_branch"] == "main"
                    record["project"] = project(body["deployment_configs"])
                    value = record["project"]
                elif self.command == "PATCH":
                    canonical = record["project"]["canonical_deployment"]
                    record["project"] = project(body["deployment_configs"], canonical)
                    value = record["project"]
                elif self.path.endswith("/check-missing"):
                    value = [key for key in body["hashes"] if key not in record["assets"]]
                    if record["mode"] == "unknown-hash":
                        value = ["f" * 32]
                elif self.path.endswith("/upload"):
                    assert len(body) == 1 and body[0]["base64"] is True
                    asset = next(a for a in ARTIFACT.assets if a.hash == body[0]["key"])
                    assert base64.b64decode(body[0]["value"], validate=True) == asset.body
                    assert body[0]["metadata"] == {"contentType": asset.content_type}
                    record["assets"].add(body[0]["key"])
                    value = {}
                elif self.path.endswith("/upsert-hashes"):
                    value = {}
                else:
                    raise AssertionError("Unexpected endpoint")
            if record["mode"] == "lost-create" and self.path.endswith("/projects") or record["mode"] == "lost-deploy" and self.path.endswith("/deployments"):
                self.close_connection = True
                self.connection.shutdown(socket.SHUT_RDWR)
                return
            if record["mode"] == "invalid-success":
                self.send(raw=b'{"success":true,"success":false,"errors":[],"result":{}}')
                return
            self.send(value)

        do_POST = mutate
        do_PATCH = mutate

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
            if target.path.startswith("/client/v4/pages/assets/"):
                assert request.headers["Authorization"] == "Bearer fixture-upload-jwt"
            else:
                assert target.path.startswith(f"/client/v4/accounts/{IDENTITY['accountId']}/pages/")
                assert request.headers["Authorization"] == "Bearer fixture-api-token"
        else:
            assert target.netloc == PROJECT + ".pages.dev"
            assert "Authorization" not in request.headers and "Cookie" not in request.headers
        request.url = f"http://127.0.0.1:{server.server_port}" + target.path
        return original_send(adapter, request, **kwargs)

    monkeypatch.setattr(requests.adapters.HTTPAdapter, "send", local_send)
    monkeypatch.setenv("HTTPS_PROXY", "http://must-not-be-used.invalid:1")
    yield record
    server.shutdown()
    server.server_close()
    thread.join(timeout=5)
    assert not thread.is_alive() and record["serverErrors"] == []


def publish(owner, permit, settings, **kwargs):
    return pages.publish_pages(owner, permit, settings, api_token=kwargs.get("api_token", "fixture-api-token"),
                               secrets=kwargs.get("secrets", SECRETS))


@pytest.mark.parametrize("action", ["deploy", "update", "preview", "public-delay", "functions-late"])
def test_publish_stage_keeps_suite_writer_and_uses_distinct_upload_token(candidate, api_wire, action):
    release, settings = candidate
    api_wire["mode"] = action
    owner, permit, store, old = held(release, "deploy" if action in ("public-delay", "functions-late") else action)
    if action == "update":
        api_wire["project"] = project(pages.configuration(owner, old, PROJECT, DATABASE_ID, SECRETS), deployment(old))
    result = publish(owner, permit, settings)
    assert result["checks"] == {"pages-published": True, "pages-bindings-verified": True, "pages-version-verified": True}
    assert result["sourceSha"] == release["sourceSha"] and result["origin"] == f"https://{PROJECT}.pages.dev"
    assert result["deploymentId"] == DEPLOYMENT_ID and api_wire["polls"] == 2
    assert len(api_wire["writes"]) == 6 and api_wire["deployments"] == 1 and len(store.stages) == 1
    assert json.loads(store.body)["writer"]["operationId"] == permit.operation_id
    assert json.loads(store.body)["active"] == (old if action == "update" else None)
    assert "suite-published" not in result["checks"] and "service-ready" not in result["checks"]
    assert all(secret not in json.dumps(result) for secret in [*SECRETS.values(), "fixture-api-token", "fixture-upload-jwt"])


@pytest.mark.parametrize("mode", ["server-error", "redirect", "lost-create", "lost-deploy", "invalid-success", "config-drift",
                                  "unknown-hash", "deployment-failure", "deployment-wrong", "active-forever", "public-wrong", "public-bool"])
def test_unknown_never_replays_rolls_back_deletes_or_unlocks(candidate, api_wire, mode):
    release, settings = candidate
    owner, permit, store, _ = held(release)
    api_wire["mode"] = mode
    with pytest.raises(api_module.PagesError) as error:
        publish(owner, permit, settings)
    assert len(store.stages) == 1 and len(store.writes) == 1
    assert json.loads(store.body)["writer"]["operationId"] == permit.operation_id
    assert api_wire["deployments"] <= 1 and all(method in ("GET", "POST", "PATCH") for method, _ in api_wire["requests"])
    if mode == "active-forever":
        assert api_wire["polls"] == 36
    if mode.startswith("public-"):
        assert api_wire["publicReads"] == 12
    assert "private-provider-details" not in str(error.value) and all(secret not in str(error.value) for secret in SECRETS.values())
    prior = len(api_wire["writes"])
    api_wire["mode"] = "normal"
    # Initial lost create now appears as an unowned resource, or the stage marker
    # itself rejects reentry. Either path must prevent all further provider writes.
    with pytest.raises((coordination.CoordinationError, api_module.PagesError)):
        publish(owner, permit, replace(settings, workdir=settings.workdir.with_name("second-attempt")))
    assert len(api_wire["writes"]) == prior


@pytest.mark.parametrize("mode", ["inventory", "wrong-project", "unowned", "old-release", "git-source", "preview-binding",
                                  "extra-queue", "fail-open", "placeholder-api", "placeholder-business"])
def test_preflight_refuses_unapproved_ownership_and_credentials(candidate, api_wire, mode):
    release, settings = candidate
    action = "update" if mode in ("old-release", "git-source", "preview-binding", "extra-queue", "fail-open") else "deploy"
    owner, permit, store, old = held(release, action)
    kwargs = {}
    if mode == "inventory":
        settings.inventory[1]["id"] = PROJECT_ID
    if mode == "wrong-project":
        settings = replace(settings, project_name="another-project")
    if mode == "unowned" or action == "update":
        api_wire["project"] = project(pages.configuration(owner, old, PROJECT, DATABASE_ID, SECRETS), deployment(old))
    if mode == "old-release":
        api_wire["project"]["deployment_configs"]["production"]["env_vars"]["SBA_RELEASE_SHA"]["value"] = "f" * 40
    if mode == "git-source":
        api_wire["project"]["source"] = {"type": "github"}
    if mode == "preview-binding":
        api_wire["project"]["deployment_configs"]["preview"]["d1_databases"] = {"MISUB_DB": {"id": DATABASE_ID}}
    if mode == "extra-queue":
        api_wire["project"]["deployment_configs"]["production"]["queue_producers"] = {"UNAPPROVED": {"name": "other"}}
    if mode == "fail-open":
        api_wire["project"]["deployment_configs"]["production"]["fail_open"] = True
    if mode == "placeholder-api":
        kwargs["api_token"] = "verification-not-applicable"
    if mode == "placeholder-business":
        kwargs["secrets"] = {**SECRETS, "COOKIE_SECRET": "verification-not-applicable"}
    with pytest.raises((coordination.CoordinationError, api_module.PagesError)):
        publish(owner, permit, settings, **kwargs)
    assert api_wire["writes"] == [] and not store.stages


def test_concurrent_stage_entry_has_only_one_publication(candidate, api_wire):
    release, settings = candidate
    owner, permit, store, _ = held(release)
    def attempt(index):
        try:
            return publish(owner, permit, replace(settings, workdir=settings.workdir.with_name(str(index))))
        except (coordination.CoordinationError, api_module.PagesError):
            return None
    with ThreadPoolExecutor(max_workers=2) as pool:
        results = list(pool.map(attempt, range(2)))
    assert sum(value is not None for value in results) == 1
    assert api_wire["deployments"] == len(store.stages) == 1


def test_inventory_bucket_must_match_the_shared_control_bucket(candidate, api_wire):
    release, settings = candidate
    settings.inventory[2] = {"kind": "r2", "name": "another-bucket", "id": "another-bucket"}
    owner, permit, store, _ = held(release, inventory=settings.inventory)
    with pytest.raises(api_module.PagesError, match="IDENTITY"):
        publish(owner, permit, settings)
    assert api_wire["writes"] == [] and not store.stages


def test_build_environment_has_no_inherited_credentials_or_node_injection(tmp_path, monkeypatch):
    for key in ("CLOUDFLARE_API_TOKEN", "AWS_SECRET_ACCESS_KEY", "GITHUB_TOKEN", "NODE_OPTIONS", "NODE_PATH", "NPM_TOKEN", "MISUB_ADMIN_PASSWORD"):
        monkeypatch.setenv(key, "must-not-inherit")
    node = tmp_path / "fixed-node/bin/node"
    env = build._environment(tmp_path, node)
    assert all(value != "must-not-inherit" for value in env.values())
    assert env["HOME"] == str(tmp_path / "home") and env["NPM_CONFIG_FETCH_RETRIES"] == "0"
    assert env["PATH"].split(os.pathsep)[0] == str(node.parent) and env["TMPDIR"] == str(tmp_path)


@pytest.mark.parametrize("change", ["floating", "url", "integrity", "local"])
def test_build_lock_rejects_floating_or_nonregistry_dependencies(change):
    raw = {"lockfileVersion": 3, "packages": {"": {}, **{"node_modules/" + p: {"version": v,
           "resolved": "https://registry.npmjs.org/fixture/-/fixture-1.0.0.tgz", "integrity": "sha512-fixture"}
           for p, v in build.VERSIONS.items()}}}
    row = raw["packages"]["node_modules/wrangler"]
    row[{"floating": "version", "url": "resolved", "integrity": "integrity", "local": "link"}[change]] = {
        "floating": "latest", "url": "https://unapproved.invalid/package.tgz", "integrity": "sha1-bad", "local": True}[change]
    with pytest.raises(api_module.PagesError):
        build._lock(json.dumps(raw).encode())


def test_real_source_inspection_rejects_dirty_submodule_before_install(checkout, tmp_path):
    root, sha, _ = checkout
    git(root, "remote", "set-url", "origin", "https://github.com/fixture/easyproxy.git")
    (root / "upstreams/misub/source.txt").write_text("not approved", encoding="utf-8")
    output = tmp_path / "must-not-build"
    with pytest.raises(sys.modules["sources"].SourceError):
        build.build_pages(root, IDENTITY["repository"], sha, tmp_path / "node.exe", tmp_path / "npm-cli.js", output)
    assert not output.exists()


def test_mirror_normalization_keeps_exact_version_and_integrity():
    raw = {"lockfileVersion": 3, "packages": {"": {}, **{"node_modules/" + p: {"version": v,
           "resolved": "https://registry.npmmirror.com/fixture/-/fixture-1.0.0.tgz", "integrity": "sha512-fixture"}
           for p, v in build.VERSIONS.items()}}}
    normalized = json.loads(build._lock(json.dumps(raw).encode()))
    for key, row in normalized["packages"].items():
        if key:
            assert row == {**raw["packages"][key], "resolved": "https://registry.npmjs.org/fixture/-/fixture-1.0.0.tgz"}


def test_original_lock_and_actual_functions_bundle_are_accepted_when_supplied():
    path = os.environ.get("EASYPROXY_TEST_PAGES_BUILD")
    if not path:
        pytest.skip("real Pages build requires EASYPROXY_TEST_PAGES_BUILD")
    output = Path(path)
    build._lock((output / "misub/package-lock.json").read_bytes())
    build._bundle((output / "_worker.bundle").read_bytes())
    assets = build._assets(output / "misub/dist", output / "assets.json")
    assert len(assets) > 10 and b"SBA_RELEASE_SHA" in (output / "_worker.bundle").read_bytes()


def test_actual_build_artifact_flows_through_real_multipart_http(candidate, api_wire, monkeypatch):
    path = os.environ.get("EASYPROXY_TEST_PAGES_BUILD")
    if not path:
        pytest.skip("actual artifact HTTP flow requires EASYPROXY_TEST_PAGES_BUILD")
    output = Path(path)
    artifact = build.PagesArtifact(build._assets(output / "misub/dist", output / "assets.json"),
                                   (output / "_worker.bundle").read_bytes(), (output / "_routes.json").read_bytes())
    build._bundle(artifact.worker_bundle)
    monkeypatch.setattr(sys.modules[__name__], "ARTIFACT", artifact)
    release, settings = candidate
    owner, permit, store, _ = held(release)
    result = publish(owner, permit, settings)
    assert result["assetCount"] == len(artifact.assets) > 10
    assert api_wire["deployments"] == len(store.stages) == 1
    assert len(api_wire["writes"]) == 5 + len({a.hash for a in artifact.assets})
