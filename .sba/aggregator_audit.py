"""执行批准根中的实际 EasyProxy 消费审计，并把结果绑定到准确候选摘要。"""
from __future__ import annotations

from dataclasses import dataclass
import hashlib
from pathlib import Path
import sys

from aggregator_build import _environment
from audit_runner import container_owner, run_audit
from audit_image import AuditImage, image_identity, verify_image
from aggregator_publish import Candidate, PublicArtifacts, RAW, _validate
from artifact_store import require
from cloudflare_http import decode_object
from coordination import Coordinator, Permit, _encode
from sources import inspect_checkout, inspect_gitlink_checkout


@dataclass(frozen=True)
class Audit:
    operation_id: str
    candidate_digest: str
    aggregator_sha: str
    body: bytes
    image: AuditImage


def candidate_digest(candidate: Candidate) -> str:
    _validate(candidate.artifacts, RAW)
    return hashlib.sha256(_encode({key: hashlib.sha256(value).hexdigest()
                                  for key, value in candidate.artifacts.items()})).hexdigest()


def audit_candidate(owner: Coordinator, permit: Permit, candidate: Candidate, public: PublicArtifacts,
                    root: Path, output: Path, *, image: AuditImage) -> Audit:
    state = owner.assert_owned(permit)
    require(candidate.operation_id == permit.operation_id, "AUDIT")
    rows = inspect_checkout(root, owner.identity["repository"], state["writer"]["candidate"]["sourceSha"])
    row = next(row for row in rows if row["path"] == "upstreams/aggregator")
    inspect_gitlink_checkout(root, row)
    digest = candidate_digest(candidate)
    require(output.is_absolute() and not str(output).startswith(("\\\\", "//")), "OUTPUT")
    output = output.resolve()
    require(not output.exists() and root not in output.parents and root != output, "OUTPUT")
    output.mkdir(mode=0o700, parents=True, exist_ok=False)
    env = _environment(output)
    require(type(image) is AuditImage and image.repository == owner.identity["repository"]
            and image.source_sha == state["writer"]["candidate"]["sourceSha"], "AUDIT_IMAGE")
    verify_image(image, env)
    summary = output / "audit.private.json"
    url = public.origin + f"/candidate/{permit.operation_id}/subs/clash.yaml"
    public.verify(f"candidate/{permit.operation_id}/subs/clash.yaml", candidate.artifacts["subs/clash.yaml"])
    owner.claim_stage(permit, "aggregator-audit")
    lifecycle = container_owner(root, output, "sba-" + permit.operation_id, image.image_id, image.source_sha)
    run_audit([sys.executable, str(root / "scripts/easyproxy_source_audit.py"), "--audit-id", "sba-" + permit.operation_id,
          "--subscription", url, "--output-path", str(summary), "--artifact-dir", str(output / "runtime"),
          "--docker-network-name", "", "--image", image.image_id, "--expected-image-id", image.image_id,
          "--expected-source-sha", image.source_sha, "--scenario-timeout-seconds", "720"],
         output, env, lifecycle=lifecycle)
    require(summary.is_file() and not summary.is_symlink() and summary.stat().st_size <= 16 * 1024 * 1024, "AUDIT")
    value = decode_object(summary.read_bytes(), require)
    require(value.get("container_cleanup_confirmed") is True, "AUDIT_CLEANUP")
    expected = image_identity(image)
    require(value.get("validated_image") == image.image_id and value.get("image_identity") == {
        key: expected[key] for key in ("imageId", "sourceSha", "platform", "scope")}, "AUDIT_IMAGE")
    verify_image(image, env)
    owner.assert_owned(permit)
    require(digest == candidate_digest(candidate), "AUDIT")
    inspect_checkout(root, owner.identity["repository"], state["writer"]["candidate"]["sourceSha"])
    return Audit(permit.operation_id, digest, row["sourceSha"], _encode(value), image)
