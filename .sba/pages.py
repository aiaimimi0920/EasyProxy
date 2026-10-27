"""共同许可下的 MiSub Pages 发布阶段；不提交套件版本、不解锁。"""
from __future__ import annotations

from dataclasses import dataclass
import hashlib
from pathlib import Path
import time

from coordination import Coordinator, Permit, inventory_digest
from diagnostics import phase
from domains import bind_domain
from origins import approved_origin, inventory_origin
from pages_api import PagesAPI, PagesError, credential, identifier, name, public_release, require
from pages_build import COMPATIBILITY_DATE, build_pages


@dataclass(frozen=True)
class PagesDeployment:
    root: Path
    node: Path
    npm_cli: Path
    workdir: Path
    project_name: str
    inventory: list[dict]


def configuration(owner: Coordinator, release: dict, project: str, database: str, secrets: dict, *, origin: str | None = None) -> dict:
    require(type(secrets) is dict and set(secrets) == {"ADMIN_PASSWORD", "COOKIE_SECRET", "MANIFEST_TOKEN"}
            and all(credential(value) for value in secrets.values()), "BUSINESS_CREDENTIALS")
    provider = f"https://{name(project)}.pages.dev"
    origin = provider if origin is None else origin
    require(approved_origin(origin, provider), "IDENTITY")
    plain = {"MISUB_PUBLIC_URL": origin, "MISUB_CALLBACK_URL": origin,
             "EASYPROXY_DEPLOYMENT_NAME": owner.identity["environment"], "CF_PAGES_PROJECT_NAME": project,
             "EASYPROXY_MISUB_D1_DATABASE_ID": identifier(database), "SBA_INVENTORY_DIGEST": owner.inventory_digest,
             "SBA_RELEASE_SHA": release["sourceSha"], "SBA_RELEASE_VERSION": release["applicationVersion"],
             "SBA_PURPOSE": owner.identity["purpose"]}
    env = {key: {"type": "plain_text", "value": value} for key, value in plain.items()}
    env.update({key: {"type": "secret_text", "value": value} for key, value in secrets.items()})
    # An isolated SBA preview is a separate project using its own production
    # environment. Its incidental Pages preview environment has no data/secrets.
    blank = {"compatibility_date": COMPATIBILITY_DATE, "compatibility_flags": [], "env_vars": {}, "d1_databases": {},
             "always_use_latest_compatibility_date": False, "fail_open": False}
    return {"production": {**blank, "env_vars": env, "d1_databases": {"MISUB_DB": {"id": database}}}, "preview": blank}


def _verify_environment(value: dict, expected: dict):
    require(type(value) is dict and value.get("compatibility_date") == COMPATIBILITY_DATE
            and value.get("compatibility_flags", []) == [] and value.get("always_use_latest_compatibility_date") is False
            and value.get("fail_open") is False, "CONFIGURATION")
    require(value.get("d1_databases", {}) == expected["d1_databases"], "BINDINGS")
    for field in ("kv_namespaces", "r2_buckets", "services", "durable_object_namespaces", "queue_producers", "hyperdrive_bindings",
                  "analytics_engine_datasets", "vectorize_bindings", "ai_bindings", "mtls_certificates", "browsers"):
        require(value.get(field) in (None, {}, []), "BINDINGS")
    variables = value.get("env_vars", {})
    # Pages represents an empty environment map as null. Only an explicitly
    # empty expected map may accept that shape; required production bindings
    # and falsey values of any other type must still fail closed.
    if variables is None and expected["env_vars"] == {}:
        variables = {}
    require(type(variables) is dict and set(variables) == set(expected["env_vars"]), "BINDINGS")
    for key, item in expected["env_vars"].items():
        observed = variables[key]
        require(type(observed) is dict and observed.get("type") == item["type"], "BINDINGS")
        if item["type"] == "plain_text":
            require(observed.get("value") == item["value"], "BINDINGS")
        # Write-only secrets: only their presence/type is attested here. This is
        # not an authentication test or a claim that plaintext was read back.


