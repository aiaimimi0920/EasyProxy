#!/usr/bin/env python3

from __future__ import annotations

import argparse
import json
import re
from pathlib import Path
from typing import Any


RUN_ID_PATTERN = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$")
OBJECT_KEY_PATTERN = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._/-]{0,511}$")
NON_PUBLIC_ITEMS = {"crawledproxies"}


def content_type_for(key: str) -> str:
    suffix = Path(key).suffix.lower()
    if suffix == ".json":
        return "application/json; charset=utf-8"
    if suffix in {".yaml", ".yml"}:
        return "application/yaml; charset=utf-8"
    return "text/plain; charset=utf-8"


def normalize_key(raw: Any, item_name: str) -> str:
    key = str(raw or "").strip().lstrip("/")
    if not OBJECT_KEY_PATTERN.fullmatch(key) or key.endswith("/") or ".." in key.split("/") or "//" in key:
        raise RuntimeError(f"storage.items.{item_name}.key is not a safe object key")
    if key.startswith(("candidate/", "releases/", "last-known-good/", "manifests/")):
        raise RuntimeError(f"storage.items.{item_name}.key must be a canonical stable key")
    return key


def materialize_release(runtime_config: dict[str, Any], run_id: str) -> tuple[dict[str, Any], dict[str, Any]]:
    if not RUN_ID_PATTERN.fullmatch(run_id):
        raise RuntimeError("run-id must contain only letters, numbers, dot, underscore, or dash")

    rendered = json.loads(json.dumps(runtime_config))
    storage = rendered.get("storage")
    items = storage.get("items") if isinstance(storage, dict) else None
    if not isinstance(items, dict) or not items:
        raise RuntimeError("runtime config does not define storage.items")

    plan_items: list[dict[str, Any]] = []
    buckets: set[str] = set()
    for name, item in items.items():
        if not isinstance(item, dict):
            raise RuntimeError(f"storage.items.{name} must be an object")
        bucket = str(item.get("bucket") or "").strip()
        if not bucket:
            raise RuntimeError(f"storage.items.{name}.bucket is required")
        stable_key = normalize_key(item.get("key"), name)
        candidate_run_key = f"candidate/{run_id}/{stable_key}"
        item["key"] = candidate_run_key
        buckets.add(bucket)
        plan_items.append(
            {
                "name": name,
                "bucket": bucket,
                "stable_key": stable_key,
                "candidate_run_key": candidate_run_key,
                "candidate_key": f"candidate/{stable_key}",
                "release_key": f"releases/{run_id}/{stable_key}",
                "lkg_key": f"last-known-good/{stable_key}",
                "content_type": content_type_for(stable_key),
                "publish": name not in NON_PUBLIC_ITEMS,
            }
        )

    if len(buckets) != 1:
        raise RuntimeError("safe publication currently requires all public artifacts to use one R2 bucket")

    plan = {
        "schema_version": 1,
        "run_id": run_id,
        "bucket": next(iter(buckets)),
        "candidate_manifest_key": "candidate/manifest.json",
        "release_manifest_key": f"releases/{run_id}/manifest.json",
        "stable_manifest_key": "manifests/stable.json",
        "lkg_manifest_key": "last-known-good/manifest.json",
        "items": plan_items,
    }
    return rendered, plan


def main() -> int:
    parser = argparse.ArgumentParser(description="Rewrite Aggregator storage keys for one isolated candidate run.")
    parser.add_argument("--runtime-config", required=True)
    parser.add_argument("--run-id", required=True)
    parser.add_argument("--output-runtime-config", required=True)
    parser.add_argument("--output-plan", required=True)
    args = parser.parse_args()

    source = json.loads(Path(args.runtime_config).read_text(encoding="utf-8"))
    rendered, plan = materialize_release(source, args.run_id)
    runtime_output = Path(args.output_runtime_config)
    plan_output = Path(args.output_plan)
    runtime_output.parent.mkdir(parents=True, exist_ok=True)
    plan_output.parent.mkdir(parents=True, exist_ok=True)
    runtime_output.write_text(json.dumps(rendered, ensure_ascii=False, indent=2), encoding="utf-8")
    plan_output.write_text(json.dumps(plan, ensure_ascii=False, indent=2), encoding="utf-8")
    print(json.dumps({"run_id": args.run_id, "item_count": len(plan["items"])}, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
