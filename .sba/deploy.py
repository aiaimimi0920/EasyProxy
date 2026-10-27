"""单进程部署/升级编排；阶段证据全部成立后才提交同一套件版本。"""
from __future__ import annotations

from pathlib import Path

from aggregator_audit import audit_candidate
from aggregator_publish import RAW, PublicArtifacts, _validate, promote_candidate, publish_candidate
from artifact_store import ArtifactStore
from audit_image import AuditImage, image_identity
from assets_repair import begin_recovery as begin_assets_recovery
from coordination import Coordinator, r2_store
from d1_init import initialize_d1
from diagnostics import phase
from domains import preflight_domains
from fresh_domain_repair import begin_recovery as begin_fresh_domain_recovery
from fresh_domain_resources import verify_configured as verify_fresh_configured
from fresh_domain_resume import FreshDomainResume
from ech import EchDeployment, metadata as worker_metadata, publish_ech
from github_environment import REFRESH_SECRETS, configure_environment, reviewed_workflow
from github_repair import begin_recovery, verify_initialized
from generation_repair import begin_recovery as begin_generation_recovery
from generation_outputs_repair import begin_recovery as begin_generation_outputs_recovery
from pages_asset_protocol_repair import begin_recovery as begin_pages_asset_protocol_recovery
from misub_sync import sync_misub
from migration import MiSubMigration, migrate_misub
from pages import PagesDeployment, configuration as pages_configuration, publish_pages
from pages_repair import PagesResume, begin_recovery as begin_pages_recovery, verify_configured
from pages_diagnostic_repair import begin_recovery as begin_pages_diagnostic_recovery
from repair_context import PAGES_REPAIR_ID, ASSETS_REPAIR_ID, GENERATION_REPAIR_ID, PAGES_DIAGNOSTIC_REPAIR_ID
from repair_context import GENERATION_OUTPUTS_REPAIR_ID, PAGES_ASSET_PROTOCOL_REPAIR_ID
from repair_context import FRESH_DOMAIN_REPAIR_ID
from request import ApplicationRequest, SECRETS
from sealed_box import Sealer, secret_value
from sources import inspect_checkout
from toolchain import LinuxTools
from verify_public import verify_public
from worker_api import credential


class DeploymentError(RuntimeError):
    pass


def require(value):
    if not value:
        raise DeploymentError("EASYPROXY_SBA_DEPLOY_REJECTED")


def stage_checks(result: dict, expected: set[str]):
    require(type(result) is dict and type(result.get("checks")) is dict
            and set(result["checks"]) == expected and all(value is True for value in result["checks"].values()))