def _verify_project(value: dict, project: str, expected: dict, project_id: str | None = None) -> str:
    require(type(value) is dict and value.get("name") == project and value.get("subdomain") == project + ".pages.dev"
            and value.get("production_branch") == "main" and value.get("source") is None, "OWNERSHIP")
    identity = identifier(value.get("id"))
    require(project_id is None or identity == project_id, "OWNERSHIP")
    configs = value.get("deployment_configs")
    require(type(configs) is dict and set(configs) == {"production", "preview"}, "CONFIGURATION")
    for environment in configs:
        _verify_environment(configs[environment], expected[environment])
    return identity


def _verify_deployment(value: dict, project: str, project_id: str, release: dict, deployment_id: str | None = None,
                       *, ready: bool = True):
    require(type(value) is dict and value.get("project_name") == project and value.get("project_id") == project_id
            and value.get("environment") == "production" and (not ready or value.get("uses_functions") is True), "DEPLOYMENT")
    identity = identifier(value.get("id"))
    require(deployment_id is None or identity == deployment_id, "DEPLOYMENT")
    trigger = value.get("deployment_trigger")
    require(type(trigger) is dict and type(trigger.get("metadata")) is dict
            and trigger["metadata"].get("branch") == "main"
            and trigger["metadata"].get("commit_hash") == release["sourceSha"], "DEPLOYMENT")
    return identity


def _propagation_delay():
    time.sleep(5)


def _wait_deployment(api: PagesAPI, initial: dict, project_id: str, release: dict) -> str:
    deployment_id = _verify_deployment(initial, api.project_name, project_id, release, ready=False)
    for attempt in range(36):
        value = api.deployment(deployment_id)
        _verify_deployment(value, api.project_name, project_id, release, deployment_id, ready=False)
        stage = value.get("latest_stage")
        require(type(stage) is dict and stage.get("name") in ("queued", "initialize", "clone_repo", "build", "deploy")
                and stage.get("status") in ("idle", "active", "success"), "DEPLOYMENT_UNKNOWN")
        if stage["name"] == "deploy" and stage["status"] == "success":
            _verify_deployment(value, api.project_name, project_id, release, deployment_id)
            return deployment_id
        if attempt < 35:
            _propagation_delay()
    raise PagesError("EASYPROXY_SBA_PAGES_DEPLOYMENT_UNKNOWN")


def _verify_public(project: str, release: dict, *, origin: str | None = None):
    for attempt in range(12):
        try:
            value = public_release(project, origin=origin) if origin is not None else public_release(project)
            if type(value.get("schemaVersion")) is int and value == {"schemaVersion": 1, "applicationId": "easyproxy-cloud", **release}:
                return
        except PagesError:
            pass
        if attempt < 11:
            _propagation_delay()
    raise PagesError("EASYPROXY_SBA_PAGES_PUBLIC_VERSION_UNKNOWN")


