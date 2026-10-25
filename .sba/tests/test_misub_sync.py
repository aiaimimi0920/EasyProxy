"""真实 Requests wire；仅本机合成 MiSub，不接触生产或输出节点/凭据。"""
import copy
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
from pathlib import Path
import socket
import sys
import threading
from urllib.parse import urlsplit

import pytest
import requests

sys.path.insert(0, str(Path(__file__).parents[1]))
import misub_sync as sync
from aggregator_publish import Candidate
from coordination import Coordinator, CoordinationError, inventory_digest
from test_aggregator_publish import BODIES, Public, audit, URI
from test_coordination import CHECKS, IDENTITY, MemoryStore, OLD, state

ROOT = Path(__file__).resolve().parents[2]
PROJECT, WORKER = "easyproxy-fixture-misub", "fixture"
INVENTORY = [{"kind": "pages", "name": PROJECT, "id": PROJECT},
             {"kind": "worker", "name": WORKER, "id": WORKER},
             {"kind": "r2", "name": IDENTITY["bucket"], "id": IDENTITY["bucket"]}]


def baseline():
    return {"subscriptions": [{"id": "user-source", "kind": "subscription", "enabled": False,
                               "input": "https://user.invalid/sub", "url": "https://user.invalid/sub", "custom": "keep"},
                              {"id": "user-connector", "kind": "connector", "enabled": True, "input": "https://other.invalid/",
                               "options": {"connector_type": "ech_worker", "connector_config": {"access_token": "private-other"}}}],
            "profiles": [{"id": "custom-profile", "enabled": False, "manualNodes": [], "description": "keep"},
                         {"id": "aggregator_global", "customId": "aggregator-global", "enabled": True,
                          "isPublic": True, "expiresAt": "2030-01-01", "manualNodes": ["user-connector"], "custom": "keep"}],
            "settings": {"aggregatorSync": {"enabled": False, "runOnCron": False}},
            "ruleTemplates": [{"id": "user-rule", "content": "keep"}]}


@pytest.fixture
def wire(monkeypatch):
    record = {"requests": [], "saves": [], "data": baseline(), "mode": "normal", "exports": 0, "errors": []}

    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *_args):
            pass

        def respond(self, payload, status=200):
            body = json.dumps(payload).encode()
            self.send_response(status)
            self.send_header("Content-Length", str(len(body)))
            if self.path == "/api/login":
                self.send_header("Set-Cookie", "auth_session=fixture-cookie; Path=/; Secure; HttpOnly")
            if status == 307:
                self.send_header("Location", "https://unapproved.invalid/private")
            self.end_headers()
            self.wfile.write(body)

        def do_POST(self):
            payload = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
            record["requests"].append(("POST", self.path, dict(self.headers)))
            if self.path == "/api/login":
                assert payload == {"password": "fixture-admin"}
                return self.respond({"success": True})
            assert "auth_session=fixture-cookie" in self.headers.get("Cookie", "")
            if self.path == "/api/backup/export":
                record["exports"] += 1
                data = copy.deepcopy(record["data"])
                if record["mode"] == "concurrent" and record["exports"] == 2:
                    data["subscriptions"][0]["custom"] = "concurrent-user-edit"
                return self.respond({"success": True, "exportData": {"app": "MiSub", "type": "misub-backup", "version": 1,
                    "scope": "dataAndSettings", "source": {"storageType": "d1"}, "data": data}})
            assert self.path == "/api/sba/managed-sync"
            record["saves"].append(payload)
            for resource, key in (("subscriptions", "subscriptions"), ("profiles", "profiles")):
                rows = record["data"][key]
                diff = payload["diff"][resource]
                updates = {row["id"]: row for row in diff["updated"]}
                record["data"][key] = [{**row, **updates.get(row["id"], {})} for row in rows if row["id"] not in diff["removed"]] + diff["added"]
            if record["mode"] == "lost":
                self.connection.shutdown(socket.SHUT_RDWR)
                return
            if record["mode"] in ("redirect", "server-error"):
                return self.respond({"private-provider": "must-not-leak"}, 307 if record["mode"] == "redirect" else 500)
            if record["mode"] == "data-drift":
                record["data"]["subscriptions"][0]["unapproved"] = "must-be-detected"
            return self.respond({"success": True})

        def do_GET(self):
            record["requests"].append(("GET", self.path, dict(self.headers)))
            if self.path == "/api/settings":
                value = copy.deepcopy(record["data"]["settings"])
                if record["mode"] == "notifications":
                    value.update(BotToken="private-token", ChatID="private-id")
                if record["mode"] == "cron":
                    value["aggregatorSync"] = {"enabled": True, "runOnCron": True}
                return self.respond(value)
            assert self.path == "/api/manifest/aggregator-global"
            assert self.headers["Authorization"] == "Bearer fixture-manifest"
            profile = next(row for row in record["data"]["profiles"] if row.get("customId") == "aggregator-global")
            sources = [row for row in record["data"]["subscriptions"] if row["id"] in profile["manualNodes"]]
            if record["mode"] == "wrong-manifest":
                sources = sources[:-1]
            self.respond({"success": True, "profile": profile, "sources": sources})

    class Server(ThreadingHTTPServer):
        def handle_error(self, *_args):
            error = sys.exc_info()[1]
            if not isinstance(error, (BrokenPipeError, ConnectionResetError, ConnectionAbortedError)):
                record["errors"].append(type(error).__name__)

    server = Server(("127.0.0.1", 0), Handler)
    server.daemon_threads = True
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    original = requests.adapters.HTTPAdapter.send

    def local(adapter, request, **kwargs):
        url = request.url
        target = urlsplit(url)
        assert target.scheme == "https" and target.netloc == PROJECT + ".pages.dev"
        assert kwargs.get("verify") is True and kwargs.get("proxies") == {} and adapter.max_retries.total == 0
        assert request.headers["Origin"] == "https://" + PROJECT + ".pages.dev"
        request.url = f"http://127.0.0.1:{server.server_port}" + target.path
        try:
            return original(adapter, request, **kwargs)
        finally:
            request.url = url

    monkeypatch.setattr(requests.adapters.HTTPAdapter, "send", local)
    monkeypatch.setenv("HTTPS_PROXY", "http://must-not-be-used.invalid:1")
    import aggregator_publish
    monkeypatch.setattr(aggregator_publish, "inspect_checkout", lambda *args: [{"path": "upstreams/aggregator", "sourceSha": "e" * 40}])
    yield record
    server.shutdown()
    server.server_close()
    thread.join(timeout=5)
    assert not thread.is_alive() and record["errors"] == []


