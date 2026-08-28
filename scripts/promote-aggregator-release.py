#!/usr/bin/env python3

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
from datetime import datetime, timezone
from io import BytesIO
from pathlib import Path
from typing import Any, Callable
from urllib.parse import quote

import boto3
import requests


MANIFEST_CONTENT_TYPE = "application/json; charset=utf-8"
RUN_ID_PATTERN = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$")
OBJECT_KEY_PATTERN = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._/-]{0,511}$")


def json_bytes(value: Any) -> bytes:
    return (json.dumps(value, ensure_ascii=False, indent=2, sort_keys=True) + "\n").encode("utf-8")


def sha256_bytes(body: bytes) -> str:
    return hashlib.sha256(body).hexdigest()


def response_status(exc: Exception) -> int | None:
    response = getattr(exc, "response", None)
    if isinstance(response, dict):
        metadata = response.get("ResponseMetadata") or {}
        try:
            return int(metadata.get("HTTPStatusCode"))
        except (TypeError, ValueError):
            return None
    return None


def get_object_or_none(client: Any, bucket: str, key: str) -> dict[str, Any] | None:
    try:
        response = client.get_object(Bucket=bucket, Key=key)
    except (KeyError, FileNotFoundError):
        return None
    except Exception as exc:
        if response_status(exc) == 404:
            return None
        raise
    body = response.get("Body")
    raw = body.read() if hasattr(body, "read") else body
    if isinstance(raw, str):
        raw = raw.encode("utf-8")
    if not isinstance(raw, bytes):
        raise RuntimeError(f"R2 returned an invalid body for {key}")
    return {
        "body": raw,
        "content_type": str(response.get("ContentType") or "application/octet-stream"),
        "cache_control": str(response.get("CacheControl") or "max-age=60"),
    }


def put_object(client: Any, bucket: str, key: str, body: bytes, content_type: str, cache_control: str = "max-age=60") -> None:
    client.put_object(
        Bucket=bucket,
        Key=key,
        Body=body,
        ContentType=content_type,
        CacheControl=cache_control,
    )


def delete_object(client: Any, bucket: str, key: str) -> None:
    client.delete_object(Bucket=bucket, Key=key)


def validate_artifact(name: str, body: bytes) -> None:
    if not body.strip():
        raise RuntimeError(f"candidate artifact is empty: {name}")
    text = body.decode("utf-8", errors="replace")
    if name == "public-clash" and not any(marker in text for marker in ("proxies:", "proxy-groups:", "proxy-providers:")):
        raise RuntimeError("candidate public-clash is not a Clash document")
    if name in {"public-singbox", "public-effective-json", "crawledsubs"}:
        try:
            payload = json.loads(text)
        except json.JSONDecodeError as exc:
            raise RuntimeError(f"candidate {name} is not valid JSON") from exc
        if payload in ({}, [], "", None):
            raise RuntimeError(f"candidate {name} contains an empty JSON value")


def validate_object_key(key: str, label: str) -> None:
    if not OBJECT_KEY_PATTERN.fullmatch(key) or key.endswith("/") or ".." in key.split("/") or "//" in key:
        raise RuntimeError(f"publication plan contains an unsafe {label}")


def validate_plan(plan: dict[str, Any]) -> tuple[str, str, list[dict[str, Any]]]:
    run_id = str(plan.get("run_id") or "").strip()
    bucket = str(plan.get("bucket") or "").strip()
    raw_items = plan.get("items") or []
    if plan.get("schema_version") != 1 or not RUN_ID_PATTERN.fullmatch(run_id) or not bucket or not isinstance(raw_items, list):
        raise RuntimeError("publication plan is incomplete or unsupported")
    expected_manifests = {
        "candidate_manifest_key": "candidate/manifest.json",
        "release_manifest_key": f"releases/{run_id}/manifest.json",
        "stable_manifest_key": "manifests/stable.json",
        "lkg_manifest_key": "last-known-good/manifest.json",
    }
    for field, expected in expected_manifests.items():
        if plan.get(field) != expected:
            raise RuntimeError(f"publication plan {field} does not match release {run_id}")

    items: list[dict[str, Any]] = []
    names: set[str] = set()
    stable_keys: set[str] = set()
    for item in raw_items:
        if not isinstance(item, dict) or not item.get("publish"):
            continue
        name = str(item.get("name") or "").strip()
        stable_key = str(item.get("stable_key") or "").strip()
        content_type = str(item.get("content_type") or "").strip()
        if not name or name in names or stable_key in stable_keys or str(item.get("bucket") or "") != bucket or not content_type:
            raise RuntimeError("publication plan contains duplicate or incomplete public items")
        validate_object_key(stable_key, f"stable key for {name}")
        expected_keys = {
            "candidate_run_key": f"candidate/{run_id}/{stable_key}",
            "candidate_key": f"candidate/{stable_key}",
            "release_key": f"releases/{run_id}/{stable_key}",
            "lkg_key": f"last-known-good/{stable_key}",
        }
        for field, expected in expected_keys.items():
            if item.get(field) != expected:
                raise RuntimeError(f"publication plan {field} is invalid for {name}")
        names.add(name)
        stable_keys.add(stable_key)
        items.append(item)
    if not items:
        raise RuntimeError("publication plan does not contain public artifacts")
    return run_id, bucket, items


