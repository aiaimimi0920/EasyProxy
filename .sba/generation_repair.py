"""仅恢复准确 1.0.4 generation UNKNOWN；现场版本仍是原 1.0.3。"""
import hashlib
import uuid

from assets_repair import CONTROL_SHA256, OPERATION, checked_state, checked_objects
from coordination import Coordinator, Permit, ETAG, _encode
from pages_api import PagesAPI
from pages_repair import (PagesResume, parent_completed, verify_initialized, verify_configured,
                          verify_existing_worker, verify_existing_pages)
from repair_context import ASSETS_CONTEXT, ASSETS_PARENT_RELEASE, GENERATION_CONTEXT, GENERATION_PARENT_RELEASE
from request import ApplicationRequest


class GenerationRepairError(RuntimeError):
    pass


def require(value):
    if not value:
        raise GenerationRepairError("EASYPROXY_SBA_GENERATION_REPAIR_REJECTED")


def begin_recovery(owner: Coordinator, request: ApplicationRequest, root, credentials):
    require(type(request) is ApplicationRequest and request.action == "repair"
            and request.context == GENERATION_CONTEXT and request.previous == GENERATION_PARENT_RELEASE)
    config = request.config
    require(owner.identity == config.identity("production") and owner.inventory_digest == config.inventory_digest)
    # 最新失败任务与原 Pages 父任务均必须已按准确 executor/request 完成；不消费旧批准。
    for context in (GENERATION_CONTEXT, ASSETS_CONTEXT):
        parent_completed(credentials["EASYPROXY_GITHUB_TOKEN"], context=context)
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
    evidence = {"schemaVersion": 1, "context": request.context, "ancestry": ASSETS_CONTEXT,
                "taskId": request.task_id, "previousControlSha256": CONTROL_SHA256,
                "previousOperationId": OPERATION, "resourceRelease": ASSETS_PARENT_RELEASE, "candidate": request.candidate}
    marker = owner.store.create_stage(operation, "generation-recovery-transition", _encode(evidence))
    require(type(marker) is str and ETAG.fullmatch(marker) is not None)
    require(checked_state(owner) == (value, etag))
    value["revision"] = uuid.uuid4().hex
    value["writer"] = {"operationId": operation, "action": "deploy", "candidate": request.candidate}
    raw, new_etag = owner._write(value, etag)  # 仅一次 CAS；不清锁、不重放、不移除历史证据。
    permit = Permit(operation, "deploy", new_etag, raw)
    owner.assert_owned(permit)
    return permit, PagesResume(operation, hashlib.sha256(raw).hexdigest(), ASSETS_PARENT_RELEASE["sourceSha"])
