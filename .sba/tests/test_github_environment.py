"""真实 HTTP 编解码与单次发送；本机模拟 GitHub，不接触远端账号或秘密。"""
import base64
import copy
import hashlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
from pathlib import Path
import socket
import sys
import threading
from urllib.parse import urlsplit

import pytest
import requests

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import github_environment as deployment
from github_api import GitHubAPI, GitHubError
from coordination import Coordinator, CoordinationError
from sealed_box import Sealer
from suite_config import parse_configuration
from suite_config import WORKFLOW
from refresh_registry import REGISTRY_VARIABLE
from test_coordination import MemoryStore, OLD, NEW, CHECKS, state
from test_suite_config import PUBLIC


@pytest.fixture
def wire(monkeypatch):
    record = {"environment": None, "variables": {}, "secrets": {}, "policies": [], "writes": [], "requests": [],
              "mode": "normal", "defaultBranch": "main", "admin": True, "registry": None}
    record["workflow"] = b"name: Fixture approved schedule\non: workflow_dispatch\njobs: {}\n"
    root = "/repos/fixture/easyproxy"
    env = root + "/environments/testing"
    public_key = base64.b64encode(bytes(range(32))).decode()

    class Handler(BaseHTTPRequestHandler):
        protocol_version = "HTTP/1.1"

        def log_message(self, *_):
            pass

        def send(self, value=None, status=200, raw=None):
            body = raw if raw is not None else (json.dumps(value).encode() if value is not None else b"")
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            if status == 307:
                self.send_header("Location", "https://forbidden.invalid/token-leak")
            self.end_headers()
            self.wfile.write(body)

        def do_GET(self):
            path = urlsplit(self.path).path
            record["requests"].append(("GET", path))
            if path == root:
                self.send({"id": 12345, "full_name": "Fixture/EasyProxy", "default_branch": record["defaultBranch"],
                           "permissions": {"admin": record["admin"]}, "archived": False, "disabled": False})
            elif path == env:
                if record["environment"] is None:
                    self.send({"message": "Not Found"}, 404)
                else:
                    row = copy.deepcopy(record["environment"])
                    if record["mode"] == "wrong-environment":
                        row["url"] = "https://api.github.com/repos/foreign/repo/environments/testing"
                    self.send(row)
            elif path == root + "/contents/" + WORKFLOW:
                body = record["workflow"] if record["mode"] != "wrong-workflow" else b"wrong"
                self.send({"type": "file", "path": WORKFLOW, "size": len(body), "encoding": "base64",
                    "content": base64.b64encode(body).decode(),
                    "sha": hashlib.sha1(b"blob " + str(len(body)).encode() + b"\0" + body).hexdigest()})
            elif path == root + "/actions/workflows/sba-aggregator-refresh.yml":
                self.send({"id": 123, "path": WORKFLOW, "state": "disabled_manually" if record["mode"] == "disabled-workflow" else "active"})
            elif path == root + "/actions/variables/" + REGISTRY_VARIABLE:
                if record["registry"] is None:
                    self.send({"message": "Not Found"}, 404)
                else:
                    self.send({"name": REGISTRY_VARIABLE, "value": record["registry"]})
            elif path.endswith("/deployment-branch-policies"):
                self.send({"total_count": len(record["policies"]), "branch_policies": record["policies"]})
            elif path.endswith("/variables"):
                self.send({"total_count": len(record["variables"]), "variables": [
                    {"name": key, "value": value} for key, value in record["variables"].items()]})
            elif path.endswith("/secrets/public-key"):
                self.send({"key_id": "123456789", "key": public_key})
            elif path.endswith("/secrets"):
                self.send({"total_count": len(record["secrets"]), "secrets": [
                    {"name": name} for name in record["secrets"]]})
            else:
                self.send({"message": "Not Found"}, 404)

        def mutate(self):
            path = urlsplit(self.path).path
            body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
            record["requests"].append((self.command, path))
            record["writes"].append((self.command, path, body))
            if record["mode"] in ("redirect", "server-error"):
                self.send({"message": "private-provider-message"}, 307 if record["mode"] == "redirect" else 500)
                return
            if path == env:
                record["environment"] = {"id": 67890, "name": "testing",
                    "url": "https://api.github.com/repos/Fixture/EasyProxy/environments/testing",
                    "deployment_branch_policy": body["deployment_branch_policy"],
                    "protection_rules": [{"id": 17, "type": "branch_policy"}]}
                status, value = 200, record["environment"]
            elif path.endswith("/deployment-branch-policies"):
                record["policies"].append({"id": 1, **body})
                status, value = 200, record["policies"][-1]
            elif path == root + "/actions/variables" or path == root + "/actions/variables/" + REGISTRY_VARIABLE:
                assert body["name"] == REGISTRY_VARIABLE
                record["registry"] = body["value"]
                status, value = (201 if self.command == "POST" else 204), None
            elif path.endswith("/variables"):
                record["variables"][body["name"]] = body["value"]
                status, value = 201, None
            elif "/secrets/" in path:
                name = path.rsplit("/", 1)[1]
                status = 204 if name in record["secrets"] else 201
                record["secrets"][name] = body
                value = None
            else:
                raise AssertionError("unexpected write")
            if record["mode"] == "response-lost":
                self.close_connection = True
                self.connection.shutdown(socket.SHUT_RDWR)
                return
            if record["mode"] == "duplicate-json":
                self.send(raw=b'{"id":67890,"id":0}')
                return
            self.send(value, status)

        do_PUT = mutate
        do_POST = mutate
        do_PATCH = mutate

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
        target = urlsplit(request.url)
        assert target.scheme == "https" and target.netloc == "api.github.com"
        assert request.headers["Authorization"] == "Bearer fixture-token"
        assert request.headers["X-GitHub-Api-Version"] == "2022-11-28"
        assert kwargs.get("verify") is True and kwargs.get("proxies") == {} and adapter.max_retries.total == 0
        assert "Cookie" not in request.headers
        request.url = f"http://127.0.0.1:{server.server_port}" + target.path + ("?" + target.query if target.query else "")
        return original(adapter, request, **kwargs)

    monkeypatch.setattr(requests.adapters.HTTPAdapter, "send", send)
    monkeypatch.setenv("HTTPS_PROXY", "http://must-not-use.invalid:1")
    monkeypatch.setattr(deployment, "inspect_checkout", lambda *_: None)

    def encrypt(self, key, plaintext):
        assert key == public_key and plaintext.startswith("fixture-")
        return base64.b64encode(b"x" * (len(plaintext.encode()) + 48)).decode()

    monkeypatch.setattr(Sealer, "encrypt", encrypt)
    yield record
    server.shutdown()
    server.server_close()
    thread.join(timeout=5)
    assert not thread.is_alive()