def count_sources(payload: Any) -> int:
    if isinstance(payload, list):
        return len(payload)
    if isinstance(payload, dict):
        for key in ("subscriptions", "sources", "subs", "data"):
            if isinstance(payload.get(key), (list, dict)):
                return len(payload[key])
        return len(payload)
    return 0


def enforce_count_gate(label: str, current: int, minimum: int, previous: int, max_drop_ratio: float) -> None:
    if current < minimum:
        raise RuntimeError(f"{label} count {current} is below minimum {minimum}")
    if previous > 0:
        floor = previous * (1.0 - max_drop_ratio)
        if current < floor:
            raise RuntimeError(
                f"{label} count {current} exceeds allowed drop from previous {previous} "
                f"(max drop ratio {max_drop_ratio:.3f})"
            )


def default_public_fetch(base_url: str, key: str, run_id: str) -> bytes:
    encoded_key = "/".join(quote(part, safe="") for part in key.split("/"))
    response = requests.get(
        f"{base_url.rstrip('/')}/{encoded_key}",
        params={"easyproxy_release": run_id},
        headers={"Cache-Control": "no-cache"},
        timeout=30,
    )
    response.raise_for_status()
    return response.content


def verify_public(public_fetch: Callable[[str, str, str], bytes], base_url: str, key: str, run_id: str, expected: bytes) -> None:
    actual = public_fetch(base_url, key, run_id)
    if sha256_bytes(actual) != sha256_bytes(expected):
        raise RuntimeError(f"public artifact hash mismatch: {key}")


def restore_objects(client: Any, bucket: str, previous: dict[str, dict[str, Any] | None], keys: list[str]) -> None:
    errors: list[str] = []
    for key in reversed(keys):
        prior = previous[key]
        try:
            if prior is None:
                delete_object(client, bucket, key)
            else:
                put_object(
                    client,
                    bucket,
                    key,
                    prior["body"],
                    prior["content_type"],
                    prior["cache_control"],
                )
        except Exception as exc:  # pragma: no cover - multiple rollback failure aggregation
            errors.append(f"{key}: {exc}")
    if errors:
        raise RuntimeError("stable rollback failed: " + "; ".join(errors))


def reconcile_committed_stable(
    client: Any,
    bucket: str,
    manifest: dict[str, Any],
    *,
    public_base_url: str,
    public_fetch: Callable[[str, str, str], bytes],
) -> None:
    if not manifest:
        return
    run_id = str(manifest.get("run_id") or "").strip()
    artifacts = manifest.get("artifacts") or []
    if not RUN_ID_PATTERN.fullmatch(run_id) or not isinstance(artifacts, list) or not artifacts:
        raise RuntimeError("existing stable manifest is incomplete; refusing promotion")
    for artifact in artifacts:
        stable_key = str(artifact.get("stable_key") or "").strip()
        release_key = str(artifact.get("release_key") or "").strip()
        expected_hash = str(artifact.get("sha256") or "").strip().lower()
        validate_object_key(stable_key, "stable manifest key")
        if release_key != f"releases/{run_id}/{stable_key}" or len(expected_hash) != 64:
            raise RuntimeError("existing stable manifest contains an invalid release mapping")
        release = get_object_or_none(client, bucket, release_key)
        if release is None or sha256_bytes(release["body"]) != expected_hash:
            raise RuntimeError(f"committed immutable release is missing or corrupt: {release_key}")
        stable = get_object_or_none(client, bucket, stable_key)
        if stable is not None and sha256_bytes(stable["body"]) == expected_hash:
            continue
        content_type = str(artifact.get("content_type") or release["content_type"])
        put_object(client, bucket, stable_key, release["body"], content_type)
        verify_public(public_fetch, public_base_url, stable_key, run_id, release["body"])


