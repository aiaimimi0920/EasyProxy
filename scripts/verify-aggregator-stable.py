#!/usr/bin/env python3

from __future__ import annotations

import argparse
import hashlib
import json
from typing import Any, Callable
from urllib.parse import urljoin

import requests


def sha256_bytes(body: bytes) -> str:
    return hashlib.sha256(body).hexdigest()


def fetch_bytes(url: str) -> bytes:
    response = requests.get(url, headers={"Cache-Control": "no-cache"}, timeout=30)
    response.raise_for_status()
    return response.content


def verify_stable(
    base_url: str,
    artifact_name: str,
    expected_url: str,
    *,
    fetch: Callable[[str], bytes] = fetch_bytes,
) -> dict[str, Any]:
    normalized_base = base_url.rstrip("/") + "/"
    manifest_url = urljoin(normalized_base, "manifests/stable.json")
    try:
        manifest = json.loads(fetch(manifest_url).decode("utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise RuntimeError("Aggregator stable manifest is not valid UTF-8 JSON") from exc
    if not isinstance(manifest, dict) or not str(manifest.get("run_id") or "").strip():
        raise RuntimeError("Aggregator stable manifest does not identify a release")

    artifacts = manifest.get("artifacts") or []
    artifact = next((item for item in artifacts if str(item.get("name") or "") == artifact_name), None)
    if not isinstance(artifact, dict):
        raise RuntimeError(f"Aggregator stable manifest does not contain {artifact_name}")
    stable_key = str(artifact.get("stable_key") or "").strip().lstrip("/")
    release_key = str(artifact.get("release_key") or "").strip().lstrip("/")
    expected_hash = str(artifact.get("sha256") or "").strip().lower()
    if not stable_key or not release_key or len(expected_hash) != 64:
        raise RuntimeError(f"Aggregator manifest entry for {artifact_name} is incomplete")

    canonical_url = urljoin(normalized_base, stable_key)
    if expected_url.rstrip("/") != canonical_url.rstrip("/"):
        raise RuntimeError(
            f"Configured Aggregator URL must use the canonical stable artifact: expected {canonical_url}, got {expected_url}"
        )
    stable_body = fetch(canonical_url)
    release_body = fetch(urljoin(normalized_base, release_key))
    if sha256_bytes(stable_body) != expected_hash:
        raise RuntimeError(f"Aggregator stable artifact hash does not match manifest: {stable_key}")
    if sha256_bytes(release_body) != expected_hash:
        raise RuntimeError(f"Aggregator immutable release artifact hash does not match manifest: {release_key}")
    return {
        "run_id": manifest["run_id"],
        "artifact_name": artifact_name,
        "stable_url": canonical_url,
        "release_url": urljoin(normalized_base, release_key),
        "sha256": expected_hash,
    }


def main() -> int:
    parser = argparse.ArgumentParser(description="Verify a canonical Aggregator stable artifact against its release manifest.")
    parser.add_argument("--base-url", required=True)
    parser.add_argument("--artifact-name", default="public-effective")
    parser.add_argument("--expected-url", required=True)
    args = parser.parse_args()
    result = verify_stable(args.base_url, args.artifact_name, args.expected_url)
    print(json.dumps(result, ensure_ascii=False, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
