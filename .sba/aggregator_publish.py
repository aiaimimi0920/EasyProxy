"""Aggregator 候选与稳定发布阶段；所有写入单次发送，失败保留 writer，不自动恢复。"""
from __future__ import annotations

import base64
from dataclasses import dataclass
from datetime import datetime, timezone
import hashlib
import json
from pathlib import Path
import re
from urllib.parse import urlsplit

import requests
import yaml

from artifact_store import ArtifactError, ArtifactStore, MAX_BYTES, Object, require
from cloudflare_http import decode_object, secure_session
from coordination import Coordinator, Permit, SHA, _encode
from sources import inspect_checkout


ARTIFACTS = {
    "public-clash": "subs/clash.yaml", "public-singbox": "subs/singbox.json",
    "public-mixed": "subs/mixed.txt", "public-v2ray": "subs/v2ray.txt",
    "public-effective": "subs/effective.txt", "public-effective-json": "subs/effective.json",
    "crawledsubs": "internal/crawledsubs.json",
}
RAW = {key for name, key in ARTIFACTS.items() if name not in ("public-effective", "public-effective-json")}
RUN = re.compile(r"[a-f0-9]{32}\Z")


class ClashSafeLoader(yaml.SafeLoader):
    """仅兼容批准 subconverter 输出的 !<str> 标量；不污染全局 SafeLoader。"""


ClashSafeLoader.add_constructor("str", yaml.SafeLoader.construct_yaml_str)


@dataclass(frozen=True)
class Candidate:
    operation_id: str
    artifacts: dict[str, bytes]


class PublicArtifacts:
    def __init__(self, origin: str):
        require(isinstance(origin, str), "ORIGIN")
        url = urlsplit(origin)
        require(url.scheme == "https" and re.fullmatch(r"[a-z0-9-]+\.[a-z0-9-]+\.workers\.dev", url.netloc) is not None
                and url.path == "/aggregator" and not url.query and not url.fragment, "ORIGIN")
        self.origin = origin
        self.session = secure_session()

    def verify(self, key: str, body: bytes) -> None:
        ArtifactStore._key(key)
        try:
            with self.session.get(self.origin + "/" + key, timeout=(10, 60), allow_redirects=False,
                                  stream=True, headers={"Cache-Control": "no-cache"}) as response:
                require(response.status_code == 200, "PUBLIC_UNKNOWN")
                actual = bytearray()
                for chunk in response.iter_content(65536):
                    actual.extend(chunk)
                    require(len(actual) <= MAX_BYTES, "PUBLIC_UNKNOWN")
                require(bytes(actual) == body, "PUBLIC_MISMATCH")
        except (requests.RequestException, OSError):
            raise ArtifactError("EASYPROXY_SBA_ARTIFACT_PUBLIC_UNKNOWN") from None

    def close(self) -> None:
        self.session.close()


def _validate(bodies: dict[str, bytes], expected: set[str]) -> None:
    require(type(bodies) is dict and set(bodies) == expected
            and all(type(body) is bytes and 0 < len(body) <= MAX_BYTES and body.strip() for body in bodies.values()), "INPUT")
    try:
        for key, body in bodies.items():
            body.decode("utf-8", errors="strict")
            if key.endswith(".json"):
                value = json.loads(body)
                require(value not in ({}, [], "", None), "EMPTY")
                if key == "subs/singbox.json":
                    require(type(value) is dict and type(value.get("outbounds")) is list and value["outbounds"], "FORMAT")
            if key == "subs/clash.yaml":
                value = yaml.load(body, Loader=ClashSafeLoader)
                require(type(value) is dict and type(value.get("proxies")) is list and value["proxies"], "FORMAT")
            if key in ("subs/mixed.txt", "subs/v2ray.txt"):
                decoded = base64.b64decode(b"".join(body.split()), validate=True).decode("utf-8")
                require(decoded.strip() and all("://" in line and not any(char.isspace() for char in line)
                                               for line in decoded.splitlines() if line), "FORMAT")
    except (ValueError, UnicodeError, RecursionError, yaml.YAMLError):
        raise ArtifactError("EASYPROXY_SBA_ARTIFACT_FORMAT") from None


def _owned(owner: Coordinator, permit: Permit, store: ArtifactStore) -> dict:
    require(store.store is owner.store and permit.action in ("deploy", "update", "preview", "refresh"), "IDENTITY")
    return owner.assert_owned(permit)


