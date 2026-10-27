"""自定义域名真实传输边界的合成 API；不访问真实云账号。"""
import copy
import json
from pathlib import Path
import sys
from urllib.parse import urlsplit

import pytest
import requests

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import domains
from cloudflare_http import api_result
from origins import inventory_origin
from suite_config import parse_configuration
from test_suite_config import PUBLIC
from test_coordination import coordination, MemoryStore, NEW, OLD, CHECKS
from aggregator_publish import PublicArtifacts


ACCOUNT, ZONE = "c" * 32, "d" * 32
WORKER, PAGES = "ep-testing-edge", "ep-testing-misub"


def config():
    return parse_configuration({**PUBLIC, "pagesOrigin": "https://app.example.com",
        "workerOrigin": "https://edge.example.com"}, repository="fixture/easyproxy", environment="testing")


def held(action="deploy"):
    value, store = config(), MemoryStore()
    store.bucket = value.bucket
    owner = coordination.Coordinator(store, value.identity("production"), value.inventory_digest)
    if action == "update":
        first = owner.begin("deploy", OLD)
        owner.commit_verified(first, CHECKS["deploy"])
    return owner, owner.begin(action, NEW, previous=OLD if action == "update" else None), store


@pytest.fixture
def cloud(monkeypatch):
    class Cloud:
        def __init__(self):
            self.requests, self.writes = [], []
            self.dns, self.workers, self.pages = {}, {}, {}
            self.mode, self.pending_reads = "normal", 0
            self.attach_environment, self.read_environment = "production", "production"

        def request(self, session, method, url, **options):
            assert session.trust_env is False
            assert session.get_adapter(url).max_retries.total == 0
            assert options["headers"] == {"Authorization": "Bearer fixture-api-token"}
            assert options["allow_redirects"] is False and options["stream"] is True
            path = urlsplit(url).path.removeprefix("/client/v4")
            assert urlsplit(url).netloc == "api.cloudflare.com"
            self.requests.append((method, path, copy.deepcopy(options.get("params"))))
            body, query = options.get("json"), options.get("params", {})
            if method != "GET":
                self.writes.append((method, path, copy.deepcopy(body)))
            if path == "/zones":
                result = [{"id": ZONE, "name": "example.com", "status": "active", "account": {"id": ACCOUNT}}]
                if self.mode == "foreign-account": result[0]["account"]["id"] = "f" * 32
                if self.mode == "pending-zone": result[0]["status"] = "pending"
            elif path.endswith("/dns_records"):
                if method == "POST":
                    self.dns[body["name"]] = {**body, "id": "e" * 32}
                    result = self.dns[body["name"]]
                else:
                    result = [self.dns[query["name"]]] if query["name"] in self.dns else []
            elif path.endswith("/workers/domains"):
                if method == "PUT":
                    result = {**body, "id": "a" * 40, "zone_name": "example.com", "environment": "production"}
                    self.workers[body["hostname"]] = result
                    self.dns[body["hostname"]] = {"id": "b" * 32, "type": "AAAA", "content": "100::",
                        "name": body["hostname"], "proxied": True}
                else:
                    result = [self.workers[query["hostname"]]] if query["hostname"] in self.workers else []
                    result = [{**row, "environment": self.read_environment} for row in result]
            elif path.endswith("/pages/projects"):
                result = [{"name": PAGES, "domains": [PAGES + ".pages.dev", *self.pages]}]
                if self.mode == "foreign-pages": result[0]["name"] = "foreign-project"
            elif path.endswith("/domains") and method == "POST":
                result = {"id": "f" * 32, "domain_id": "f" * 32, "name": body["name"],
                    "zone_tag": ZONE, "status": "pending"}
                self.pages[body["name"]] = result
            elif "/pages/projects/" in path and "/domains/" in path:
                name = path.rsplit("/", 1)[1]
                result = copy.deepcopy(self.pages[name])
                self.pending_reads += 1
                if self.mode != "never-ready" and self.pending_reads >= 2:
                    result["status"] = "active"
            else:
                raise AssertionError((method, path))
            if method != "GET" and self.mode == "lost-response":
                raise requests.ConnectionError("PRIVATE_PROVIDER_TOKEN")
            if method != "GET" and self.mode == "wrong-write-identity":
                result = {**result, "id": "invalid", "service": "foreign"}
            if method == "PUT" and path.endswith("/workers/domains"):
                # Script-based attach can return a blank deprecated environment;
                # subsequent GET still returns the canonical production value.
                result = {**result, "environment": self.attach_environment}
            raw = json.dumps({"success": True, "errors": None if "/workers/domains" in path else [],
                "result": result}).encode()
            if self.mode == "direct-worker-domain" and method == "PUT" and path.endswith("/workers/domains"):
                raw = json.dumps(result).encode()
            if self.mode == "direct-pages-domain" and "/pages/projects/" in path and "/domains" in path:
                raw = json.dumps(result).encode()

            class Response:
                status_code = 200
                def __enter__(self): return self
                def __exit__(self, *_): pass
                def iter_content(self, _size): yield raw
            return Response()

    value = Cloud()
    monkeypatch.setattr(requests.Session, "request", lambda session, *args, **kwargs: value.request(session, *args, **kwargs))
    monkeypatch.setattr(domains.time, "sleep", lambda _: None)
    return value


