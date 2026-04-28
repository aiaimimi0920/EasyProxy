#!/usr/bin/env python3

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
from typing import Any

import boto3


def load_json(path: Path) -> dict[str, Any]:
    return json.loads(path.read_text(encoding="utf-8"))


def get_storage_item(runtime_config: dict[str, Any], item_name: str) -> dict[str, Any]:
    storage = runtime_config.get("storage") or {}
    items = storage.get("items") or {}
    item = items.get(item_name)
    if not isinstance(item, dict):
        raise RuntimeError(f"runtime config missing storage.items.{item_name}")
    return item


def unique_preserve(values: list[str]) -> list[str]:
    seen: set[str] = set()
    result: list[str] = []
    for raw in values:
        value = str(raw or "").strip()
        if not value or value in seen:
            continue
        seen.add(value)
        result.append(value)
    return result


def put_text_object(s3_client, bucket: str, key: str, body: str, content_type: str) -> None:
    s3_client.put_object(
        Bucket=bucket,
        Key=key,
        Body=body.encode("utf-8"),
        ContentType=content_type,
        CacheControl="max-age=60",
    )


def main() -> int:
    parser = argparse.ArgumentParser(description="Publish audited effective aggregator proxy URIs to R2.")
    parser.add_argument("--runtime-config", required=True)
    parser.add_argument("--audit-summary", required=True)
    args = parser.parse_args()

    access_key = os.environ.get("R2_ACCESS_KEY_ID", "").strip()
    secret_key = os.environ.get("R2_SECRET_ACCESS_KEY", "").strip()
    account_id = os.environ.get("R2_ACCOUNT_ID", "").strip()
    if not access_key or not secret_key or not account_id:
        raise RuntimeError("R2_ACCESS_KEY_ID, R2_SECRET_ACCESS_KEY, and R2_ACCOUNT_ID are required")

    runtime_config = load_json(Path(args.runtime_config))
    audit_summary = load_json(Path(args.audit_summary))
    stable_uris = unique_preserve(list((audit_summary.get("nodes") or {}).get("stable_available_uris") or []))
    if not stable_uris:
        raise RuntimeError("audit summary did not produce any stable_available_uris")

    effective_item = get_storage_item(runtime_config, "public-effective")
    effective_json_item = get_storage_item(runtime_config, "public-effective-json")

    bucket = str(effective_item.get("bucket") or "").strip()
    key = str(effective_item.get("key") or "").strip()
    json_bucket = str(effective_json_item.get("bucket") or "").strip()
    json_key = str(effective_json_item.get("key") or "").strip()
    if not bucket or not key or not json_bucket or not json_key:
        raise RuntimeError("effective storage item is missing bucket/key configuration")

    endpoint = f"https://{account_id}.r2.cloudflarestorage.com"
    s3_client = boto3.client(
        "s3",
        endpoint_url=endpoint,
        aws_access_key_id=access_key,
        aws_secret_access_key=secret_key,
        region_name="auto",
    )

    text_body = "\n".join(stable_uris).rstrip() + "\n"
    json_body = {
        "generated_at": audit_summary.get("audit_id", ""),
        "count": len(stable_uris),
        "stable_available_uris": stable_uris,
        "audit_summary": audit_summary,
    }

    put_text_object(s3_client, bucket, key, text_body, "text/plain; charset=utf-8")
    put_text_object(s3_client, json_bucket, json_key, json.dumps(json_body, ensure_ascii=False, indent=2), "application/json; charset=utf-8")

    print(
        json.dumps(
            {
                "bucket": bucket,
                "key": key,
                "json_bucket": json_bucket,
                "json_key": json_key,
                "count": len(stable_uris),
            },
            ensure_ascii=False,
            indent=2,
        )
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