def held():
    owner = Coordinator(MemoryStore(), IDENTITY, inventory_digest(INVENTORY))
    permit = owner.begin("deploy", OLD)
    public = Public(None)
    return owner, permit, public, Candidate(permit.operation_id, BODIES), audit(permit, public)


def run(args):
    owner, permit, public, candidate, receipt = args
    return sync.sync_misub(owner, permit, candidate, receipt, public, ROOT, INVENTORY,
                           admin_password="fixture-admin", manifest_token="fixture-manifest", ech_token="fixture-ech")


def test_single_save_preserves_user_rows_profile_fields_and_suite_writer(wire):
    args = held()
    result = run(args)
    assert result == {"checks": {"misub-refreshed": True}, "profileId": "aggregator-global", "runtimeNodes": 1, "connectorNodes": 2}
    assert len(wire["saves"]) == 1
    assert state(args[0].store)["writer"] is not None and state(args[0].store)["active"] is None
    assert wire["data"]["subscriptions"][:2] == baseline()["subscriptions"]
    profile = wire["data"]["profiles"][1]
    assert profile["isPublic"] is True and profile["expiresAt"] == "2030-01-01" and profile["custom"] == "keep"
    assert "private" not in json.dumps(result) and URI not in json.dumps(result)
    assert all(path not in ("/api/data", "/api/cron", "/api/settings/reset", "/api/backup/restore") for _, path, _ in wire["requests"])
    assert wire["saves"][0]["diff"]["subscriptions"]["removed"] == []


def test_refresh_same_node_ids_still_binds_the_unchanged_managed_profile(wire):
    args = held()
    run(args)
    owner = args[0]
    owner.commit_verified(args[1], CHECKS["deploy"])
    permit = owner.begin("refresh", OLD, previous=OLD)
    run((owner, permit, args[2], Candidate(permit.operation_id, BODIES), audit(permit, args[2])))
    assert len(wire["saves"]) == 2
    changes = wire["saves"][1]["diff"]
    assert changes["profiles"]["added"] == [] and len(changes["profiles"]["updated"]) == 1
    assert changes["profiles"]["updated"][0]["customId"] == sync.PROFILE
    assert all(row["id"] == "proxy_runtime_node_1" for row in changes["subscriptions"]["updated"])
    assert state(owner.store)["writer"]["operationId"] == permit.operation_id


@pytest.mark.parametrize("mode", ["lost", "redirect", "server-error", "data-drift", "wrong-manifest"])
def test_unknown_never_retries_restores_or_releases_and_same_permit_cannot_save_again(wire, mode):
    wire["mode"] = mode
    args = held()
    with pytest.raises(sync.MiSubError, match="EASYPROXY_SBA_MISUB_") as error:
        run(args)
    assert "private-provider" not in str(error.value)
    assert len(wire["saves"]) == 1 and state(args[0].store)["writer"] is not None
    wire["mode"] = "normal"
    with pytest.raises(CoordinationError, match="STAGE_ALREADY_ATTEMPTED"):
        run(args)
    assert len(wire["saves"]) == 1


@pytest.mark.parametrize("mode", ["concurrent", "notifications", "cron"])
def test_preflight_rejects_changes_and_uncontrolled_side_effects_before_save(wire, mode):
    wire["mode"] = mode
    args = held()
    with pytest.raises(sync.MiSubError):
        run(args)
    assert wire["saves"] == [] and state(args[0].store)["writer"] is not None


def test_old_ech_preferred_ips_and_disabled_global_profile_are_not_silently_lost():
    data = baseline()
    data["subscriptions"].append({"id": "conn_ech_workers_pref_1", "kind": "connector",
                                   "connector_config": {"server_ip": "192.0.2.10"}})
    expected = sync.desired_state(ROOT, data, [URI], "https://fixture.account.workers.dev", "fixture-token", "sba-fixture")
    source = next(row for row in expected["subscriptions"] if row["id"] == "conn_ech_workers_pref_1")
    assert source["connector_config"]["server_ip"] == "192.0.2.10"
    data["profiles"][1]["enabled"] = False
    with pytest.raises(sync.MiSubError, match="PROFILE_DISABLED"):
        sync.desired_state(ROOT, data, [URI], "https://fixture.account.workers.dev", "fixture-token", "sba-fixture")


@pytest.mark.parametrize("value", ["verification-not-applicable", "", "bad\nvalue"])
def test_verification_placeholders_and_invalid_credentials_never_make_requests(value, monkeypatch):
    monkeypatch.setattr(requests.Session, "send", lambda *args, **kwargs: pytest.fail("must not make requests"))
    with pytest.raises(sync.MiSubError, match="CREDENTIALS"):
        sync.MiSubAPI(PROJECT, value, "fixture-manifest")
