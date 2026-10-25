"""控制记录与 bucket 同址的特殊销毁终态；绝不先解锁再删资源。"""
from __future__ import annotations

import hashlib
import json
import os
from pathlib import Path

from coordination import STATE_KEY, _code, _status
from refresh_context import require


def once(store, operation: str, call):
    sent = False

    def guard(**_kwargs):
        nonlocal sent
        require(not sent)
        sent = True

    event, handler = "before-send.s3." + operation, "easyproxy-sba-preview-delete-once"
    with store._write_mutex:
        store.client.meta.events.register_first(event, guard, unique_id=handler)
        try:
            result = call()
            require(result.get("ResponseMetadata", {}).get("HTTPStatusCode") in (200, 204))
            return result
        finally:
            store.client.meta.events.unregister(event, unique_id=handler)


def objects(store):
    keys, cursor = {}, None
    for _ in range(100):
        response = store.client.list_objects_v2(Bucket=store.bucket, MaxKeys=1000,
                                               **({"ContinuationToken": cursor} if cursor else {}))
        rows = response.get("Contents", [])
        require(type(rows) is list and len(rows) <= 1000 and type(response.get("IsTruncated")) is bool)
        for row in rows:
            key = row.get("Key")
            require(type(key) is str and 0 < len(key.encode("utf-8")) <= 1024 and key not in keys
                    and type(row.get("ETag")) is str)
            keys[key] = row["ETag"]
        if not response["IsTruncated"]:
            return keys
        next_cursor = response.get("NextContinuationToken")
        require(type(next_cursor) is str and next_cursor and next_cursor != cursor)
        cursor = next_cursor
    require(False)


def preflight_bucket(owner, permit, output: Path):
    """在删除任何云资源前验证桶可完整枚举及本地终结证据可持久化。"""
    state = owner.assert_owned(permit)
    require(permit.action == "destroy-preview" and state["identity"]["purpose"] == "preview"
            and output.is_absolute() and output.resolve() == output and not output.exists())
    listed = objects(owner.store)
    require(STATE_KEY in listed and listed[STATE_KEY] == permit.etag)
    output.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    probe = output.with_name(output.name + ".preflight")
    with probe.open("x", encoding="utf-8", newline="\n") as stream:
        json.dump({"schemaVersion": 1, "operationId": permit.operation_id,
                   "status": "preflight-only"}, stream, separators=(",", ":"))
        stream.flush()
        os.fsync(stream.fileno())
    if os.name == "posix":
        directory = os.open(output.parent, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0))
        try:
            os.fsync(directory)
        finally:
            os.close(directory)


def finish_bucket(owner, permit, output: Path, *, source_unchanged, removed: list[dict]):
    """其余批准资源已删除且读回后才调用；本函数不使用普通 commit_verified。"""
    state = owner.assert_owned(permit)
    require(permit.action == "destroy-preview" and state["identity"]["purpose"] == "preview"
            and output.is_absolute() and output.resolve() == output and not output.exists())
    owner.claim_stage(permit, "preview-terminal-deletion")
    store = owner.store
    listed = objects(store)
    require(STATE_KEY in listed and listed[STATE_KEY] == permit.etag)
    for key in sorted(listed):
        if key == STATE_KEY:
            continue
        owner.assert_owned(permit)
        once(store, "DeleteObject", lambda key=key: store.client.delete_object(Bucket=store.bucket, Key=key))
    require(objects(store) == {STATE_KEY: permit.etag})
    source = source_unchanged()  # Must include fresh source data/identity/release readback.
    require(type(source) is dict and source.get("unchanged") is True)
    owner.assert_owned(permit)
    output.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    evidence = {"schemaVersion": 1, "operationId": permit.operation_id,
        "permitSha256": hashlib.sha256(permit.state_bytes).hexdigest(), "identity": owner.identity,
        "inventoryDigest": owner.inventory_digest, "confirmedExceptBucket": [row for row in removed if row["key"] != "resource-artifacts"],
        "bucketDeletion": "pending", "source": source}
    with output.open("x", encoding="utf-8", newline="\n") as stream:
        json.dump(evidence, stream, sort_keys=True, separators=(",", ":"))
        stream.flush()
        os.fsync(stream.fileno())
    directory = os.open(output.parent, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0)) if os.name == "posix" else None
    if directory is not None:
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    # From this point no writer release/retired write is possible or attempted.
    # UNKNOWN deletion is never replayed, including when a later read is absent.
    once(store, "DeleteObject", lambda: store.client.delete_object(Bucket=store.bucket, Key=STATE_KEY))
    require(objects(store) == {})
    once(store, "DeleteBucket", lambda: store.client.delete_bucket(Bucket=store.bucket))
    try:
        store.client.head_bucket(Bucket=store.bucket)
    except Exception as error:
        require(_status(error) == 404 and _code(error) in ("404", "NoSuchBucket"))
        return
    require(False)
