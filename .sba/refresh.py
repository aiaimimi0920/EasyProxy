"""同一进程完成批准实例的候选、审计、晋级和同步；不升级软件版本。"""
from __future__ import annotations

from pathlib import Path

from aggregator_audit import audit_candidate
from aggregator_publish import RAW, PublicArtifacts, _validate, promote_candidate, publish_candidate
from artifact_store import ArtifactStore
from audit_image import AuditImage, image_identity
from coordination import Coordinator, r2_store
from deploy import stage_checks
from misub_sync import sync_misub
from refresh_context import REFRESH_SECRETS, require
from sealed_box import secret_value
from sources import inspect_checkout
from suite_config import SuiteConfig
from verify_public import verify_public
from worker_api import credential


def refresh_suite(config: SuiteConfig, release: dict, root: Path, image: AuditImage,
                  bodies: dict[str, bytes], output: Path, *, credentials: dict) -> dict:
    require(type(config) is SuiteConfig and type(image) is AuditImage
            and image.source_sha == release["sourceSha"] and image.repository == config.repository)
    require(root.is_absolute() and root.resolve(strict=True) == root and output.is_absolute()
            and output.resolve() == output and not output.exists() and root != output and root not in output.parents)
    require(type(credentials) is dict and set(credentials) == REFRESH_SECRETS
            and all(secret_value(value) and credential(value) for value in credentials.values()))
    image_identity(image)
    _validate(bodies, RAW)
    bodies = dict(bodies)
    inspect_checkout(root, config.repository, release["sourceSha"])
    store = r2_store(config.account_id, config.bucket, credentials["EASYPROXY_R2_ACCESS_KEY_ID"],
                     credentials["EASYPROXY_R2_SECRET_ACCESS_KEY"])
    public = None
    try:
        owner = Coordinator(store, config.identity("production"), config.inventory_digest)
        require(owner.current_release() == release)
        output.mkdir(mode=0o700, parents=True, exist_ok=False)
        public = PublicArtifacts(config.worker_origin + "/aggregator",
            **({"inventory": config.inventory} if config.custom_worker_origin is not None else {}))
        # Recheck and acquire atomically after all local preparation. An update
        # between selector and here must reject, never refresh the new SHA using old code.
        permit = owner.begin("refresh", release, previous=release)
        artifacts = ArtifactStore(store)
        candidate = publish_candidate(owner, permit, artifacts, public, bodies)
        audit = audit_candidate(owner, permit, candidate, public, root, output / "audit", image=image)
        promoted = promote_candidate(owner, permit, artifacts, public, candidate, audit=audit, root=root)
        stage_checks(promoted, {"candidate-verified", "stable-verified"})
        synchronized = sync_misub(owner, permit, candidate, audit, public, root, config.inventory,
            admin_password=credentials["EASYPROXY_MISUB_ADMIN_PASSWORD"],
            manifest_token=credentials["EASYPROXY_MANIFEST_TOKEN"], ech_token=credentials["EASYPROXY_ECH_TOKEN"])
        stage_checks(synchronized, {"misub-refreshed"})
        verified = verify_public(config, release, root)
        stage_checks(verified, {"release-versions-verified", "published-subscriptions-verified", "pages-frontend-verified"})
        require(verified.get("runId") == promoted.get("runId") == permit.operation_id
                and verified.get("manifestSha256") == promoted.get("manifestSha256")
                and verified.get("counts") == promoted.get("counts"))
        inspect_checkout(root, config.repository, release["sourceSha"])
        checks = {"candidate-verified": True, "stable-verified": True, "misub-refreshed": True}
        owner.commit_verified(permit, checks)
        return {"checks": checks}
    finally:
        if public is not None:
            public.close()
        store.client.close()
