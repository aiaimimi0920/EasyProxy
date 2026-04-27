#!/usr/bin/env python3

from __future__ import annotations

import argparse
import hashlib
import json
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

import boto3


def hash_hex(path: Path, algorithm: str) -> str:
    hasher = hashlib.new(algorithm)
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            hasher.update(chunk)
    return hasher.hexdigest()


def build_s3_client(
    *,
    account_id: str,
    endpoint: str,
    access_key_id: str,
    secret_access_key: str,
):
    endpoint_url = endpoint.strip() if endpoint.strip() else f"https://{account_id}.r2.cloudflarestorage.com"
    return boto3.client(
        "s3",
        endpoint_url=endpoint_url,
        region_name="auto",
        aws_access_key_id=access_key_id,
        aws_secret_access_key=secret_access_key,
    )


def upload_file(client: Any, *, bucket: str, object_key: str, source_path: Path, content_type: str) -> dict[str, Any]:
    client.upload_file(
        str(source_path),
        bucket,
        object_key,
        ExtraArgs={"ContentType": content_type},
    )
    return {
        "bucket": bucket,
        "objectKey": object_key,
        "sizeBytes": source_path.stat().st_size,
        "md5": hash_hex(source_path, "md5"),
        "sha256": hash_hex(source_path, "sha256"),
        "contentType": content_type,
    }


def main() -> int:
    parser = argparse.ArgumentParser(description="Upload rendered EasyProxy service/base runtime config artifacts to Cloudflare R2.")
    parser.add_argument("--account-id", required=True)
    parser.add_argument("--bucket", required=True)
    parser.add_argument("--access-key-id", required=True)
    parser.add_argument("--secret-access-key", required=True)
    parser.add_argument("--config-path", required=True)
    parser.add_argument("--config-object-key", required=True)
    parser.add_argument("--manifest-object-key", required=True)
    parser.add_argument("--endpoint", default="")
    parser.add_argument("--release-version", default="")
    parser.add_argument("--manifest-output", default="")
    args = parser.parse_args()

    config_path = Path(args.config_path).resolve()
    if not config_path.exists():
        raise SystemExit(f"Rendered service config not found: {config_path}")

    client = build_s3_client(
        account_id=args.account_id,
        endpoint=args.endpoint,
        access_key_id=args.access_key_id,
        secret_access_key=args.secret_access_key,
    )

    config_upload = upload_file(
        client,
        bucket=args.bucket,
        object_key=args.config_object_key,
        source_path=config_path,
        content_type="application/x-yaml",
    )

    manifest = {
        "schemaVersion": 1,
        "generatedAtUtc": datetime.now(timezone.utc).isoformat(),
        "releaseVersion": args.release_version.strip(),
        "accountId": args.account_id,
        "endpoint": args.endpoint.strip() or f"https://{args.account_id}.r2.cloudflarestorage.com",
        "bucket": args.bucket,
        "manifestObjectKey": args.manifest_object_key,
        "serviceBase": {
            "config": config_upload,
            "fingerprint": config_upload["sha256"],
        },
    }

    manifest_text = json.dumps(manifest, ensure_ascii=False, indent=2)
    if args.manifest_output:
        manifest_path = Path(args.manifest_output).resolve()
    else:
        manifest_path = config_path.parent / "easyproxy-distribution-manifest.json"
    manifest_path.parent.mkdir(parents=True, exist_ok=True)
    manifest_path.write_text(manifest_text, encoding="utf-8")

    client.upload_file(
        str(manifest_path),
        args.bucket,
        args.manifest_object_key,
        ExtraArgs={"ContentType": "application/json"},
    )

    if args.manifest_output:
        print(str(manifest_path))
    else:
        print(manifest_text)

    return 0


if __name__ == "__main__":
    raise SystemExit(main())