def promote_release(
    client: Any,
    plan: dict[str, Any],
    audit: dict[str, Any],
    *,
    public_base_url: str,
    root_commit: str,
    aggregator_commit: str,
    min_nodes: int,
    min_sources: int,
    max_node_drop_ratio: float,
    max_source_drop_ratio: float,
    public_fetch: Callable[[str, str, str], bytes] = default_public_fetch,
) -> dict[str, Any]:
    run_id, bucket, items = validate_plan(plan)
    if not public_base_url.strip():
        raise RuntimeError("public base URL is required")
    for ratio_name, ratio in (("max-node-drop-ratio", max_node_drop_ratio), ("max-source-drop-ratio", max_source_drop_ratio)):
        if not 0 <= ratio <= 1:
            raise RuntimeError(f"{ratio_name} must be between 0 and 1")

    stable_manifest_key = str(plan["stable_manifest_key"])
    previous_manifest_object = get_object_or_none(client, bucket, stable_manifest_key)
    previous_manifest: dict[str, Any] = {}
    if previous_manifest_object:
        try:
            previous_manifest = json.loads(previous_manifest_object["body"].decode("utf-8"))
        except json.JSONDecodeError as exc:
            raise RuntimeError("existing stable manifest is invalid JSON; refusing promotion") from exc
        reconcile_committed_stable(
            client,
            bucket,
            previous_manifest,
            public_base_url=public_base_url,
            public_fetch=public_fetch,
        )

    candidates: dict[str, dict[str, Any]] = {}
    for item in items:
        candidate = get_object_or_none(client, bucket, item["candidate_run_key"])
        if candidate is None:
            raise RuntimeError(f"candidate artifact is missing: {item['name']}")
        validate_artifact(str(item["name"]), candidate["body"])
        candidates[str(item["name"])] = candidate

    stable_uris = list((audit.get("nodes") or {}).get("stable_available_uris") or [])
    node_count = len({str(uri).strip() for uri in stable_uris if str(uri).strip()})
    crawled = candidates.get("crawledsubs")
    source_count = count_sources(json.loads(crawled["body"].decode("utf-8"))) if crawled else 0

    previous_counts = previous_manifest.get("counts") or {}
    enforce_count_gate("node", node_count, min_nodes, int(previous_counts.get("nodes") or 0), max_node_drop_ratio)
    enforce_count_gate("source", source_count, min_sources, int(previous_counts.get("sources") or 0), max_source_drop_ratio)

    generated_at = datetime.now(timezone.utc).isoformat()
    manifest = {
        "schema_version": 1,
        "run_id": run_id,
        "generated_at": generated_at,
        "root_commit": root_commit,
        "aggregator_commit": aggregator_commit,
        "audit_id": str(audit.get("audit_id") or ""),
        "previous_run_id": str(previous_manifest.get("run_id") or ""),
        "counts": {"nodes": node_count, "sources": source_count},
        "artifacts": [],
    }
    for item in items:
        candidate = candidates[str(item["name"])]
        manifest["artifacts"].append(
            {
                "name": item["name"],
                "stable_key": item["stable_key"],
                "release_key": item["release_key"],
                "sha256": sha256_bytes(candidate["body"]),
                "size": len(candidate["body"]),
                "content_type": item["content_type"],
            }
        )
    manifest_body = json_bytes(manifest)

    for item in items:
        body = candidates[str(item["name"])]["body"]
        put_object(client, bucket, item["release_key"], body, item["content_type"], "public, max-age=31536000, immutable")
        verify_public(public_fetch, public_base_url, item["release_key"], run_id, body)
    put_object(client, bucket, str(plan["release_manifest_key"]), manifest_body, MANIFEST_CONTENT_TYPE, "public, max-age=31536000, immutable")
    verify_public(public_fetch, public_base_url, str(plan["release_manifest_key"]), run_id, manifest_body)

    for item in items:
        body = candidates[str(item["name"])]["body"]
        put_object(client, bucket, item["candidate_key"], body, item["content_type"])
        verify_public(public_fetch, public_base_url, item["candidate_key"], run_id, body)
    put_object(client, bucket, str(plan["candidate_manifest_key"]), manifest_body, MANIFEST_CONTENT_TYPE)
    verify_public(public_fetch, public_base_url, str(plan["candidate_manifest_key"]), run_id, manifest_body)

    stable_keys = [str(item["stable_key"]) for item in items] + [stable_manifest_key]
    previous = {key: get_object_or_none(client, bucket, key) for key in stable_keys}
    snapshot_id = str(previous_manifest.get("run_id") or f"before-{run_id}")
    for key, prior in previous.items():
        if prior is None:
            continue
        put_object(client, bucket, f"last-known-good/releases/{snapshot_id}/{key}", prior["body"], prior["content_type"], "public, max-age=31536000, immutable")
        put_object(client, bucket, f"last-known-good/{key}", prior["body"], prior["content_type"])
    if previous_manifest_object:
        put_object(client, bucket, str(plan["lkg_manifest_key"]), previous_manifest_object["body"], MANIFEST_CONTENT_TYPE)

    touched: list[str] = []
    try:
        for item in items:
            key = str(item["stable_key"])
            body = candidates[str(item["name"])]["body"]
            touched.append(key)
            put_object(client, bucket, key, body, item["content_type"])
            verify_public(public_fetch, public_base_url, key, run_id, body)
        touched.append(stable_manifest_key)
        put_object(client, bucket, stable_manifest_key, manifest_body, MANIFEST_CONTENT_TYPE, "no-cache")
        verify_public(public_fetch, public_base_url, stable_manifest_key, run_id, manifest_body)
    except Exception as promotion_error:
        try:
            restore_objects(client, bucket, previous, touched)
        except Exception as rollback_error:
            raise RuntimeError(f"stable promotion failed ({promotion_error}); {rollback_error}") from rollback_error
        raise RuntimeError(f"stable promotion failed and previous stable objects were restored: {promotion_error}") from promotion_error

    return manifest