def _put(owner, permit, store, public, key, body, previous=None, *, immutable=False):
    owner.assert_owned(permit)
    result = store.put(key, body, previous, immutable=immutable)
    public.verify(key, body)
    owner.assert_owned(permit)
    return result


def publish_candidate(owner: Coordinator, permit: Permit, store: ArtifactStore,
                      public: PublicArtifacts, bodies: dict[str, bytes]) -> Candidate:
    _owned(owner, permit, store)
    _validate(bodies, RAW)
    # Copy immutable bytes before any provider write; callers cannot replace the mapping mid-stage.
    snapshot = dict(bodies)
    owner.claim_stage(permit, "aggregator-candidate")
    for key, body in snapshot.items():
        _put(owner, permit, store, public, f"candidate/{permit.operation_id}/{key}", body, immutable=True)
    return Candidate(permit.operation_id, snapshot)


def _previous(store: ArtifactStore, public: PublicArtifacts, active: dict | None):
    stored = store.get("manifests/stable.json")
    if stored is None:
        require(active is None, "STABLE_MISSING")
        require(all(store.get(key) is None for key in ARTIFACTS.values()), "UNOWNED_STABLE")
        return None, {}, {}
    manifest = decode_object(stored.body, require)
    require(manifest.get("schema_version") == 1 and isinstance(manifest.get("run_id"), str)
            and RUN.fullmatch(manifest["run_id"]) is not None and active is not None
            and manifest.get("root_commit") == active["sourceSha"]
            and manifest.get("application_version") == active["applicationVersion"], "STABLE_IDENTITY")
    rows = manifest.get("artifacts")
    require(type(rows) is list and len(rows) == len(ARTIFACTS)
            and {row.get("name") for row in rows if type(row) is dict} == set(ARTIFACTS), "STABLE_MANIFEST")
    prior = {}
    for row in rows:
        key = ARTIFACTS[row["name"]]
        release_key = f"releases/{manifest['run_id']}/{key}"
        require(row.get("stable_key") == key and row.get("release_key") == release_key, "STABLE_MANIFEST")
        immutable, stable = store.get(release_key), store.get(key)
        require(immutable is not None and stable is not None and immutable.body == stable.body
                and row.get("sha256") == hashlib.sha256(stable.body).hexdigest()
                and type(row.get("size")) is int and row["size"] == len(stable.body), "STABLE_DRIFT")
        public.verify(key, stable.body)
        prior[key] = stable
    released = store.get(f"releases/{manifest['run_id']}/manifest.json")
    require(released is not None and released.body == stored.body, "STABLE_MANIFEST")
    return stored, manifest, prior


def validated_audit(owner: Coordinator, permit: Permit, candidate: Candidate, audit, root: Path,
                    public: PublicArtifacts) -> tuple[list[str], str]:
    state = owner.assert_owned(permit)
    from aggregator_audit import Audit, candidate_digest
    from audit_image import AuditImage, image_identity
    require(type(candidate) is Candidate and candidate.operation_id == permit.operation_id, "IDENTITY")
    rows = inspect_checkout(root, owner.identity["repository"], state["writer"]["candidate"]["sourceSha"])
    aggregator_sha = next(row["sourceSha"] for row in rows if row["path"] == "upstreams/aggregator")
    require(type(audit) is Audit and audit.operation_id == permit.operation_id
            and audit.candidate_digest == candidate_digest(candidate) and audit.aggregator_sha == aggregator_sha, "AUDIT")
    require(type(audit.image) is AuditImage and audit.image.repository == owner.identity["repository"]
            and audit.image.source_sha == state["writer"]["candidate"]["sourceSha"], "AUDIT_IMAGE")
    image = image_identity(audit.image)
    audit = decode_object(audit.body, require)
    require(audit.get("container_cleanup_confirmed") is True, "AUDIT_CLEANUP")
    require(audit.get("validated_image") == image["imageId"] and audit.get("image_identity") == {
        key: image[key] for key in ("imageId", "sourceSha", "platform", "scope")}, "AUDIT_IMAGE")
    _validate(candidate.artifacts, RAW)
    require(type(audit) is dict and audit.get("audit_id") == "sba-" + permit.operation_id
            and type(audit.get("inputs")) is dict and type(audit.get("nodes")) is dict
            and audit["inputs"].get("subscriptions") == [public.origin + f"/candidate/{permit.operation_id}/subs/clash.yaml"]
            and audit["inputs"].get("proxy_uris") == [] and audit["inputs"].get("manifest_url") == ""
            and type(audit.get("compat_probe")) is dict and audit["compat_probe"].get("ok") is True, "AUDIT")
    stable_uris = audit["nodes"].get("stable_available_uris")
    require(type(stable_uris) is list and len(stable_uris) <= 100000
            and all(isinstance(uri, str) and re.fullmatch(r"[a-z][a-z0-9+.-]*://[^\s]+", uri)
                    for uri in stable_uris), "AUDIT")
    uris = list(dict.fromkeys(stable_uris))
    require(uris, "AUDIT")
    return uris, aggregator_sha