def test_custom_configuration_roundtrip_binds_exact_origins_without_changing_old_digest():
    old = parse_configuration(PUBLIC, repository="fixture/easyproxy", environment="testing")
    value = config()
    assert len(old.inventory) == 5 and len(value.inventory) == 7
    assert old.inventory_digest != value.inventory_digest
    assert parse_configuration(value.configuration(), repository=value.repository, environment=value.environment) == value
    assert inventory_origin(value.inventory, PAGES, "https://" + PAGES + ".pages.dev") == value.pages_origin
    assert {r["id"] for r in value.inventory if r["kind"] == "domain"} == {value.pages_origin, value.worker_origin}


@pytest.mark.parametrize("origin", ["http://app.example.com", "https://APP.example.com", "https://app.example.com/",
    "https://app.example.com:443", "https://user@app.example.com", "https://app.example.com?x=y",
    "https://app.example.com#x", "https://app.example.com.", "https://127.0.0.1", "https://app.localhost",
    "https://foreign.pages.dev", "https://wrong.foreign.workers.dev", "https://*.example.com"])
def test_noncanonical_and_foreign_provider_origins_rejected_before_network(origin):
    from suite_config import ConfigurationError
    with pytest.raises(ConfigurationError):
        parse_configuration({**PUBLIC, "pagesOrigin": origin}, repository="fixture/easyproxy", environment="testing")


def test_identical_custom_origins_are_rejected():
    from suite_config import ConfigurationError
    with pytest.raises(ConfigurationError):
        parse_configuration({**PUBLIC, "pagesOrigin": "https://app.example.com", "workerOrigin": "https://app.example.com"},
                            repository="fixture/easyproxy", environment="testing")


@pytest.mark.parametrize("mode", ["foreign-account", "pending-zone"])
def test_preflight_requires_exact_active_zone_account_and_never_writes(cloud, mode):
    cloud.mode = mode
    with pytest.raises(domains.DomainError, match="ZONE"):
        domains.preflight_domains(config(), "fixture-api-token", existing=False)
    assert cloud.writes == []


@pytest.mark.parametrize("kind", ["dns", "workers", "pages"])
def test_existing_foreign_association_is_not_adopted_or_overwritten(cloud, kind):
    target = domains.targets(config())[0]
    if kind == "dns": cloud.dns[target.hostname] = {"name": target.hostname}
    elif kind == "workers": cloud.workers[target.hostname] = {"hostname": target.hostname}
    else: cloud.pages[target.hostname] = {"name": target.hostname}
    with pytest.raises(domains.DomainError, match="UNOWNED_RESOURCE"):
        domains.preflight_domains(config(), "fixture-api-token", existing=False)
    assert cloud.writes == []


