"""预升级：独立资源复制真实数据并演练迁移，源只读，周期任务/代理出口隔离。"""
from __future__ import annotations

import json
from pathlib import Path
import re
import time

from aggregator_audit import audit_candidate
from aggregator_publish import RAW, PublicArtifacts, _validate, promote_candidate, publish_candidate
from artifact_store import ArtifactStore
from audit_image import AuditImage, image_identity
from coordination import Coordinator, r2_store
from d1_init import D1API
from d1_snapshot import capture, import_snapshot, rehearse_migration
from deploy import stage_checks
from ech import EchDeployment, publish_ech
from github_environment import configure_environment, reviewed_workflow
from misub_sync import sync_misub
from pages import PagesDeployment, publish_pages
from refresh_context import object_json, require
from request import ApplicationRequest, SECRETS
from sealed_box import Sealer, secret_value
from sources import inspect_checkout
from suite_config import parse_configuration
from toolchain import LinuxTools
from verify_public import verify_public
from worker_api import credential


PREVIEW_KEY = "_sba/preview-v1.json"


def private_record(store, *, value: dict | None = None) -> dict:
    if value is not None:
        body = json.dumps(value, sort_keys=True, separators=(",", ":")).encode()
        require(len(body) <= 49152)
        store._put_once(PREVIEW_KEY, body, {"IfNoneMatch": "*"})
    result = store.client.get_object(Bucket=store.bucket, Key=PREVIEW_KEY)
    stream = result["Body"]
    try:
        raw = stream.read(49153)
    finally:
        stream.close()
    require(len(raw) <= 49152)
    actual = object_json(raw.decode("utf-8"))
    require(value is None or actual == value)
    return actual


def database_identity(api, config):
    identity = api.identity()
    require(identity.get("uuid") == config.database_id and identity.get("name") == config.database_name)


def isolate_settings(owner, permit, target):
    owner.claim_stage(permit, "preview-isolation")
    rows = target.query("SELECT key,value FROM settings WHERE key='worker_settings_v1'")
    require(len(rows) <= 1 and all(set(row) == {"key", "value"} for row in rows))
    settings = object_json(rows[0]["value"]) if rows else {}
    settings.update(BotToken="", ChatID="", aggregatorSync={**(settings.get("aggregatorSync") or {}),
        "enabled": False, "runOnCron": False}, webdavBackup={**(settings.get("webdavBackup") or {}), "enabled": False})
    literal = "'" + json.dumps(settings, ensure_ascii=True, separators=(",", ":")).replace("'", "''") + "'"
    owner.assert_owned(permit)
    if rows:
        target.query("UPDATE settings SET value=" + literal + " WHERE key='worker_settings_v1'")
    else:
        target.query("INSERT INTO settings(key,value) VALUES ('worker_settings_v1'," + literal + ")")
    require(target.query("SELECT key,value FROM settings WHERE key='worker_settings_v1'") ==
            [{"key": "worker_settings_v1", "value": json.dumps(settings, ensure_ascii=True, separators=(",", ":"))}])