def setup(action="deploy"):
    config = parse_configuration(PUBLIC, repository="fixture/EasyProxy", environment="testing")
    store = MemoryStore()
    store.bucket = config.bucket
    purpose = "preview" if action == "preview" else "production"
    owner = Coordinator(store, config.identity(purpose), config.inventory_digest)
    permit = owner.begin(action, OLD)
    secrets = {name: "fixture-" + name for name in deployment.REFRESH_SECRETS} if purpose == "production" else {}
    sealer = Sealer(Path("/fixture/sealer"), OLD["sourceSha"], "f" * 64) if secrets else None
    return config, owner, permit, secrets, sealer


def configure(inputs, tmp_path):
    config, owner, permit, secrets, sealer = inputs
    path = tmp_path / WORKFLOW
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(b"name: Fixture approved schedule\non: workflow_dispatch\njobs: {}\n")
    return deployment.configure_environment(owner, permit, config, tmp_path, token="fixture-token", secrets=secrets, sealer=sealer)


def test_environment_configures_only_refresh_credentials_and_keeps_suite_writer(wire, tmp_path):
    inputs = setup()
    result = configure(inputs, tmp_path)
    assert result["checks"] == {"github-environment-configured": True, "github-environment-verified": True,
                                "github-workflow-verified": True}
    assert set(wire["secrets"]) == deployment.REFRESH_SECRETS
    assert len(wire["writes"]) == 5 + len(deployment.REFRESH_SECRETS)
    assert json.loads(wire["registry"]) == {"schemaVersion": 1, "repository": "fixture/easyproxy", "environments": ["testing"]}
    assert state(inputs[1].store)["writer"] is not None and state(inputs[1].store)["active"] is None
    encoded = json.dumps(wire["writes"])
    assert not any(value in encoded for value in inputs[3].values())
    assert OLD["sourceSha"] not in json.dumps(wire["variables"])
    assert wire["writes"][0][2] == {
        "deployment_branch_policy": {"protected_branches": False, "custom_branch_policies": True}}