def deploy_suite(request: ApplicationRequest, root: Path, tools: LinuxTools, image: AuditImage, sealer: Sealer,
                 bodies: dict[str, bytes], output: Path, *, credentials: dict,
                 migration_settings: MiSubMigration | None = None,
                 recovery_request: ApplicationRequest | None = None) -> dict:
    """工具、批准根、镜像和本地候选由调用方先准备；本函数不跨进程交接 Permit。"""
    require(type(request) is ApplicationRequest and request.action in ("deploy", "update")
            and (request.action == "update") == (type(migration_settings) is MiSubMigration)
            and (request.previous is None) == (request.action == "deploy")
            and type(tools) is LinuxTools and type(image) is AuditImage and type(sealer) is Sealer)
    require(recovery_request is None or type(recovery_request) is ApplicationRequest
            and recovery_request.action == "repair" and request.action == "deploy"
            and recovery_request.task_id == request.task_id
            and recovery_request.candidate == request.candidate and recovery_request.config == request.config)
    require(root.is_absolute() and root.resolve(strict=True) == root
            and output.is_absolute() and output.resolve() == output and not output.exists()
            and root != output and root not in output.parents)
    config, release = request.config, request.candidate
    require(type(credentials) is dict and set(credentials) == SECRETS
            and all(secret_value(value) for value in credentials.values()))
    # These provider/Pages channels intentionally accept only visible ASCII.
    require(all(credential(credentials[name]) for name in SECRETS - {"EASYPROXY_BACKUP_PASSPHRASE"}))
    require(image.source_sha == sealer.source_sha == release["sourceSha"] and image.repository == config.repository)
    image_identity(image)
    _validate(bodies, RAW)
    bodies = dict(bodies)
    inspect_checkout(root, config.repository, release["sourceSha"])
    reviewed_workflow(root)  # Missing schedule is a preflight rejection, not a successful suite.
    fresh_domain = recovery_request is not None and recovery_request.context.get("repairId") == FRESH_DOMAIN_REPAIR_ID
    with phase("DOMAINS"):
        if not fresh_domain:
            preflight_domains(config, credentials["EASYPROXY_CLOUDFLARE_API_TOKEN"], existing=request.action == "update")
    if migration_settings is not None:
        require(migration_settings.root == root and migration_settings.database_id == config.database_id
                and migration_settings.base_url == f"https://{config.pages_name}.pages.dev")
    with phase("WRITER"):
        store = r2_store(config.account_id, config.bucket, credentials["EASYPROXY_R2_ACCESS_KEY_ID"],
                         credentials["EASYPROXY_R2_SECRET_ACCESS_KEY"])
    public = None
    try:
        with phase("WRITER"):
            owner = Coordinator(store, config.identity("production"), config.inventory_digest)
            page_secrets = {"ADMIN_PASSWORD": credentials["EASYPROXY_MISUB_ADMIN_PASSWORD"],
                            "COOKIE_SECRET": credentials["EASYPROXY_MISUB_COOKIE_SECRET"],
                            "MANIFEST_TOKEN": credentials["EASYPROXY_MANIFEST_TOKEN"]}
            # Validate formats before even acquiring the durable writer.
            pages_configuration(owner, release, config.pages_name, config.database_id, page_secrets,
                                **({"origin": config.pages_origin} if config.custom_pages_origin is not None else {}))
            worker_metadata(owner, release, credentials["EASYPROXY_ECH_TOKEN"])
            output.mkdir(mode=0o700, parents=True, exist_ok=False)
            public = PublicArtifacts(config.worker_origin + "/aggregator",
                **({"inventory": config.inventory} if config.custom_worker_origin is not None else {}))
            resume = None
            if recovery_request is None:
                permit = owner.begin(request.action, release, previous=request.previous)
            else:
                begin = {PAGES_REPAIR_ID: begin_pages_recovery, ASSETS_REPAIR_ID: begin_assets_recovery,
                         FRESH_DOMAIN_REPAIR_ID: begin_fresh_domain_recovery,
                         GENERATION_REPAIR_ID: begin_generation_recovery,
                         GENERATION_OUTPUTS_REPAIR_ID: begin_generation_outputs_recovery,
                         PAGES_ASSET_PROTOCOL_REPAIR_ID: begin_pages_asset_protocol_recovery,
                         PAGES_DIAGNOSTIC_REPAIR_ID: begin_pages_diagnostic_recovery}.get(
                    recovery_request.context.get("repairId"), begin_recovery)
                permit, resume = begin(owner, recovery_request, root, credentials)
        # No stage, exception path, or finally below may release this writer.
        if request.action == "deploy":
            with phase("D1"):
                if resume is None:
                    initialized = initialize_d1(owner, permit, root, config.inventory,
                                                api_token=credentials["EASYPROXY_CLOUDFLARE_API_TOKEN"])
                else:
                    initialized = verify_initialized(config, root, credentials["EASYPROXY_CLOUDFLARE_API_TOKEN"])
            stage_checks(initialized, {"d1-initialized", "schema-verified"})
        else:
            with phase("MIGRATION"):
                migration = migrate_misub(owner, permit, migration_settings, credentials={
                    "CLOUDFLARE_API_TOKEN": credentials["EASYPROXY_CLOUDFLARE_API_TOKEN"],
                    "MISUB_ADMIN_PASSWORD": credentials["EASYPROXY_MISUB_ADMIN_PASSWORD"],
                    "EASYPROXY_BACKUP_PASSPHRASE": credentials["EASYPROXY_BACKUP_PASSPHRASE"]})
            stage_checks(migration, {"backup-created", "data-preserved", "migration-verified"})
        with phase("GITHUB"):
            if type(resume) is FreshDomainResume:
                resume.assert_permit(owner, permit)
                github = verify_fresh_configured(owner, config, root, credentials["EASYPROXY_GITHUB_TOKEN"])
            elif type(resume) is PagesResume:
                resume.assert_permit(owner, permit)
                github = verify_configured(owner, config, root, credentials["EASYPROXY_GITHUB_TOKEN"],
                                           parent_release=resume.parent_release)
            else:
                github = configure_environment(owner, permit, config, root, token=credentials["EASYPROXY_GITHUB_TOKEN"],
                    secrets={name: credentials[name] for name in REFRESH_SECRETS}, sealer=sealer, resume=resume)
        stage_checks(github, {"github-environment-configured", "github-environment-verified", "github-workflow-verified"})
        require(github.get("refreshEnabled") is True)
        continuation = {"resume": resume} if type(resume) in (PagesResume, FreshDomainResume) else {}
        with phase("EDGE"):
            edge = publish_ech(owner, permit, EchDeployment(root, tools.esbuild, output / "edge", config.worker_name,
                config.inventory), api_token=credentials["EASYPROXY_CLOUDFLARE_API_TOKEN"], ech_token=credentials["EASYPROXY_ECH_TOKEN"], **continuation)
        stage_checks(edge, {"ech-published", "ech-bindings-verified", "ech-version-verified"})
        require(edge.get("origin") == config.worker_origin and edge.get("sourceSha") == release["sourceSha"]
                and edge.get("applicationVersion") == release["applicationVersion"])
        with phase("PAGES"):
            pages = publish_pages(owner, permit, PagesDeployment(root, tools.node, tools.npm_cli, output / "pages",
                config.pages_name, config.inventory), api_token=credentials["EASYPROXY_CLOUDFLARE_API_TOKEN"], secrets=page_secrets,
                **({} if type(resume) is FreshDomainResume else continuation))
        stage_checks(pages, {"pages-published", "pages-bindings-verified", "pages-version-verified"})
        require(pages.get("origin") == config.pages_origin and pages.get("sourceSha") == release["sourceSha"]
                and pages.get("applicationVersion") == release["applicationVersion"])
        artifacts = ArtifactStore(store)
        with phase("CANDIDATE"):
            candidate = publish_candidate(owner, permit, artifacts, public, bodies)
        with phase("AUDIT"):
            audit = audit_candidate(owner, permit, candidate, public, root, output / "audit", image=image)
        with phase("PROMOTION"):
            promotion = promote_candidate(owner, permit, artifacts, public, candidate, audit=audit, root=root)
        stage_checks(promotion, {"candidate-verified", "stable-verified"})
        with phase("SYNC"):
            synchronized = sync_misub(owner, permit, candidate, audit, public, root, config.inventory,
                admin_password=credentials["EASYPROXY_MISUB_ADMIN_PASSWORD"],
                manifest_token=credentials["EASYPROXY_MANIFEST_TOKEN"], ech_token=credentials["EASYPROXY_ECH_TOKEN"])
        stage_checks(synchronized, {"misub-refreshed"})
        with phase("VERIFY"):
            verified = verify_public(config, release, root)
        stage_checks(verified, {"release-versions-verified", "published-subscriptions-verified", "pages-frontend-verified"})
        require(verified.get("runId") == promotion.get("runId") == permit.operation_id
                and verified.get("manifestSha256") == promotion.get("manifestSha256")
                and verified.get("counts") == promotion.get("counts"))
        inspect_checkout(root, config.repository, release["sourceSha"])
        checks = {"suite-published": True, "service-ready": True}
        if request.action == "update":
            checks.update({"backup-created": True, "data-preserved": True})
        with phase("COMMIT"):
            owner.commit_verified(permit, checks)
        return {"checks": checks}
    finally:
        if public is not None:
            public.close()
        store.client.close()
