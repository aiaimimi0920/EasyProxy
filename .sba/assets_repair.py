"""只恢复准确的 1.0.3 Pages 资产 UNKNOWN；不清锁、不重放已有配置。"""
import hashlib
import uuid

from coordination import Coordinator, Permit, ETAG, STATE_KEY, _encode
from pages_repair import (PagesResume, expected_objects as previous_objects, parent_completed,
                          verify_initialized, verify_configured, verify_existing_worker, verify_existing_pages)
from pages_repair import CONTROL_SHA256 as PREVIOUS_CONTROL_SHA256, OPERATION as PREVIOUS_OPERATION
from pages_api import PagesAPI
from repair_context import ASSETS_CONTEXT, ASSETS_PARENT_RELEASE, PAGES_CONTEXT
from request import ApplicationRequest


CONTROL_SHA256 = "aa8c2bf3295c755bba5f2a2018a6c9eff8c93dc1600ac8fdc7750617f6ff80b3"
OPERATION = "0321f9e60e7044b1a73ae1134ab332d4"


class AssetsRepairError(RuntimeError):
    pass


def require(value):
    if not value:
        raise AssetsRepairError("EASYPROXY_SBA_ASSETS_REPAIR_REJECTED")


def checked_state(owner: Coordinator):
    raw = owner.store.read()
    require(raw is not None and hashlib.sha256(raw[0]).hexdigest() == CONTROL_SHA256)
    current = owner._read()
    require(current is not None)
    value, etag = current
    require(_encode(value) == raw[0] and etag == raw[1] and value["active"] is None
            and value["writer"] == {"action": "deploy", "candidate": ASSETS_PARENT_RELEASE, "operationId": OPERATION})
    return value, etag


def expected_objects():
    result = previous_objects()
    for stage in ("ech-publication", "pages-publication"):
        result[f"_sba/operations/{OPERATION}/{stage}.json"] = _encode(
            {"operationId": OPERATION, "permitSha256": CONTROL_SHA256, "stage": stage})
    result[f"_sba/operations/{OPERATION}/pages-recovery-transition.json"] = _encode({
        "schemaVersion": 1, "context": PAGES_CONTEXT, "taskId": ASSETS_CONTEXT["parentTaskId"],
        "previousControlSha256": PREVIOUS_CONTROL_SHA256, "previousOperationId": PREVIOUS_OPERATION,
        "candidate": ASSETS_PARENT_RELEASE})
    return result


def checked_objects(owner: Coordinator):
    expected = expected_objects()
    listing = owner.store.client.list_objects_v2(Bucket=owner.store.bucket, MaxKeys=100)
    rows = listing.get("Contents")
    require(listing.get("IsTruncated") is False and type(rows) is list and len(rows) == 10
            and {row.get("Key") for row in rows} == {STATE_KEY, *expected})
    for key, body in expected.items():
        response = owner.store.client.get_object(Bucket=owner.store.bucket, Key=key)
        stream = response["Body"]
        try:
            require(stream.read(16385) == body)
        finally:
            stream.close()


def begin_recovery(owner: Coordinator, request: ApplicationRequest, root, credentials):
    require(type(request) is ApplicationRequest and request.action == "repair"
            and request.context == ASSETS_CONTEXT and request.previous == ASSETS_PARENT_RELEASE)
    config = request.config
    require(owner.identity == config.identity("production") and owner.inventory_digest == config.inventory_digest)
    parent_completed(credentials["EASYPROXY_GITHUB_TOKEN"], context=ASSETS_CONTEXT)
    value, etag = checked_state(owner)
    checked_objects(owner)
    token = credentials["EASYPROXY_CLOUDFLARE_API_TOKEN"]
    verify_initialized(config, root, token)
    verify_configured(owner, config, root, credentials["EASYPROXY_GITHUB_TOKEN"], parent_release=ASSETS_PARENT_RELEASE)
    verify_existing_worker(config, owner, token, credentials["EASYPROXY_ECH_TOKEN"], parent_release=ASSETS_PARENT_RELEASE)
    api = PagesAPI(config.account_id, config.pages_name, token)
    try:
        verify_existing_pages(api, owner, config, {
            "ADMIN_PASSWORD": credentials["EASYPROXY_MISUB_ADMIN_PASSWORD"],
            "COOKIE_SECRET": credentials["EASYPROXY_MISUB_COOKIE_SECRET"],
            "MANIFEST_TOKEN": credentials["EASYPROXY_MANIFEST_TOKEN"]}, parent_release=ASSETS_PARENT_RELEASE)
    finally:
        api.close()
    require(checked_state(owner) == (value, etag))
    checked_objects(owner)
    operation = uuid.uuid4().hex
    evidence = {"schemaVersion": 1, "context": request.context, "taskId": request.task_id,
                "previousControlSha256": CONTROL_SHA256, "previousOperationId": OPERATION, "candidate": request.candidate}
    marker = owner.store.create_stage(operation, "assets-recovery-transition", _encode(evidence))
    require(type(marker) is str and ETAG.fullmatch(marker) is not None)
    require(checked_state(owner) == (value, etag))
    value["revision"] = uuid.uuid4().hex
    value["writer"] = {"operationId": operation, "action": "deploy", "candidate": request.candidate}
    raw, new_etag = owner._write(value, etag)  # 单次 CAS；响应丢失不重放，writer 从不置空。
    permit = Permit(operation, "deploy", new_etag, raw)
    owner.assert_owned(permit)
    return permit, PagesResume(operation, hashlib.sha256(raw).hexdigest(), ASSETS_PARENT_RELEASE["sourceSha"])
