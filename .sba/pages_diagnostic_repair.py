"""准确 1.0.5 Pages UNKNOWN 的一次移交；保留十三对象及所有旧恢复边界。"""
import hashlib
import uuid

import assets_repair
from coordination import Coordinator, Permit, ETAG, STATE_KEY, _encode
from github_repair import parent_completed as github_parent_completed
from pages_api import PagesAPI
from pages_repair import (PagesResume, parent_completed, verify_initialized, verify_configured,
                          verify_existing_worker, verify_existing_pages)
from repair_context import (PAGES_DIAGNOSTIC_CONTEXT, PAGES_DIAGNOSTIC_PARENT_RELEASE,
                            GENERATION_CONTEXT, ASSETS_CONTEXT, ASSETS_PARENT_RELEASE, PAGES_CONTEXT, GITHUB_CONTEXT)
from request import ApplicationRequest


CONTROL_SHA256 = "401d1fa07ed734786cb58830c25e7a265e91911db21b3a1af25d459e02d9e937"
CONTROL_ETAG = '"b29f31c31a51173da98b9d338a3e7002"'
OPERATION = "42b9214bc0f848cbbccb54bc8f46f0c5"


class PagesDiagnosticRepairError(RuntimeError):
    pass


def require(value):
    if not value:
        raise PagesDiagnosticRepairError("EASYPROXY_SBA_PAGES_DIAGNOSTIC_REPAIR_REJECTED")


def checked_state(owner: Coordinator):
    raw = owner.store.read()
    require(raw is not None and hashlib.sha256(raw[0]).hexdigest() == CONTROL_SHA256 and raw[1] == CONTROL_ETAG)
    current = owner._read()
    require(current is not None)
    value, etag = current
    require(_encode(value) == raw[0] and etag == raw[1] and value["active"] is None
            and value["writer"] == {"action": "deploy", "candidate": PAGES_DIAGNOSTIC_PARENT_RELEASE, "operationId": OPERATION})
    return value, etag


def expected_objects():
    result = assets_repair.expected_objects()
    result[f"_sba/operations/{OPERATION}/generation-recovery-transition.json"] = _encode({
        "schemaVersion": 1, "context": GENERATION_CONTEXT, "ancestry": ASSETS_CONTEXT,
        "taskId": PAGES_DIAGNOSTIC_CONTEXT["parentTaskId"], "previousControlSha256": assets_repair.CONTROL_SHA256,
        "previousOperationId": assets_repair.OPERATION, "resourceRelease": ASSETS_PARENT_RELEASE,
        "candidate": PAGES_DIAGNOSTIC_PARENT_RELEASE})
    for stage in ("ech-publication", "pages-publication"):
        result[f"_sba/operations/{OPERATION}/{stage}.json"] = _encode(
            {"operationId": OPERATION, "permitSha256": CONTROL_SHA256, "stage": stage})
    return result


def checked_objects(owner: Coordinator):
    expected = expected_objects()
    listing = owner.store.client.list_objects_v2(Bucket=owner.store.bucket, MaxKeys=100)
    rows = listing.get("Contents")
    require(listing.get("IsTruncated") is False and type(rows) is list and len(rows) == 13
            and len(expected) == 12 and all(type(row) is dict for row in rows)
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
            and request.context == PAGES_DIAGNOSTIC_CONTEXT and type(request.context["parentRunId"]) is int
            and request.previous == PAGES_DIAGNOSTIC_PARENT_RELEASE)
    config = request.config
    require(owner.identity == config.identity("production") and owner.inventory_digest == config.inventory_digest)
    for context in (PAGES_DIAGNOSTIC_CONTEXT, GENERATION_CONTEXT, ASSETS_CONTEXT, PAGES_CONTEXT):
        parent_completed(credentials["EASYPROXY_GITHUB_TOKEN"], context=context)
    github_parent_completed(credentials["EASYPROXY_GITHUB_TOKEN"])
    value, etag = checked_state(owner)
    checked_objects(owner)
    token = credentials["EASYPROXY_CLOUDFLARE_API_TOKEN"]
    verify_initialized(config, root, token)
    verify_configured(owner, config, root, credentials["EASYPROXY_GITHUB_TOKEN"], parent_release=PAGES_DIAGNOSTIC_PARENT_RELEASE)
    verify_existing_worker(config, owner, token, credentials["EASYPROXY_ECH_TOKEN"], parent_release=PAGES_DIAGNOSTIC_PARENT_RELEASE)
    api = PagesAPI(config.account_id, config.pages_name, token)
    try:
        verify_existing_pages(api, owner, config, {
            "ADMIN_PASSWORD": credentials["EASYPROXY_MISUB_ADMIN_PASSWORD"],
            "COOKIE_SECRET": credentials["EASYPROXY_MISUB_COOKIE_SECRET"],
            "MANIFEST_TOKEN": credentials["EASYPROXY_MANIFEST_TOKEN"]}, parent_release=PAGES_DIAGNOSTIC_PARENT_RELEASE)
    finally:
        api.close()
    require(checked_state(owner) == (value, etag))
    checked_objects(owner)
    operation = uuid.uuid4().hex
    evidence = {"schemaVersion": 1, "context": request.context,
                "ancestry": [GENERATION_CONTEXT, ASSETS_CONTEXT, PAGES_CONTEXT, GITHUB_CONTEXT],
                "taskId": request.task_id, "previousControlSha256": CONTROL_SHA256,
                "previousControlEtag": CONTROL_ETAG, "previousOperationId": OPERATION,
                "resourceRelease": PAGES_DIAGNOSTIC_PARENT_RELEASE, "candidate": request.candidate}
    marker = owner.store.create_stage(operation, "diagnostic-recovery-transition", _encode(evidence))
    require(type(marker) is str and ETAG.fullmatch(marker) is not None)
    require(checked_state(owner) == (value, etag))
    value["revision"] = uuid.uuid4().hex
    value["writer"] = {"operationId": operation, "action": "deploy", "candidate": request.candidate}
    raw, new_etag = owner._write(value, etag)  # 单次 CAS；writer 始终非空，UNKNOWN 绝不重放。
    permit = Permit(operation, "deploy", new_etag, raw)
    owner.assert_owned(permit)
    return permit, PagesResume(operation, hashlib.sha256(raw).hexdigest(), PAGES_DIAGNOSTIC_PARENT_RELEASE["sourceSha"])