def test_preview_environment_has_no_refresh_credentials_or_cloud_write_token(wire, tmp_path):
    inputs = setup("preview")
    result = configure(inputs, tmp_path)
    assert result["refreshEnabled"] is False and not wire["secrets"]
    assert len(wire["writes"]) == 4 and state(inputs[1].store)["writer"] is not None
    assert wire["registry"] is None


def test_existing_foreign_environment_is_never_imported_or_overwritten(wire, tmp_path):
    wire["environment"] = {"id": 21, "name": "testing",
        "url": "https://api.github.com/repos/fixture/easyproxy/environments/testing"}
    with pytest.raises(GitHubError, match="UNOWNED_ENVIRONMENT"):
        configure(setup(), tmp_path)
    assert not wire["writes"]


def test_update_checks_ownership_then_reencrypts_existing_secret_names(wire, tmp_path):
    inputs = setup()
    configure(inputs, tmp_path)
    config, owner, permit, secrets, _ = inputs
    owner.commit_verified(permit, CHECKS["deploy"])
    update = owner.begin("update", NEW, previous=OLD)
    count = len(wire["writes"])
    configure((config, owner, update, secrets, Sealer(Path("/fixture/sealer"), NEW["sourceSha"], "f" * 64)), tmp_path)
    assert len(wire["writes"]) - count == len(secrets)
    assert state(owner.store)["active"] == OLD and state(owner.store)["writer"]["candidate"] == NEW


@pytest.mark.parametrize("mutation", ["variables", "secret", "policy", "environment-id"])
def test_update_refuses_foreign_or_drifted_ownership_without_writes(wire, tmp_path, mutation):
    inputs = setup()
    configure(inputs, tmp_path)
    config, owner, permit, secrets, _ = inputs
    owner.commit_verified(permit, CHECKS["deploy"])
    update = owner.begin("update", NEW, previous=OLD)
    if mutation == "variables":
        wire["variables"][deployment.OWNER_VARIABLE] = "foreign"
    elif mutation == "secret":
        wire["secrets"]["FOREIGN_TOKEN"] = {}
    elif mutation == "policy":
        wire["policies"][0]["name"] = "*"
    else:
        wire["environment"]["id"] += 1
    count = len(wire["writes"])
    with pytest.raises(GitHubError):
        configure((config, owner, update, secrets, Sealer(Path("/fixture/sealer"), NEW["sourceSha"], "f" * 64)), tmp_path)
    assert len(wire["writes"]) == count


@pytest.mark.parametrize("mode", ["server-error", "redirect", "response-lost", "duplicate-json"])
def test_unknown_write_is_single_send_never_replayed_or_unlocked(wire, tmp_path, mode):
    inputs = setup()
    wire["mode"] = mode
    with pytest.raises(GitHubError) as failure:
        configure(inputs, tmp_path)
    assert "private-provider-message" not in str(failure.value)
    assert len(wire["writes"]) == 1 and state(inputs[1].store)["writer"] is not None
    wire["mode"] = "normal"
    with pytest.raises((GitHubError, CoordinationError)):
        configure(inputs, tmp_path)
    assert len(wire["writes"]) == 1


