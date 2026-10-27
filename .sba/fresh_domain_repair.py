"""准确 Edge UNKNOWN 的独立移交：先完整只读证据，再一次非空 writer CAS。"""
import hashlib
import uuid

from coordination import Coordinator, Permit, ETAG, STATE_KEY, _encode
from domains import DomainAPI, DomainTarget
from fresh_domain_parent import verify_parent_receipt
from fresh_domain_resources import verify_configured, verify_existing_worker, verify_worker_domain
from fresh_domain_resume import FreshDomainResume, require
from fresh_pristine_config import configuration_matches
from github_repair import verify_initialized
from pages_api import PagesAPI
from repair_context import FRESH_DOMAIN_CONTEXT as CONTEXT, FRESH_DOMAIN_PARENT_RELEASE as PARENT
from request import ApplicationRequest

CONTROL_SHA256 = "656bfa4bcbcb798d82d5fcddb20c568c71a37118d5327a1b93a8b66f86e047da"
CONTROL_ETAG = '"a838d8376400ba3204c6ba42bf58ec2b"'
OPERATION = "43950cf00c644d92976ea303cc4e2701"
STAGES = {
    "database-initialization": ('"e46a90675ec094a1a5572ae3d45f732b"', "d17117b7e662dfb3a793b9bada7315f5bd5f691bb464c657f9df4b739d245990"),
    "github-environment": ('"79e2fa81d9478cc709bb62ec7a856575"', "9f90777310e2ab11627783d05c0360f5e9301057993735b928ddf9e0c500bb24"),
    "ech-publication": ('"86be3fe111753f32970e94dfd34a28c0"', "9315d788e7dda2e703bb100f0c9312e256ee57eb5bda8637c05ea93cfac135a9"),
    "worker-custom-domain": ('"2b4a6f197b37d2e7733b4bd5ec95e307"', "cdd12cfd682d13c76568f8d806ce2a7737ee8b5b0b11aaac2058286affb4e9f1"),
}


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
    return {f"_sba/operations/{OPERATION}/{stage}.json": (
        _encode({"operationId": OPERATION, "stage": stage, "permitSha256": CONTROL_SHA256}), etag, digest)
        for stage, (etag, digest) in STAGES.items()}


def checked_objects(owner, *, transition=None):
    expected = {STATE_KEY: (None, CONTROL_ETAG, CONTROL_SHA256), **expected_objects()}
    if transition is not None:
        operation, body, etag = transition
        expected[f"_sba/operations/{operation}/fresh-domain-recovery-transition.json"] = (
            body, etag, hashlib.sha256(body).hexdigest())
    listing = owner.store.client.list_objects_v2(Bucket=owner.store.bucket, MaxKeys=100)
    rows = listing.get("Contents")
    require(listing.get("IsTruncated") is False and type(rows) is list and len(rows) == len(expected)
            and all(type(row) is dict for row in rows) and {row.get("Key") for row in rows} == set(expected))
    for row in rows:
        body, etag, digest = expected[row["Key"]]
        require(row.get("ETag") == etag and type(row.get("Size")) is int and 0 < row["Size"] <= 16384)
        response = owner.store.client.get_object(Bucket=owner.store.bucket, Key=row["Key"])
        stream = response["Body"]
        try:
            raw = stream.read(16385)
        finally:
            stream.close()
        require(response.get("ETag") == etag and len(raw) == row["Size"]
                and hashlib.sha256(raw).hexdigest() == digest and (body is None or raw == body))


def verify_domains(owner, config, token):
    verify_worker_domain(owner, config.inventory, token)
    api = DomainAPI(config.account_id, token)
    try:
        api.inspect(DomainTarget("pages", config.pages_name, config.pages_origin), existing=False)
    finally:
        api.close()


def assert_partial(owner, request, root, credentials):
    require(type(request) is ApplicationRequest and request.action == "repair" and request.context == CONTEXT
            and type(request.context.get("parentRunId")) is int and request.previous == PARENT
            and request.application_version == "1.0.13" and configuration_matches(request.config))
    config = request.config
    require(owner.identity == config.identity("production") and owner.inventory_digest == config.inventory_digest)
    verify_parent_receipt(credentials["EASYPROXY_GITHUB_TOKEN"])
    current = checked_state(owner)
    checked_objects(owner)
    token = credentials["EASYPROXY_CLOUDFLARE_API_TOKEN"]
    verify_initialized(config, root, token)
    verify_configured(owner, config, root, credentials["EASYPROXY_GITHUB_TOKEN"])
    verify_existing_worker(owner, config, token, credentials["EASYPROXY_ECH_TOKEN"])
    verify_domains(owner, config, token)
    api = PagesAPI(config.account_id, config.pages_name, token)
    try:
        require(api.project() is None)
    finally:
        api.close()
    require(checked_state(owner) == current)
    checked_objects(owner)
    return current


def begin_recovery(owner: Coordinator, request: ApplicationRequest, root, credentials):
    value, etag = assert_partial(owner, request, root, credentials)
    operation = uuid.uuid4().hex
    evidence = {"schemaVersion": 1, "context": request.context, "taskId": request.task_id,
        "previousControlSha256": CONTROL_SHA256, "previousControlEtag": CONTROL_ETAG,
        "previousOperationId": OPERATION, "candidate": request.candidate}
    body = _encode(evidence)
    marker = owner.store.create_stage(operation, "fresh-domain-recovery-transition", body)
    require(type(marker) is str and ETAG.fullmatch(marker) is not None)
    require(checked_state(owner) == (value, etag))
    checked_objects(owner, transition=(operation, body, marker))
    value["revision"] = uuid.uuid4().hex
    value["writer"] = {"operationId": operation, "action": "deploy", "candidate": request.candidate}
    raw, new_etag = owner._write(value, etag)
    permit = Permit(operation, "deploy", new_etag, raw)
    owner.assert_owned(permit)
    return permit, FreshDomainResume(operation, hashlib.sha256(raw).hexdigest(), request.config.workers_subdomain)
