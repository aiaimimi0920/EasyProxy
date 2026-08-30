#!/usr/bin/env python3

from __future__ import annotations

import argparse
import base64
import copy
import hashlib
import json
import os
from pathlib import Path
from typing import Any

import requests
from cryptography.hazmat.primitives import hashes
from cryptography.hazmat.primitives.ciphers.aead import AESGCM
from cryptography.hazmat.primitives.kdf.pbkdf2 import PBKDF2HMAC


BACKUP_AAD = b"MiSub encrypted backup v1"
BACKUP_ITERATIONS = 310_000


def normalize_proxy_type(value: str) -> str:
    normalized = value.strip().lower()
    if normalized in {"all", "any", "*"}:
        return ""
    return normalized


def ensure(condition: bool, message: str) -> None:
    if not condition:
        raise RuntimeError(message)


def encrypt_backup(payload: dict[str, Any], passphrase: str) -> dict[str, Any]:
    ensure(bool(passphrase.strip()), "EASYPROXY_BACKUP_PASSPHRASE is required")
    salt = os.urandom(16)
    iv = os.urandom(12)
    key = PBKDF2HMAC(
        algorithm=hashes.SHA256(),
        length=32,
        salt=salt,
        iterations=BACKUP_ITERATIONS,
    ).derive(passphrase.encode())
    ciphertext = AESGCM(key).encrypt(
        iv,
        json.dumps(payload, ensure_ascii=False, separators=(",", ":")).encode(),
        BACKUP_AAD,
    )
    return {
        "format": "misub-encrypted-backup",
        "version": 1,
        "kdf": {
            "name": "PBKDF2",
            "hash": "SHA-256",
            "iterations": BACKUP_ITERATIONS,
            "salt": base64.b64encode(salt).decode(),
        },
        "cipher": {"name": "AES-GCM", "iv": base64.b64encode(iv).decode()},
        "ciphertext": base64.b64encode(ciphertext).decode(),
    }


def update_zen_source(
    sources: list[dict[str, Any]], source_id: str, count: int, proxy_type: str, chatgpt: bool
) -> list[dict[str, Any]]:
    updated = copy.deepcopy(sources)
    matches = [source for source in updated if str(source.get("id", "")).strip() == source_id]
    ensure(len(matches) == 1, f"expected exactly one MiSub source {source_id!r}")
    source = matches[0]
    options = source.get("options") if isinstance(source.get("options"), dict) else {}
    direct = source.get("connector_config")
    nested = options.get("connector_config")
    current = direct if isinstance(direct, dict) else nested
    ensure(isinstance(current, dict), f"MiSub source {source_id!r} has no connector config")
    ensure(bool(str(current.get("api_key", "")).strip()), "ZenProxy connector api_key is missing")

    connector_config = copy.deepcopy(current)
    connector_config.update(
        {
            "count": count,
            "type": proxy_type,
            "chatgpt": chatgpt,
            "google": False,
            "residential": False,
        }
    )
    source["connector_type"] = "zenproxy_client"
    source["connector_config"] = copy.deepcopy(connector_config)
    options["connector_type"] = "zenproxy_client"
    options["connector_config"] = copy.deepcopy(connector_config)
    source["options"] = options
    return updated


def verify_logical_backup(backup: dict[str, Any]) -> None:
    ensure(backup.get("format") == "misub-logical-backup", "unexpected MiSub backup format")
    data = backup.get("data")
    ensure(isinstance(data, dict), "MiSub backup data is missing")
    canonical = json.dumps(data, ensure_ascii=False, sort_keys=True, separators=(",", ":"))
    expected = str((backup.get("integrity") or {}).get("canonicalDataSha256", ""))
    ensure(hashlib.sha256(canonical.encode()).hexdigest() == expected, "MiSub backup checksum mismatch")


def save_state(session: requests.Session, base_url: str, payload: dict[str, Any]) -> None:
    response = session.post(base_url + "api/misubs", json=payload, timeout=60)
    response.raise_for_status()
    ensure(response.json().get("success") is True, "MiSub state update did not report success")


def main() -> int:
    parser = argparse.ArgumentParser(description="Safely update a MiSub ZenProxy connector source")
    parser.add_argument("--base-url", required=True)
    parser.add_argument("--source-id", default="conn_zenproxy_primary")
    parser.add_argument("--count", type=int, default=10)
    parser.add_argument("--type", dest="proxy_type", default="http")
    parser.add_argument("--chatgpt", action=argparse.BooleanOptionalAction, default=True)
    parser.add_argument("--backup-output", type=Path, required=True)
    args = parser.parse_args()
    ensure(args.count > 0, "count must be positive")
    args.proxy_type = normalize_proxy_type(args.proxy_type)

    admin_password = os.environ.get("MISUB_ADMIN_PASSWORD", "")
    manifest_token = os.environ.get("MISUB_MANIFEST_TOKEN", "")
    backup_passphrase = os.environ.get("EASYPROXY_BACKUP_PASSPHRASE", "")
    ensure(bool(admin_password), "MISUB_ADMIN_PASSWORD is required")
    ensure(bool(manifest_token), "MISUB_MANIFEST_TOKEN is required")
    base_url = args.base_url.rstrip("/") + "/"

    session = requests.Session()
    login = session.post(base_url + "api/login", json={"password": admin_password}, timeout=30)
    login.raise_for_status()
    ensure(login.json().get("success") is True, "MiSub login did not report success")
    response = session.get(base_url + "api/data", timeout=30)
    response.raise_for_status()
    data = response.json()
    original = {"misubs": data.get("misubs") or [], "profiles": data.get("profiles") or []}

    export_response = session.post(base_url + "api/system/export", json={}, timeout=60)
    export_response.raise_for_status()
    export_payload = export_response.json()
    ensure(export_payload.get("success") is True, "MiSub export did not report success")
    backup = export_payload.get("exportData") or {}
    verify_logical_backup(backup)
    args.backup_output.parent.mkdir(parents=True, exist_ok=True)
    args.backup_output.write_text(
        json.dumps(encrypt_backup(backup, backup_passphrase), separators=(",", ":")),
        encoding="utf-8",
    )

    candidate = {
        "misubs": update_zen_source(
            original["misubs"], args.source_id, args.count, args.proxy_type, args.chatgpt
        ),
        "profiles": original["profiles"],
    }
    try:
        save_state(session, base_url, candidate)
        manifest = session.get(
            base_url + "api/manifest/aggregator-global",
            headers={"Authorization": f"Bearer {manifest_token}"},
            timeout=60,
        )
        manifest.raise_for_status()
        source = next(item for item in manifest.json().get("sources", []) if item.get("id") == args.source_id)
        connector = (source.get("options") or {}).get("connector_config") or {}
        ensure(
            connector.get("type") == args.proxy_type
            and connector.get("chatgpt") is args.chatgpt
            and int(connector.get("count", 0)) == args.count,
            "updated ZenProxy connector is not visible in the machine manifest",
        )
    except Exception:
        save_state(session, base_url, original)
        raise

    print(json.dumps({"source_id": args.source_id, "updated": True, "backup_created": True}))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