@pytest.mark.parametrize("field,value", [("admin", False), ("defaultBranch", "release/*"),
    ("defaultBranch", "../main"), ("defaultBranch", "main\n")])
def test_repository_admin_and_literal_default_branch_are_checked_before_write(wire, tmp_path, field, value):
    wire[field] = value
    with pytest.raises(GitHubError):
        configure(setup(), tmp_path)
    assert not wire["writes"]


def test_bad_environment_response_identity_stops_before_mutation(wire, tmp_path):
    inputs = setup()
    configure(inputs, tmp_path)
    wire["mode"] = "wrong-environment"
    count = len(wire["writes"])
    with pytest.raises(GitHubError, match="ENVIRONMENT"):
        configure(inputs, tmp_path)
    assert len(wire["writes"]) == count


def test_unexpected_or_placeholder_credentials_are_rejected_before_network(wire, tmp_path):
    config, owner, permit, secrets, sealer = setup()
    secrets["CLOUDFLARE_API_TOKEN"] = "must-not-enter-refresh"
    with pytest.raises(GitHubError, match="CREDENTIALS"):
        configure((config, owner, permit, secrets, sealer), tmp_path)
    assert not wire["requests"]
    secrets.pop("CLOUDFLARE_API_TOKEN")
    secrets["EASYPROXY_ECH_TOKEN"] = "verification-not-applicable"
    with pytest.raises(GitHubError, match="CREDENTIALS"):
        configure((config, owner, permit, secrets, sealer), tmp_path)
    assert not wire["requests"]


def test_stage_reentry_cannot_repeat_environment_creation_even_when_it_was_not_applied(wire, tmp_path):
    inputs = setup()
    inputs[1].claim_stage(inputs[2], "github-environment")
    with pytest.raises(CoordinationError, match="STAGE_ALREADY_ATTEMPTED"):
        configure(inputs, tmp_path)
    assert not wire["writes"]


@pytest.mark.parametrize("mode", ["wrong-workflow", "disabled-workflow"])
def test_unpublished_drifted_or_disabled_workflow_is_not_called_a_configured_refresh(wire, tmp_path, mode):
    wire["mode"] = mode
    with pytest.raises(GitHubError, match="WORKFLOW"):
        configure(setup(), tmp_path)
    assert not wire["writes"]


def test_malformed_foreign_registry_is_rejected_without_writing(wire, tmp_path):
    wire["registry"] = json.dumps({"schemaVersion": 1, "repository": "foreign/repo", "environments": []})
    with pytest.raises(RuntimeError, match="REGISTRY_REJECTED"):
        configure(setup(), tmp_path)
    assert not wire["writes"]


def test_shared_registry_preserves_other_production_environments(wire, tmp_path):
    wire["registry"] = json.dumps({"schemaVersion": 1, "repository": "fixture/easyproxy", "environments": ["other"]})
    configure(setup(), tmp_path)
    assert json.loads(wire["registry"])["environments"] == ["other", "testing"]


def test_update_never_silently_reenables_a_removed_registry_entry(wire, tmp_path):
    inputs = setup()
    configure(inputs, tmp_path)
    config, owner, permit, secrets, _ = inputs
    owner.commit_verified(permit, CHECKS["deploy"])
    update = owner.begin("update", NEW, previous=OLD)
    wire["registry"] = None
    count = len(wire["writes"])
    with pytest.raises(GitHubError, match="REGISTRY_DRIFT"):
        configure((config, owner, update, secrets, Sealer(Path("/fixture/sealer"), NEW["sourceSha"], "f" * 64)), tmp_path)
    assert len(wire["writes"]) == count