def preview_suite(request: ApplicationRequest, root: Path, tools: LinuxTools, image: AuditImage, sealer: Sealer,
                  bodies: dict[str, bytes], output: Path, *, credentials: dict) -> dict:
    require(type(request) is ApplicationRequest and request.action == "preview" and type(tools) is LinuxTools
            and type(image) is AuditImage and type(sealer) is Sealer and request.context is not None)
    config, release = request.config, request.candidate
    source = request.context["source"]
    original = parse_configuration(source["configuration"], repository=config.repository, environment=source["environment"])
    require(root.is_absolute() and root.resolve(strict=True) == root and output.is_absolute()
            and output.resolve() == output and not output.exists() and root != output and root not in output.parents
            and set(credentials) == SECRETS and all(secret_value(value) for value in credentials.values())
            and image.source_sha == sealer.source_sha == release["sourceSha"] and image.repository == config.repository)
    require(all(credential(credentials[name]) for name in SECRETS - {"EASYPROXY_BACKUP_PASSPHRASE"})
            and re.fullmatch(r"[!#$%&'*+.^_`|~0-9A-Za-z-]+", credentials["EASYPROXY_ECH_TOKEN"]) is not None)
    image_identity(image)
    _validate(bodies, RAW)
    bodies = dict(bodies)
    inspect_checkout(root, config.repository, release["sourceSha"])
    reviewed_workflow(root)
    stores, apis, public = [], [], None
    try:
        def store_for(settings):
            store = r2_store(settings.account_id, settings.bucket, credentials["EASYPROXY_R2_ACCESS_KEY_ID"],
                             credentials["EASYPROXY_R2_SECRET_ACCESS_KEY"])
            stores.append(store)
            return store

        source_owner = Coordinator(store_for(original), original.identity("production"), original.inventory_digest)
        require(source_owner.current_release() == request.previous)
        source_api = D1API(original.account_id, credentials["EASYPROXY_CLOUDFLARE_API_TOKEN"], original.database_id)
        apis.append(source_api)
        database_identity(source_api, original)
        baseline = capture(source_api)
        require(source_owner.current_release() == request.previous)
        store = store_for(config)
        owner = Coordinator(store, config.identity("preview"), config.inventory_digest)
        target = D1API(config.account_id, credentials["EASYPROXY_CLOUDFLARE_API_TOKEN"], config.database_id)
        apis.append(target)
        database_identity(target, config)
        output.mkdir(mode=0o700, parents=True, exist_ok=False)
        permit = owner.begin("preview", release)
        created_at = int(time.time() * 1000)
        private_record(store, value={"schemaVersion": 1, "previewTaskId": request.task_id, "release": release,
            "source": source, "snapshot": {"id": baseline.digest, "createdAt": created_at}})
        import_snapshot(owner, permit, baseline, target)
        rehearse_migration(owner, permit, root, target)
        isolate_settings(owner, permit, target)
        require(capture(source_api).digest == baseline.digest and source_owner.current_release() == request.previous)
        github = configure_environment(owner, permit, config, root, token=credentials["EASYPROXY_GITHUB_TOKEN"],
                                       secrets={}, sealer=None)
        stage_checks(github, {"github-environment-configured", "github-environment-verified", "github-workflow-verified"})
        require(github.get("refreshEnabled") is False)
        edge = publish_ech(owner, permit, EchDeployment(root, tools.esbuild, output / "edge", config.worker_name, config.inventory),
            api_token=credentials["EASYPROXY_CLOUDFLARE_API_TOKEN"], ech_token=credentials["EASYPROXY_ECH_TOKEN"])
        stage_checks(edge, {"ech-published", "ech-bindings-verified", "ech-version-verified"})
        page = publish_pages(owner, permit, PagesDeployment(root, tools.node, tools.npm_cli, output / "pages", config.pages_name,
            config.inventory), api_token=credentials["EASYPROXY_CLOUDFLARE_API_TOKEN"], secrets={
                "ADMIN_PASSWORD": credentials["EASYPROXY_MISUB_ADMIN_PASSWORD"], "COOKIE_SECRET": credentials["EASYPROXY_MISUB_COOKIE_SECRET"],
                "MANIFEST_TOKEN": credentials["EASYPROXY_MANIFEST_TOKEN"]})
        stage_checks(page, {"pages-published", "pages-bindings-verified", "pages-version-verified"})
        public = PublicArtifacts(config.worker_origin + "/aggregator")
        artifacts = ArtifactStore(store)
        candidate = publish_candidate(owner, permit, artifacts, public, bodies)
        audit = audit_candidate(owner, permit, candidate, public, root, output / "audit", image=image)
        promoted = promote_candidate(owner, permit, artifacts, public, candidate, audit=audit, root=root)
        stage_checks(promoted, {"candidate-verified", "stable-verified"})
        synchronized = sync_misub(owner, permit, candidate, audit, public, root, config.inventory,
            admin_password=credentials["EASYPROXY_MISUB_ADMIN_PASSWORD"], manifest_token=credentials["EASYPROXY_MANIFEST_TOKEN"],
            ech_token=credentials["EASYPROXY_ECH_TOKEN"])
        stage_checks(synchronized, {"misub-refreshed"})
        verified = verify_public(config, release, root)
        stage_checks(verified, {"release-versions-verified", "published-subscriptions-verified", "pages-frontend-verified"})
        require(verified.get("runId") == promoted.get("runId") == permit.operation_id
                and verified.get("manifestSha256") == promoted.get("manifestSha256") and verified.get("counts") == promoted.get("counts"))
        from preview_isolation import verify_isolation
        verify_isolation(config)
        require(capture(source_api).digest == baseline.digest and source_owner.current_release() == request.previous)
        database_identity(source_api, original)
        database_identity(target, config)
        inspect_checkout(root, config.repository, release["sourceSha"])
        checks = dict.fromkeys(("suite-published", "snapshot-copied", "migration-verified", "source-unchanged",
                                "side-effects-isolated", "service-ready"), True)
        owner.commit_verified(permit, checks)
        return {"checks": checks, "lifecycle": {"resources": [{"key": row["key"], "status": "created"}
            for row in config.platform_inventory], "urls": [config.worker_origin, config.pages_origin],
            "snapshot": {"id": baseline.digest, "createdAt": created_at}}}
    finally:
        if public is not None:
            public.close()
        for api in apis:
            api.close()
        for store in stores:
            store.client.close()