def test_two_domains_single_creation_readback_and_durable_identity_evidence(cloud):
    owner, permit, store = held()
    domains.preflight_domains(config(), "fixture-api-token", existing=False)
    assert cloud.writes == []
    for target in domains.targets(config()):
        domains.bind_domain(owner, permit, config().inventory, target.service, target.kind, "fixture-api-token")
    assert [r[0] for r in cloud.writes] == ["PUT", "POST", "POST"]
    assert cloud.writes[0][2]["hostname"] == "edge.example.com"
    assert cloud.writes[1][2] == {"name": "app.example.com"}
    assert cloud.writes[2][2]["content"] == PAGES + ".pages.dev"
    for kind in ("worker", "pages"):
        evidence = json.loads(store.stages[(permit.operation_id, kind + "-domain-owned")])
        assert evidence["zoneId"] == ZONE and evidence["inventoryDigest"] == config().inventory_digest
        assert evidence["accountId"] == ACCOUNT and evidence["dnsId"]
    before = copy.deepcopy(cloud.writes)
    with pytest.raises(domains.DomainError):
        domains.bind_domain(owner, permit, config().inventory, WORKER, "worker", "fixture-api-token")
    assert cloud.writes == before
    assert json.loads(store.body)["writer"] is not None


@pytest.mark.parametrize("kind", ["worker", "pages"])
@pytest.mark.parametrize("mode", ["lost-response", "wrong-write-identity"])
def test_unknown_write_stops_without_replay_or_dns_followup(cloud, kind, mode):
    cloud.mode = mode
    owner, permit, store = held()
    target = next(t for t in domains.targets(config()) if t.kind == kind)
    with pytest.raises(domains.DomainError) as error:
        domains.bind_domain(owner, permit, config().inventory, target.service, kind, "fixture-api-token")
    assert "PRIVATE" not in str(error.value)
    assert len(cloud.writes) == 1
    assert (permit.operation_id, kind + "-domain-owned") not in store.stages
    assert json.loads(store.body)["writer"] is not None


def test_update_verifies_existing_domain_ownership_without_domain_mutations(cloud):
    owner, permit, _ = held()
    for target in domains.targets(config()):
        domains.bind_domain(owner, permit, config().inventory, target.service, target.kind, "fixture-api-token")
    count = len(cloud.writes)
    update_owner, update, _ = held("update")
    domains.preflight_domains(config(), "fixture-api-token", existing=True)
    for target in domains.targets(config()):
        domains.bind_domain(update_owner, update, config().inventory, target.service, target.kind, "fixture-api-token")
    assert len(cloud.writes) == count
    cloud.workers["edge.example.com"]["service"] = "foreign-worker"
    with pytest.raises(domains.DomainError, match="OWNERSHIP"):
        domains.preflight_domains(config(), "fixture-api-token", existing=True)
    assert len(cloud.writes) == count


def test_certificate_wait_is_bounded_get_only_and_never_records_false_success(cloud):
    cloud.mode = "never-ready"
    owner, permit, store = held()
    with pytest.raises(domains.DomainError, match="NOT_READY"):
        domains.bind_domain(owner, permit, config().inventory, PAGES, "pages", "fixture-api-token")
    assert [r[0] for r in cloud.writes] == ["POST", "POST"]
    assert cloud.pending_reads == 120
    assert (permit.operation_id, "pages-domain-owned") not in store.stages


def test_worker_domain_null_errors_exception_cannot_be_used_for_worker_upload():
    with pytest.raises(domains.DomainError, match="IDENTITY"):
        api_result(None, "fixture", "PUT", f"/accounts/{ACCOUNT}/workers/scripts/worker", domains.require,
                   worker_domain_response=True)


def test_worker_domain_real_40hex_identity_does_not_relax_zone_or_dns_ids(cloud):
    owner, permit, store = held()
    domains.bind_domain(owner, permit, config().inventory, WORKER, "worker", "fixture-api-token")
    evidence = json.loads(store.stages[(permit.operation_id, "worker-domain-owned")])
    assert evidence["domainId"] == "a" * 40
    with pytest.raises(domains.DomainError, match="IDENTITY"):
        domains._id("a" * 40)
    with pytest.raises(domains.DomainError, match="IDENTITY"):
        domains._id("a" * 32, worker=True)


def test_documented_direct_worker_domain_put_response_still_requires_exact_owner(cloud):
    cloud.mode = "direct-worker-domain"
    owner, permit, store = held()
    domains.bind_domain(owner, permit, config().inventory, WORKER, "worker", "fixture-api-token")
    assert len(cloud.writes) == 1
    assert json.loads(store.stages[(permit.operation_id, "worker-domain-owned")])["domainId"] == "a" * 40


