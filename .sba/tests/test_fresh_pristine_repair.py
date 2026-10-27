"""新部署仅从完整空现场继续；所有接口均只读，任何漂移或不确定即停止。"""
from dataclasses import replace
import json
from types import SimpleNamespace
from pathlib import Path
import sys

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import fresh_pristine_repair as repair
from request import SECRETS
from test_fresh_pristine_request import approved
from test_request import parse


@pytest.fixture
def pristine(approved, monkeypatch):
    request = parse(approved)
    config = request.config
    credentials = dict.fromkeys(SECRETS, "fixture-only")
    record = SimpleNamespace(calls=[], closed=[], mutation=None,
        registry=json.dumps({"schemaVersion": 1, "repository": config.repository, "environments": []}))
    def observed(name, result):
        record.calls.append(name)
        if record.mutation == name + "-error": raise OSError("PRIVATE_PROVIDER_RESPONSE")
        return result
    def receipt(token):
        assert token == credentials["EASYPROXY_GITHUB_TOKEN"]
        observed("parent", None)
    class Store:
        client = SimpleNamespace(close=lambda: record.closed.append("r2"))
        def read(self):
            return observed("control", (b"existing", '"' + "a" * 32 + '"') if record.mutation == "control" else None)
        def is_empty(self): return observed("objects", record.mutation != "object")
    class Database:
        def __init__(self, *_): pass
        def close(self): record.closed.append("database")
        def identity(self):
            return observed("database", {"uuid": "wrong" if record.mutation == "d1-id" else config.database_id,
                "name": "wrong" if record.mutation == "d1-name" else config.database_name})
        def query(self, sql):
            assert sql == repair.SCHEMA_QUERY, "never execute DDL/DML in pristine checks"
            return observed("schema", [{"type": "table", "name": "private", "tbl_name": "private",
                "sql": "CREATE TABLE private (id)"}] if record.mutation == "schema" else [])
    class Worker:
        def __init__(self, *_): pass
        def close(self): record.closed.append("worker")
        def settings_or_absent(self, name):
            assert name == config.worker_name
            return observed("worker", {} if record.mutation == "worker" else None)
        def account_subdomain(self):
            return observed("subdomain", "wrong" if record.mutation == "subdomain" else config.workers_subdomain)
    class Pages:
        def __init__(self, *_): pass
        def close(self): record.closed.append("pages")
        def project(self): return observed("pages", {} if record.mutation == "pages" else None)
    class Github:
        def __init__(self, *_): pass
        def close(self): record.closed.append("github")
        def repository_info(self): return observed("github", {"id": 1})
        def environment_info(self): return observed("environment", {} if record.mutation == "environment" else None)
        def repository_variable(self, name):
            assert name == "EASYPROXY_SBA_REFRESH_REGISTRY"
            return observed("registry", json.dumps({"schemaVersion": 1, "repository": config.repository,
                "environments": ["other"]}) if record.mutation == "registry" else record.registry)
        def active_workflow(self): observed("workflow", None)
    def domains(current, token, *, existing):
        assert current is config and token == credentials["EASYPROXY_CLOUDFLARE_API_TOKEN"] and existing is False
        observed("domains", None)
        if record.mutation in ("dns", "worker-domain", "pages-domain"):
            raise RuntimeError("PRIVATE_DOMAIN_DRIFT")
    monkeypatch.setattr(repair, "verify_parent_receipt", receipt)
    monkeypatch.setattr(repair, "preflight_domains", domains)
    monkeypatch.setattr(repair, "r2_store", lambda *_: Store())
    for name, value in (("D1API", Database), ("WorkerAPI", Worker), ("PagesAPI", Pages), ("GitHubAPI", Github)):
        monkeypatch.setattr(repair, name, value)
    return request, credentials, record


def test_all_resource_checks_are_read_only_and_clients_close(pristine):
    request, credentials, record = pristine
    repair.assert_fresh_pristine(request, credentials)
    assert record.calls == ["parent", "control", "objects", "database", "schema", "worker", "subdomain",
        "pages", "github", "environment", "registry", "workflow", "domains"]
    assert record.closed == ["r2", "database", "worker", "pages", "github"]


@pytest.mark.parametrize("mutation", ["parent-error", "control", "object", "d1-id", "d1-name", "schema",
    "worker", "subdomain", "pages", "environment", "registry", "dns", "worker-domain", "pages-domain",
    "control-error", "objects-error", "database-error", "schema-error", "worker-error", "subdomain-error",
    "pages-error", "github-error", "environment-error", "registry-error", "workflow-error", "domains-error"])
def test_any_drift_or_uncertain_read_stops_without_writes(pristine, mutation):
    request, credentials, record = pristine
    record.mutation = mutation
    with pytest.raises((repair.FreshPristineError, RuntimeError, OSError)):
        repair.assert_fresh_pristine(request, credentials)
    if mutation == "parent-error": assert record.calls == ["parent"] and record.closed == []
    elif mutation in ("control", "object", "control-error", "objects-error"): assert record.closed == ["r2"]
    else: assert record.closed


@pytest.mark.parametrize("mutation", ["action", "previous", "context", "config", "float-run"])
def test_direct_entry_cannot_bypass_request_binding(pristine, mutation):
    request, credentials, record = pristine
    if mutation == "action": current = replace(request, action="deploy")
    elif mutation == "previous": current = replace(request, previous={**request.previous, "sourceSha": "f" * 40})
    elif mutation == "context": current = replace(request, context={**request.context, "requestDigest": "f" * 64})
    elif mutation == "float-run": current = replace(request, context={**request.context, "parentRunId": float(request.context["parentRunId"])})
    else: current = replace(request, config=replace(request.config, bucket="other-bucket"))
    with pytest.raises(repair.FreshPristineError): repair.assert_fresh_pristine(current, credentials)
    assert record.calls == [] and record.closed == []


@pytest.mark.parametrize("value", [None, "[]", "{}", "null", "private-invalid",
    '{"schemaVersion":true,"repository":"aiaimimi0920/easyproxy","environments":[]}',
    '{"schemaVersion":1,"repository":"other/easyproxy","environments":[]}',
    '{"schemaVersion":1,"repository":"aiaimimi0920/easyproxy","environments":[],"extra":true}',
    '{"schemaVersion":1,"schemaVersion":1,"repository":"aiaimimi0920/easyproxy","environments":[]}'])
def test_existing_empty_registry_requires_the_real_strict_repository_contract(pristine, value):
    request, credentials, record = pristine
    record.registry = value
    with pytest.raises(RuntimeError): repair.assert_fresh_pristine(request, credentials)
    assert record.closed == ["r2", "database", "worker", "pages", "github"] and "domains" not in record.calls
