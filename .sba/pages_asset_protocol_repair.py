"""仅准确1.0.9资产验证失败的一次恢复；16原对象、非空writer和单次CAS。"""
import hashlib
import uuid

from coordination import Coordinator, Permit, ETAG, STATE_KEY, _encode
from generation_outputs_parent import verify_parent_receipt as verify_generation_parent_receipt
from github_repair import parent_completed as github_parent_completed
from pages_api import PagesAPI
from pages_asset_protocol_parent import verify_parent_receipt
import pages_diagnostic_repair as prior
from pages_repair import (PagesResume, parent_completed, verify_initialized, verify_configured,
                          verify_existing_worker, verify_existing_pages)
from repair_context import (PAGES_ASSET_PROTOCOL_CONTEXT as CONTEXT, PAGES_ASSET_PROTOCOL_PARENT_RELEASE as PARENT,
    GENERATION_OUTPUTS_CONTEXT, PAGES_DIAGNOSTIC_CONTEXT, PAGES_DIAGNOSTIC_PARENT_RELEASE,
    GENERATION_CONTEXT, ASSETS_CONTEXT, PAGES_CONTEXT, GITHUB_CONTEXT)
from request import ApplicationRequest

CONTROL_SHA256 = "fe86d7108f4fd4db5bb54c20545ac7bde6f2eac04cdedd3079d129e25f5e1941"
CONTROL_ETAG = '"0db6892ce9355e7e94a0cf81f15f6d4b"'
OPERATION = "c51c2072b9dd4405a61afa4449e3b0b7"
ANCESTRY = [GENERATION_OUTPUTS_CONTEXT, PAGES_DIAGNOSTIC_CONTEXT, GENERATION_CONTEXT,
            ASSETS_CONTEXT, PAGES_CONTEXT, GITHUB_CONTEXT]


class PagesAssetProtocolRepairError(RuntimeError):
    pass


def require(value):
    if not value:
        raise PagesAssetProtocolRepairError("EASYPROXY_SBA_PAGES_ASSET_PROTOCOL_REPAIR_REJECTED")


def checked_state(owner):
    raw = owner.store.read()
    require(raw is not None and hashlib.sha256(raw[0]).hexdigest() == CONTROL_SHA256 and raw[1] == CONTROL_ETAG)
    current = owner._read()
    require(current is not None)
    value, etag = current
    require(_encode(value) == raw[0] and etag == raw[1] and value["active"] is None
            and value["writer"] == {"action": "deploy", "candidate": PARENT, "operationId": OPERATION})
    return value, etag


def expected_objects():
    result = prior.expected_objects()
    result[f"_sba/operations/{OPERATION}/generation-outputs-recovery-transition.json"] = _encode({
        "schemaVersion": 1, "context": GENERATION_OUTPUTS_CONTEXT, "ancestry": ANCESTRY[1:],
        "taskId": CONTEXT["parentTaskId"], "previousControlSha256": prior.CONTROL_SHA256,
        "previousControlEtag": prior.CONTROL_ETAG, "previousOperationId": prior.OPERATION,
        "resourceRelease": PAGES_DIAGNOSTIC_PARENT_RELEASE, "candidate": PARENT})
    for stage in ("ech-publication", "pages-publication"):
        result[f"_sba/operations/{OPERATION}/{stage}.json"] = _encode({
            "operationId": OPERATION, "permitSha256": CONTROL_SHA256, "stage": stage})
    return result


def checked_objects(owner):
    expected = expected_objects()
    listing = owner.store.client.list_objects_v2(Bucket=owner.store.bucket, MaxKeys=100)
    rows = listing.get("Contents")
    require(listing.get("IsTruncated") is False and type(rows) is list and len(rows) == len(expected) + 1
            and {row.get("Key") for row in rows} == {STATE_KEY, *expected})
    for key, body in expected.items():
        response = owner.store.client.get_object(Bucket=owner.store.bucket, Key=key)
        with response["Body"] as stream:
            require(stream.read(len(body) + 1) == body)


def begin_recovery(owner: Coordinator, request: ApplicationRequest, root, credentials):
    require(type(request) is ApplicationRequest and request.action == "repair"
            and request.context == CONTEXT and type(request.context["parentRunId"]) is int and request.previous == PARENT)
    config = request.config
    require(owner.identity == config.identity("production") and owner.inventory_digest == config.inventory_digest)
    verify_parent_receipt(credentials["EASYPROXY_GITHUB_TOKEN"])
    verify_generation_parent_receipt(credentials["EASYPROXY_GITHUB_TOKEN"])
    for context in ANCESTRY[1:-1]:
        parent_completed(credentials["EASYPROXY_GITHUB_TOKEN"], context=context)
    github_parent_completed(credentials["EASYPROXY_GITHUB_TOKEN"])
    value, etag = checked_state(owner)
    checked_objects(owner)
    token = credentials["EASYPROXY_CLOUDFLARE_API_TOKEN"]
    verify_initialized(config, root, token)
    verify_configured(owner, config, root, credentials["EASYPROXY_GITHUB_TOKEN"], parent_release=PARENT)
    verify_existing_worker(config, owner, token, credentials["EASYPROXY_ECH_TOKEN"], parent_release=PARENT)
    api = PagesAPI(config.account_id, config.pages_name, token)
    try:
        verify_existing_pages(api, owner, config, {
            "ADMIN_PASSWORD": credentials["EASYPROXY_MISUB_ADMIN_PASSWORD"],
            "COOKIE_SECRET": credentials["EASYPROXY_MISUB_COOKIE_SECRET"],
            "MANIFEST_TOKEN": credentials["EASYPROXY_MANIFEST_TOKEN"]}, parent_release=PARENT)
    finally:
        api.close()
    require(checked_state(owner) == (value, etag))
    checked_objects(owner)
    operation = uuid.uuid4().hex
    evidence = {"schemaVersion": 1, "context": request.context, "ancestry": ANCESTRY, "taskId": request.task_id,
                "previousControlSha256": CONTROL_SHA256, "previousControlEtag": CONTROL_ETAG,
                "previousOperationId": OPERATION, "resourceRelease": PARENT, "candidate": request.candidate}
    marker = owner.store.create_stage(operation, "pages-asset-protocol-recovery-transition", _encode(evidence))
    require(type(marker) is str and ETAG.fullmatch(marker) is not None)
    require(checked_state(owner) == (value, etag))
    value["revision"] = uuid.uuid4().hex
    value["writer"] = {"operationId": operation, "action": "deploy", "candidate": request.candidate}
    raw, new_etag = owner._write(value, etag)
    permit = Permit(operation, "deploy", new_etag, raw)
    owner.assert_owned(permit)
    return permit, PagesResume(operation, hashlib.sha256(raw).hexdigest(), PARENT["sourceSha"])