@pytest.mark.parametrize("mode", ["normal", "direct-worker-domain"])
def test_worker_domain_empty_attach_environment_requires_strict_production_readback(cloud, mode):
    cloud.mode, cloud.attach_environment = mode, ""
    owner, permit, store = held()
    domains.bind_domain(owner, permit, config().inventory, WORKER, "worker", "fixture-api-token")
    assert len(cloud.writes) == 1
    assert json.loads(store.stages[(permit.operation_id, "worker-domain-owned")])["domainId"] == "a" * 40
    assert json.loads(store.body)["writer"] is not None


@pytest.mark.parametrize("environment", [None, False, True, 0, "staging", "prod"])
def test_worker_domain_attach_rejects_foreign_or_untyped_environment_without_replay(cloud, environment):
    cloud.attach_environment = environment
    owner, permit, store = held()
    with pytest.raises(domains.DomainError, match="OWNERSHIP"):
        domains.bind_domain(owner, permit, config().inventory, WORKER, "worker", "fixture-api-token")
    assert len(cloud.writes) == 1
    assert (permit.operation_id, "worker-domain-owned") not in store.stages
    assert json.loads(store.body)["writer"] is not None


@pytest.mark.parametrize("environment", ["", None, False, "staging"])
def test_worker_domain_get_never_accepts_blank_or_foreign_environment(cloud, environment):
    cloud.read_environment = environment
    owner, permit, store = held()
    with pytest.raises(domains.DomainError, match="OWNERSHIP"):
        domains.bind_domain(owner, permit, config().inventory, WORKER, "worker", "fixture-api-token")
    assert len(cloud.writes) == 1
    assert (permit.operation_id, "worker-domain-owned") not in store.stages
    assert json.loads(store.body)["writer"] is not None


def test_documented_direct_pages_domain_response_has_single_create_and_verified_dns(cloud):
    cloud.mode = "direct-pages-domain"
    owner, permit, store = held()
    domains.bind_domain(owner, permit, config().inventory, PAGES, "pages", "fixture-api-token")
    assert [r[0] for r in cloud.writes] == ["POST", "POST"]
    assert json.loads(store.stages[(permit.operation_id, "pages-domain-owned")])["domainId"] == "f" * 32


def test_pages_domain_wire_exception_cannot_be_used_for_project_or_deployment():
    with pytest.raises(domains.DomainError, match="IDENTITY"):
        api_result(None, "fixture", "POST", f"/accounts/{ACCOUNT}/pages/projects/{PAGES}/deployments",
                   domains.require, pages_domain_response=True)


@pytest.mark.parametrize("failure", [None, "invalid-etag", "lost-receipt"])
def test_domain_evidence_requires_known_exact_etag_and_never_replays(cloud, failure):
    owner, permit, store = held()
    original = store.create_stage

    def create(operation, stage, body):
        etag = original(operation, stage, body)
        if stage.endswith("-domain-owned"):
            if failure == "invalid-etag": return "truthy-but-unknown"
            if failure == "lost-receipt": raise coordination.StoreUnknown()
        return etag

    store.create_stage = create
    if failure:
        with pytest.raises(domains.DomainError, match="EVIDENCE_UNKNOWN"):
            domains.bind_domain(owner, permit, config().inventory, WORKER, "worker", "fixture-api-token")
    else:
        domains.bind_domain(owner, permit, config().inventory, WORKER, "worker", "fixture-api-token")
    assert len(cloud.writes) == 1 and json.loads(store.body)["writer"] is not None
    with pytest.raises(domains.DomainError, match="UNOWNED_RESOURCE"):
        domains.bind_domain(owner, permit, config().inventory, WORKER, "worker", "fixture-api-token")
    cloud.workers.clear()
    cloud.dns.clear()
    with pytest.raises(coordination.CoordinationError, match="STAGE_ALREADY_ATTEMPTED"):
        domains.bind_domain(owner, permit, config().inventory, WORKER, "worker", "fixture-api-token")
    assert len(cloud.writes) == 1


def test_public_custom_artifacts_require_exact_inventory_worker_origin():
    value = config()
    public = PublicArtifacts(value.worker_origin + "/aggregator", inventory=value.inventory)
    public.close()
    for origin in (value.pages_origin + "/aggregator", "https://foreign.example.com/aggregator"):
        with pytest.raises(RuntimeError):
            PublicArtifacts(origin, inventory=value.inventory)
    with pytest.raises(RuntimeError):
        PublicArtifacts(value.worker_origin + "/aggregator")
