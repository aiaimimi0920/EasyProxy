"""新实例精确资源的只读 API 合同；域名只记录新证据，不发送 attach。"""
import copy
import hashlib
import json
from pathlib import Path
import sys
from types import SimpleNamespace

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import fresh_domain_resources as resources
from coordination import Coordinator, _encode, StoreUnknown, CoordinationError
from fresh_domain_resume import FreshDomainError
from test_coordination import MemoryStore
from test_domains import cloud, config, ACCOUNT, ZONE, WORKER


def existing_domain(cloud):
    host = "edge.example.com"
    cloud.workers[host] = {"id": "a" * 40, "hostname": host, "service": WORKER, "zone_id": ZONE,
        "zone_name": "example.com", "environment": "production", "enabled": True, "previews_enabled": False}
    cloud.dns[host] = {"id": "b" * 32, "name": host, "type": "AAAA", "content": "100::", "proxied": True}


@pytest.mark.parametrize("mutation", [None, "domain-id", "dns-id", "service", "environment", "dns-content", "foreign-pages"])
def test_domain_owner_ids_and_dns_are_exact_read_only_before_new_proof(cloud, monkeypatch, mutation):
    value = config()
    store = MemoryStore(); store.bucket = value.bucket
    owner = Coordinator(store, value.identity("production"), value.inventory_digest)
    permit = owner.begin("deploy", {"sourceSha": "f" * 40, "applicationVersion": "1.0.13"})
    existing_domain(cloud)
    identities = {"zoneId": ZONE, "domainId": "a" * 40, "dnsId": "b" * 32}
    monkeypatch.setattr(resources, "DOMAIN_IDENTITY_SHA256", hashlib.sha256(_encode(identities)).hexdigest())
    host = "edge.example.com"
    if mutation == "domain-id": cloud.workers[host]["id"] = "f" * 40
    elif mutation == "dns-id": cloud.dns[host]["id"] = "f" * 32
    elif mutation == "service": cloud.workers[host]["service"] = "foreign-worker"
    elif mutation == "environment": cloud.read_environment = ""
    elif mutation == "dns-content": cloud.dns[host]["content"] = "2001:db8::1"
    elif mutation == "foreign-pages": cloud.pages[host] = {"name": host}
    if mutation:
        with pytest.raises(RuntimeError): resources.record_worker_domain(owner, permit, value.inventory, "fixture-api-token")
        assert store.stages == {}
    else:
        resources.record_worker_domain(owner, permit, value.inventory, "fixture-api-token")
        evidence = json.loads(store.stages[(permit.operation_id, "worker-domain-owned")])
        assert {k: evidence[k] for k in identities} == identities
        assert evidence["accountId"] == ACCOUNT and evidence["inventoryDigest"] == value.inventory_digest
        with pytest.raises(CoordinationError): resources.record_worker_domain(owner, permit, value.inventory, "fixture-api-token")
        assert len(store.stages) == 2
    assert cloud.writes == [] and all(row[0] == "GET" for row in cloud.requests)


@pytest.mark.parametrize("mutation", [None, "repository", "workflow", "environment", "variables", "secrets", "registry", "policy"])
def test_new_environment_is_complete_read_only_and_bound_to_parent(monkeypatch, tmp_path, mutation):
    from github_environment import variables, REFRESH_SECRETS
    value = config()
    owner = SimpleNamespace(identity=value.identity("production"), inventory_digest=value.inventory_digest)
    expected = variables(owner, value, 123, resources.ENVIRONMENT_ID)
    calls = []
    monkeypatch.setattr(resources, "reviewed_workflow", lambda _: b"approved-workflow")
    class API:
        def __init__(self, *_): pass
        def close(self): calls.append("closed")
        def repository_info(self): return {"id": 123, "defaultBranch": "other" if mutation == "repository" else "main"}
        def workflow_at(self, ref):
            calls.append(ref)
            return b"changed" if mutation == "workflow" else b"approved-workflow"
        def active_workflow(self): calls.append("active-workflow")
        def environment_info(self): return {"id": 0 if mutation == "environment" else resources.ENVIRONMENT_ID,
            "deployment_branch_policy": {"protected_branches": False, "custom_branch_policies": True},
            "protection_rules": [{"type": "branch_policy"}]}
        def policies(self): return [{"id": 1, "name": "other" if mutation == "policy" else "main", "type": "branch"}]
        def variables(self): return {} if mutation == "variables" else expected
        def secret_names(self): return set() if mutation == "secrets" else REFRESH_SECRETS
        def repository_variable(self, _): return json.dumps({"schemaVersion": 1, "repository": value.repository,
            "environments": [] if mutation == "registry" else [value.environment]})
        # Deliberately no mutation methods.
    monkeypatch.setattr(resources, "GitHubAPI", API)
    if mutation:
        with pytest.raises(RuntimeError): resources.verify_configured(owner, value, tmp_path, "fixture-only")
    else:
        assert resources.verify_configured(owner, value, tmp_path, "fixture-only")["refreshEnabled"] is True
        assert resources.PARENT["sourceSha"] in calls
    assert calls[-1] == "closed"


