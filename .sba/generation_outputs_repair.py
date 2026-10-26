"""仅准确1.0.7生成失败的一次恢复；复用1.0.5十三对象资源前置。"""
import hashlib
import uuid

from coordination import Coordinator, Permit, ETAG, _encode
from generation_outputs_parent import verify_parent_receipt
from github_repair import parent_completed as github_parent_completed
from pages_api import PagesAPI
from pages_diagnostic_repair import CONTROL_SHA256, CONTROL_ETAG, OPERATION, checked_state, checked_objects
from pages_repair import (PagesResume, parent_completed, verify_initialized, verify_configured,
                          verify_existing_worker, verify_existing_pages)
from repair_context import (GENERATION_OUTPUTS_CONTEXT, GENERATION_OUTPUTS_PARENT_RELEASE,
    PAGES_DIAGNOSTIC_CONTEXT, PAGES_DIAGNOSTIC_PARENT_RELEASE, GENERATION_CONTEXT,
    ASSETS_CONTEXT, PAGES_CONTEXT, GITHUB_CONTEXT)
from request import ApplicationRequest


class GenerationOutputsRepairError(RuntimeError):
    pass


def require(value):
    if not value:
        raise GenerationOutputsRepairError("EASYPROXY_SBA_GENERATION_OUTPUTS_REPAIR_REJECTED")


def begin_recovery(owner: Coordinator, request: ApplicationRequest, root, credentials):
    require(type(request) is ApplicationRequest and request.action == "repair"
            and request.context == GENERATION_OUTPUTS_CONTEXT and type(request.context["parentRunId"]) is int
            and request.previous == GENERATION_OUTPUTS_PARENT_RELEASE)
    config = request.config
    require(owner.identity == config.identity("production") and owner.inventory_digest == config.inventory_digest)
    verify_parent_receipt(credentials["EASYPROXY_GITHUB_TOKEN"])
    ancestry = [PAGES_DIAGNOSTIC_CONTEXT, GENERATION_CONTEXT, ASSETS_CONTEXT, PAGES_CONTEXT, GITHUB_CONTEXT]
    for context in ancestry[:-1]:
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
    evidence = {"schemaVersion": 1, "context": request.context, "ancestry": ancestry,
                "taskId": request.task_id, "previousControlSha256": CONTROL_SHA256,
                "previousControlEtag": CONTROL_ETAG, "previousOperationId": OPERATION,
                "resourceRelease": PAGES_DIAGNOSTIC_PARENT_RELEASE, "candidate": request.candidate}
    marker = owner.store.create_stage(operation, "generation-outputs-recovery-transition", _encode(evidence))
    require(type(marker) is str and ETAG.fullmatch(marker) is not None)
    require(checked_state(owner) == (value, etag))
    value["revision"] = uuid.uuid4().hex
    value["writer"] = {"operationId": operation, "action": "deploy", "candidate": request.candidate}
    raw, new_etag = owner._write(value, etag)  # 单次CAS，非空writer移交；UNKNOWN绝不重放。
    permit = Permit(operation, "deploy", new_etag, raw)
    owner.assert_owned(permit)
    return permit, PagesResume(operation, hashlib.sha256(raw).hexdigest(), PAGES_DIAGNOSTIC_PARENT_RELEASE["sourceSha"])