def promote_candidate(owner: Coordinator, permit: Permit, store: ArtifactStore, public: PublicArtifacts,
                      candidate: Candidate, *, audit, root: Path,
                      min_nodes: int = 1, min_sources: int = 1) -> dict:
    state = _owned(owner, permit, store)
    uris, aggregator_sha = validated_audit(owner, permit, candidate, audit, root, public)
    bodies = dict(candidate.artifacts)
    require(type(min_nodes) is int and min_nodes > 0 and type(min_sources) is int and min_sources > 0, "COUNTS")
    sources = json.loads(bodies["internal/crawledsubs.json"])
    require(type(sources) in (list, dict) and len(sources) >= min_sources and len(uris) >= min_nodes, "COUNTS")
    previous, manifest, prior = _previous(store, public, state["active"])
    counts = manifest.get("counts", {"nodes": 0, "sources": 0})
    require(type(counts) is dict and all(type(counts.get(key)) is int and counts[key] >= 0 for key in ("nodes", "sources")), "COUNTS")
    require(len(uris) >= counts["nodes"] * .40 and len(sources) >= counts["sources"] * .20, "COUNT_DROP")
    for key, body in bodies.items():
        actual = store.get(f"candidate/{permit.operation_id}/{key}")
        require(actual is not None and actual.body == body, "CANDIDATE_DRIFT")
        public.verify(f"candidate/{permit.operation_id}/{key}", body)
    bodies["subs/effective.txt"] = ("\n".join(uris) + "\n").encode("utf-8")
    bodies["subs/effective.json"] = _encode({"count": len(uris), "stable_available_uris": uris})
    release = state["writer"]["candidate"]
    result = {"schema_version": 1, "run_id": permit.operation_id,
              "generated_at": datetime.now(timezone.utc).isoformat(), "root_commit": release["sourceSha"],
              "application_version": release["applicationVersion"], "aggregator_commit": aggregator_sha,
              "previous_run_id": manifest.get("run_id", ""), "counts": {"nodes": len(uris), "sources": len(sources)},
              "artifacts": [{"name": name, "stable_key": key, "release_key": f"releases/{permit.operation_id}/{key}",
                             "sha256": hashlib.sha256(bodies[key]).hexdigest(), "size": len(bodies[key])}
                            for name, key in ARTIFACTS.items()]}
    encoded = _encode(result)
    owner.claim_stage(permit, "aggregator-promotion")
    for key, body in bodies.items():
        _put(owner, permit, store, public, f"releases/{permit.operation_id}/{key}", body, immutable=True)
    _put(owner, permit, store, public, f"releases/{permit.operation_id}/manifest.json", encoded, immutable=True)
    if previous:
        for key, obj in prior.items():
            snapshot_key = f"last-known-good/releases/{manifest['run_id']}/{key}"
            existing = store.get(snapshot_key)
            if existing is None:
                _put(owner, permit, store, public, snapshot_key, obj.body, immutable=True)
            else:
                require(existing.body == obj.body, "SNAPSHOT_DRIFT")
                public.verify(snapshot_key, obj.body)
            _put(owner, permit, store, public, "last-known-good/" + key, obj.body, store.get("last-known-good/" + key))
        _put(owner, permit, store, public, "last-known-good/manifest.json", previous.body,
             store.get("last-known-good/manifest.json"))
    for key, body in bodies.items():
        _put(owner, permit, store, public, key, body, prior.get(key))
    # The stable manifest is the final commit point. Partial stable writes never trigger rollback.
    _put(owner, permit, store, public, "manifests/stable.json", encoded, previous)
    return {"checks": {"candidate-verified": True, "stable-verified": True}, "runId": permit.operation_id,
            "manifestSha256": hashlib.sha256(encoded).hexdigest(), "counts": result["counts"]}