@pytest.mark.parametrize("mutation", [None, "subdomain", "settings", "bundle", "provider", "custom", "provider-schema", "custom-schema"])
def test_parent_worker_has_exact_bundle_bindings_and_both_public_versions(monkeypatch, mutation):
    from ech import metadata
    value = config()
    owner = SimpleNamespace(identity=value.identity("production"), inventory_digest=value.inventory_digest)
    expected = metadata(owner, resources.PARENT, "fixture-token")
    calls = []
    class API:
        def __init__(self, *_): pass
        def close(self): calls.append("closed")
        def account_subdomain(self): return "other" if mutation == "subdomain" else value.workers_subdomain
        def settings(self, _):
            result = copy.deepcopy(expected)
            if mutation == "settings": result["bindings"][0]["bucket_name"] = "other"
            return result
        def script_subdomain(self, _): calls.append("subdomain-read")
    monkeypatch.setattr(resources, "WorkerAPI", API)
    monkeypatch.setattr(resources, "worker_bundle_digest", lambda *_: "f" * 64 if mutation == "bundle" else resources.WORKER_BUNDLE_SHA256)
    def public(origin):
        calls.append(origin)
        channel = "custom" if origin == value.worker_origin else "provider"
        bad = mutation == channel
        return {"schemaVersion": True if mutation == channel + "-schema" else 1, "applicationId": "easyproxy-cloud", **resources.PARENT,
                **({"sourceSha": "f" * 40} if bad else {})}
    monkeypatch.setattr(resources, "public_release", public)
    if mutation:
        with pytest.raises(RuntimeError): resources.verify_existing_worker(owner, value, "fixture-api", "fixture-token")
    else:
        resources.verify_existing_worker(owner, value, "fixture-api", "fixture-token")
        assert value.worker_origin in calls and any(".workers.dev" in row for row in calls)
    assert calls[-1] == "closed"


@pytest.mark.parametrize("failure", ["unknown", "bad-etag"])
def test_new_domain_proof_unknown_cannot_be_replayed(cloud, monkeypatch, failure):
    value = config()
    store = MemoryStore(); store.bucket = value.bucket
    owner = Coordinator(store, value.identity("production"), value.inventory_digest)
    permit = owner.begin("deploy", {"sourceSha": "f" * 40, "applicationVersion": "1.0.13"})
    monkeypatch.setattr(resources, "verify_worker_domain", lambda *_: {"fixture": True})
    original = store.create_stage
    def create(operation, stage, body):
        etag = original(operation, stage, body)
        if stage == "worker-domain-owned":
            if failure == "unknown": raise StoreUnknown()
            return None
        return etag
    monkeypatch.setattr(store, "create_stage", create)
    with pytest.raises((StoreUnknown, FreshDomainError)):
        resources.record_worker_domain(owner, permit, value.inventory, "fixture-api-token")
    before = dict(store.stages)
    with pytest.raises(CoordinationError): resources.record_worker_domain(owner, permit, value.inventory, "fixture-api-token")
    assert store.stages == before and json.loads(store.body)["writer"] is not None and cloud.writes == []
