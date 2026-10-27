"""第十入口的只读资源证明；已建 Worker 域名绝不再次 attach 或修改 DNS。"""
import hashlib

from coordination import ETAG, _encode, inventory_digest
from domains import DomainAPI, DomainTarget
from fresh_domain_resume import require, WORKER_BUNDLE_SHA256
from github_api import GitHubAPI
from github_environment import variables, verify_policy, reviewed_workflow, REFRESH_SECRETS
from pages_repair import worker_bundle_digest
from refresh_registry import REGISTRY_VARIABLE, parse_registry
from repair_context import FRESH_DOMAIN_PARENT_RELEASE as PARENT
from worker_api import WorkerAPI, public_release

ENVIRONMENT_ID = 25444391810
DOMAIN_IDENTITY_SHA256 = "12ce0d343bc550957f9946f2cb8d5de7d589008da51f972b709a314913fc67e6"


def verify_configured(owner, config, root, token):
    api = GitHubAPI(config.repository, config.environment, token)
    try:
        repository = api.repository_info()
        require(repository["defaultBranch"] == "main"
                and api.workflow_at("main") == api.workflow_at(PARENT["sourceSha"]) == reviewed_workflow(root))
        api.active_workflow()
        environment = api.environment_info()
        require(environment is not None and environment["id"] == ENVIRONMENT_ID)
        verify_policy(environment, api.policies(), "main")
        require(api.variables() == variables(owner, config, repository["id"], ENVIRONMENT_ID)
                and api.secret_names() == REFRESH_SECRETS
                and parse_registry(api.repository_variable(REGISTRY_VARIABLE), config.repository)["environments"] == [config.environment])
        return {"checks": dict.fromkeys(("github-environment-configured", "github-environment-verified",
                "github-workflow-verified"), True), "refreshEnabled": True}
    finally:
        api.close()


def verify_existing_worker(owner, config, token, ech_token):
    from ech import metadata, _verify_settings
    api = WorkerAPI(config.account_id, token)
    try:
        require(api.account_subdomain() == config.workers_subdomain)
        _verify_settings(api.settings(config.worker_name), metadata(owner, PARENT, ech_token))
        api.script_subdomain(config.worker_name)
        require(worker_bundle_digest(api, config.worker_name) == WORKER_BUNDLE_SHA256)
        expected = {"schemaVersion": 1, "applicationId": "easyproxy-cloud", **PARENT}
        provider = f"https://{config.worker_name}.{config.workers_subdomain}.workers.dev"
        for origin in (provider, config.worker_origin):
            observed = public_release(origin)
            require(type(observed) is dict and type(observed.get("schemaVersion")) is int and observed == expected)
    finally:
        api.close()


def verify_worker_domain(owner, inventory, token):
    require(inventory_digest(inventory) == owner.inventory_digest)
    workers = [row for row in inventory if row["kind"] == "worker"]
    require(len(workers) == 1)
    service = workers[0]["name"]
    rows = [row for row in inventory if row["kind"] == "domain" and row["name"] == service]
    require(len(rows) == 1)
    target = DomainTarget("worker", service, rows[0]["id"])
    api = DomainAPI(owner.identity["accountId"], token)
    try:
        zone = api.inspect(target, existing=True)
        domains, dns = api.worker_domains(target), api.dns(target, zone)
        require(len(domains) == 1)
        identity = {"zoneId": zone["id"], "domainId": api._worker(domains[0], target, zone), "dnsId": api._dns(dns, target)}
        require(hashlib.sha256(_encode(identity)).hexdigest() == DOMAIN_IDENTITY_SHA256)
        return {"schemaVersion": 1, "accountId": api.account, "inventoryDigest": owner.inventory_digest,
                "kind": "worker", "service": service, "hostname": target.hostname, **identity}
    finally:
        api.close()


def record_worker_domain(owner, permit, inventory, token):
    # A fresh operation owns this new proof. No old operation evidence is repaired in place.
    owner.assert_owned(permit)
    evidence = verify_worker_domain(owner, inventory, token)
    owner.claim_stage(permit, "worker-domain-verification")
    etag = owner.store.create_stage(permit.operation_id, "worker-domain-owned", _encode(evidence))
    require(type(etag) is str and ETAG.fullmatch(etag) is not None)
    owner.assert_owned(permit)