def publish_pages(owner: Coordinator, permit: Permit, settings: PagesDeployment, *, api_token: str, secrets: dict, resume=None) -> dict:
    from pages_repair import PagesResume, PROJECT_ID
    state = owner.assert_owned(permit)
    require(permit.action in ("deploy", "update", "preview") and inventory_digest(settings.inventory) == owner.inventory_digest,
            "INVENTORY")
    projects = [row for row in settings.inventory if row["kind"] == "pages"]
    databases = [row for row in settings.inventory if row["kind"] == "d1"]
    buckets = [row for row in settings.inventory if row["kind"] == "r2"]
    require(len(projects) == len(databases) == len(buckets) == 1
            and projects[0]["id"] == projects[0]["name"] == settings.project_name
            and buckets[0]["id"] == buckets[0]["name"] == owner.identity["bucket"],
            "IDENTITY")
    release = state["writer"]["candidate"]
    if resume is not None:
        require(type(resume) is PagesResume, "OWNERSHIP")
        resume.assert_permit(owner, permit)
    provider_origin = f"https://{settings.project_name}.pages.dev"
    origin = inventory_origin(settings.inventory, settings.project_name, provider_origin)
    origin_option = {"origin": origin} if origin != provider_origin else {}
    require(not origin_option or resume is None, "OWNERSHIP")
    expected = configuration(owner, release, settings.project_name, databases[0]["id"], secrets, **origin_option)
    api = PagesAPI(owner.identity["accountId"], settings.project_name, api_token)
    try:
        with phase("PAGES_BUILD"):
            artifact = build_pages(settings.root, owner.identity["repository"], release["sourceSha"],
                                   settings.node, settings.npm_cli, settings.workdir)
        existing = api.project()
        project_id = None
        if resume is not None:
            with phase("PAGES_PARENT_VERIFY"):
                previous = configuration(owner, resume.parent_release, settings.project_name, databases[0]["id"], secrets)
                project_id = _verify_project(existing, settings.project_name, previous, PROJECT_ID)
                require(existing.get("latest_deployment") is None and existing.get("canonical_deployment") is None
                        and api.deployments() == [], "DEPLOYMENT")
        elif permit.action == "update":
            with phase("PAGES_PARENT_VERIFY"):
                previous = configuration(owner, state["active"], settings.project_name, databases[0]["id"], secrets, **origin_option)
                project_id = _verify_project(existing, settings.project_name, previous)
                canonical = existing.get("canonical_deployment")
                _verify_deployment(canonical, settings.project_name, project_id, state["active"])
                require(type(canonical.get("latest_stage")) is dict and canonical["latest_stage"].get("name") == "deploy"
                        and canonical["latest_stage"].get("status") == "success", "DEPLOYMENT")
        else:
            require(existing is None, "UNOWNED_RESOURCE")
        with phase("PAGES_CLAIM"):
            owner.claim_stage(permit, "pages-publication")
        if existing is None:
            created = api.create(expected)
            project_id = _verify_project(created, settings.project_name, expected)
        else:
            api.configure(expected)
        with phase("PAGES_CONFIG_VERIFY"):
            owner.assert_owned(permit)
            _verify_project(api.project(), settings.project_name, expected, project_id)
        api.upload_assets(artifact.assets, lambda: owner.assert_owned(permit))
        with phase("PAGES_PREDEPLOY_VERIFY"):
            owner.assert_owned(permit)
            # Recheck immediately before the only deployment POST. The provider has
            # no project-config CAS here; do not claim atomic replacement against admins.
            _verify_project(api.project(), settings.project_name, expected, project_id)
        deployed = api.deploy(artifact, release)
        with phase("PAGES_DEPLOYMENT_WAIT"):
            deployment_id = _wait_deployment(api, deployed, project_id, release)
        with phase("PAGES_DEPLOYMENT_VERIFY"):
            project = api.project()
            _verify_project(project, settings.project_name, expected, project_id)
            _verify_deployment(project.get("canonical_deployment"), settings.project_name, project_id, release, deployment_id)
        with phase("PAGES_PUBLIC_VERIFY"):
            _verify_public(settings.project_name, release)
            if origin_option:
                with phase("DOMAINS"):
                    bind_domain(owner, permit, settings.inventory, settings.project_name, "pages", api_token)
                _verify_public(settings.project_name, release, **origin_option)
        with phase("PAGES_FINAL_OWNER"):
            owner.assert_owned(permit)
        return {"checks": {"pages-published": True, "pages-bindings-verified": True, "pages-version-verified": True},
                "project": settings.project_name, "deploymentId": deployment_id,
                "origin": origin, "sourceSha": release["sourceSha"],
                "applicationVersion": release["applicationVersion"], "assetCount": len(artifact.assets),
                "workerBundleSha256": hashlib.sha256(artifact.worker_bundle).hexdigest()}
    finally:
        api.close()  # UNKNOWN preserves the writer and all partial state. No retry/rollback/delete.