def build_client() -> Any:
    access_key = os.environ.get("R2_ACCESS_KEY_ID", "").strip()
    secret_key = os.environ.get("R2_SECRET_ACCESS_KEY", "").strip()
    account_id = os.environ.get("R2_ACCOUNT_ID", "").strip()
    if not access_key or not secret_key or not account_id:
        raise RuntimeError("R2_ACCESS_KEY_ID, R2_SECRET_ACCESS_KEY, and R2_ACCOUNT_ID are required")
    return boto3.client(
        "s3",
        endpoint_url=f"https://{account_id}.r2.cloudflarestorage.com",
        aws_access_key_id=access_key,
        aws_secret_access_key=secret_key,
        region_name="auto",
    )


def main() -> int:
    parser = argparse.ArgumentParser(description="Validate and safely promote one Aggregator candidate release.")
    parser.add_argument("--plan", required=True)
    parser.add_argument("--audit-summary", required=True)
    parser.add_argument("--public-base-url", required=True)
    parser.add_argument("--root-commit", required=True)
    parser.add_argument("--aggregator-commit", required=True)
    parser.add_argument("--min-nodes", type=int, default=1)
    parser.add_argument("--min-sources", type=int, default=1)
    parser.add_argument("--max-node-drop-ratio", type=float, default=0.60)
    parser.add_argument("--max-source-drop-ratio", type=float, default=0.80)
    parser.add_argument("--output", required=True)
    args = parser.parse_args()

    plan = json.loads(Path(args.plan).read_text(encoding="utf-8"))
    audit = json.loads(Path(args.audit_summary).read_text(encoding="utf-8"))
    manifest = promote_release(
        build_client(),
        plan,
        audit,
        public_base_url=args.public_base_url,
        root_commit=args.root_commit,
        aggregator_commit=args.aggregator_commit,
        min_nodes=args.min_nodes,
        min_sources=args.min_sources,
        max_node_drop_ratio=args.max_node_drop_ratio,
        max_source_drop_ratio=args.max_source_drop_ratio,
    )
    output = Path(args.output)
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(json.dumps(manifest, ensure_ascii=False, indent=2), encoding="utf-8")
    print(json.dumps(manifest, ensure_ascii=False, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
