"""准确 1.0.26 消费审计超时后停止的一次移交；不重放旧操作、不清控制键、不降低门禁。"""
from __future__ import annotations

import hashlib
import uuid

from aggregator_publish import PublicArtifacts, RAW, _previous, _validate
from artifact_store import ArtifactStore
from cloudflare_http import decode_object, secure_session
from coordination import Coordinator, ETAG, Permit, _encode, r2_store
from d1_init import COUNT_QUERY, D1API, HISTORY_QUERY, SCHEMA_QUERY, _schema, reviewed_plan
from edge_repair import verify_provider_baseline
from publication_baseline import PublicationBaseline
from repair_context import AUDIT_TIMEOUT_ACTIVE_RELEASE, AUDIT_TIMEOUT_CONTEXT, AUDIT_TIMEOUT_PARENT_RELEASE
from request import ApplicationRequest
from worker_api import public_release

OPERATION = "2e8987c30b9f42e2897cea3188ac5590"
CONTROL_SHA256 = "7e81aa46118d66133f2d626d7fbcc333cc2f66995ab7d3748e6af2dcae9c033b"
CONTROL_ETAG = '"2f1cd9191d58c502fb24f0e45b9b03f3"'
INVENTORY = "79962304889e05df2e52433195c6cd8659d9481c3a6d0a175a69170ed754aabb"
STABLE_SHA256 = "23f07cbc6497d4f3a8cd672a5b9681b44e20b132aecfa6f0173920f83b638fc3"
STABLE_ETAG = '"2f2f28eaf17a826c4b0d27fa5a834dbf"'
BACKUP_SIZE = 51192
BACKUP_SHA256 = "dc2d7f26cf52a497e19964a5c1c361eec6f9e633c684800a08e022b6673d1987"
STAGES = {"aggregator-audit", "aggregator-candidate", "ech-publication", "github-environment", "pages-publication"}
TRANSITION = "update-audit-timeout-recovery-transition"
EXECUTOR_HEAD_SHA = "d591242a9046e1583c29bce82b521acd3d83bea1"
JOB_ID = 137101599388


class AuditRepairError(RuntimeError):
    pass


def require(value, *_):
    if not value:
        raise AuditRepairError("EASYPROXY_SBA_AUDIT_REPAIR_REJECTED")


def checked_state(owner):
    raw, etag = owner.store.read()
    require(hashlib.sha256(raw).hexdigest() == CONTROL_SHA256 and etag == CONTROL_ETAG)
    value, observed = owner._read()
    require(_encode(value) == raw and observed == etag and value["active"] == AUDIT_TIMEOUT_ACTIVE_RELEASE
            and value["writer"] == {"action": "update", "candidate": AUDIT_TIMEOUT_PARENT_RELEASE, "operationId": OPERATION})
    return value, etag


def checked_objects(owner, *, transition=None):
    client, bucket = owner.store.client, owner.store.bucket

    def read(key, limit):
        response = client.get_object(Bucket=bucket, Key=key)
        try:
            raw = response["Body"].read(limit + 1)
            require(len(raw) <= limit)
            return raw
        finally:
            response["Body"].close()

    backup = read(f"_sba/backups/{OPERATION}/misub.age", BACKUP_SIZE)
    require(len(backup) == BACKUP_SIZE and backup.startswith(b"age-encryption.org/v1\n")
            and hashlib.sha256(backup).hexdigest() == BACKUP_SHA256)
    expected = {f"_sba/operations/{OPERATION}/{stage}.json" for stage in STAGES}
    if transition is not None:
        expected.add(f"_sba/operations/{OPERATION}/{TRANSITION}.json")
    for prefix, keys in ((f"_sba/operations/{OPERATION}/", expected), (f"releases/{OPERATION}/", set())):
        listing = client.list_objects_v2(Bucket=bucket, Prefix=prefix, MaxKeys=1000)
        rows = listing.get("Contents", [])
        require(listing.get("IsTruncated") is False and listing.get("KeyCount") == len(rows)
                and len(rows) == len(keys) and {row.get("Key") for row in rows} == keys)
    for stage in STAGES:
        value = decode_object(read(f"_sba/operations/{OPERATION}/{stage}.json", 16384), require)
        require(value == {"operationId": OPERATION, "stage": stage, "permitSha256": CONTROL_SHA256})
    if transition is not None:
        require(read(f"_sba/operations/{OPERATION}/{TRANSITION}.json", 16384) == transition)


def verify_parent(token):
    session = secure_session()
    try:
        def read(url):
            with session.get(url, headers={"Authorization": "Bearer " + token}, timeout=(10, 40),
                             stream=True, allow_redirects=False) as response:
                require(response.status_code == 200)
                raw = bytearray()
                for chunk in response.iter_content(65536):
                    raw.extend(chunk)
                    require(len(raw) <= 1048576)
                return decode_object(bytes(raw), require)

        base = f"https://api.github.com/repos/aiaimimi0920/SpringBok/actions/runs/{AUDIT_TIMEOUT_CONTEXT['parentRunId']}"
        run = read(base)
        require(run.get("id") == AUDIT_TIMEOUT_CONTEXT["parentRunId"] and type(run.get("run_attempt")) is int
                and run["run_attempt"] == 1 and run.get("status") == "completed" and run.get("conclusion") == "success"
                and run.get("head_sha") == EXECUTOR_HEAD_SHA
                and run.get("event") == "workflow_dispatch" and run.get("path") == ".github/workflows/sba-execute-linux.yml"
                and run.get("display_title") == "sba:" + AUDIT_TIMEOUT_CONTEXT["parentTaskId"] + ":" + AUDIT_TIMEOUT_CONTEXT["requestDigest"])
        jobs = read(base + "/attempts/1/jobs?per_page=100")
        require(jobs.get("total_count") == 1 and type(jobs.get("jobs")) is list and len(jobs["jobs"]) == 1)
        job = jobs["jobs"][0]
        require(job.get("id") == JOB_ID and job.get("status") == "completed" and job.get("conclusion") == "success"
                and job.get("labels") == ["ubuntu-24.04"] and job.get("runner_group_name") == "GitHub Actions"
                and type(job.get("completed_at")) is str)
        for status in ("queued", "in_progress", "waiting", "pending", "requested"):
            runs = read("https://api.github.com/repos/aiaimimi0920/EasyProxy/actions/workflows/sba-aggregator-refresh.yml/runs?per_page=100&status=" + status)
            require(runs.get("total_count") == 0 and runs.get("workflow_runs") == [])
    finally:
        session.close()


def verify_d1_schema(config, root, token):
    api = D1API(config.account_id, token, config.database_id)
    try:
        row = api.identity()
        require(row.get("uuid") == config.database_id and row.get("name") == config.database_name)
        counts = api.query(COUNT_QUERY)
        require(_schema(api.query(SCHEMA_QUERY)) == reviewed_plan(root)[1]
                and api.query(HISTORY_QUERY) == [{"migration_id": 1, "name": "0001_initial"}, {"migration_id": 2, "name": "0002_cron_executions"}]
                and len(counts) == 1 and type(counts[0].get("subscriptions")) is int and counts[0]["subscriptions"] > 0
                and type(counts[0].get("profiles")) is int and counts[0]["profiles"] >= 1)
    finally:
        api.close()


def verify_published(owner, config):
    expected = {"schemaVersion": 1, "applicationId": "easyproxy-cloud", **AUDIT_TIMEOUT_PARENT_RELEASE}
    require(public_release(config.worker_origin) == public_release(config.pages_origin) == expected)
    public = PublicArtifacts(config.worker_origin + "/aggregator", inventory=config.inventory)
    try:
        artifacts = ArtifactStore(owner.store)
        prior, _, verified = _previous(artifacts, public, AUDIT_TIMEOUT_ACTIVE_RELEASE)
        require(prior is not None and hashlib.sha256(prior.body).hexdigest() == STABLE_SHA256
                and prior.etag == STABLE_ETAG and len(verified) == 7)
        bodies = {}
        for key in RAW:
            obj = artifacts.get(f"candidate/{OPERATION}/{key}")
            require(obj is not None)
            bodies[key] = obj.body
            public.verify(f"candidate/{OPERATION}/{key}", obj.body)
        _validate(bodies, RAW)
    finally:
        public.close()


def inspect_recovery(owner: Coordinator, request: ApplicationRequest, root, credentials):
    require(type(request) is ApplicationRequest and request.action == "repair" and request.context == AUDIT_TIMEOUT_CONTEXT
            and request.previous == AUDIT_TIMEOUT_PARENT_RELEASE and request.application_version == "1.0.27"
            and owner.identity == request.config.identity("production") and owner.inventory_digest == INVENTORY
            and owner.inventory_digest == request.config.inventory_digest)
    value, etag = checked_state(owner)
    checked_objects(owner)
    verify_parent(credentials["EASYPROXY_GITHUB_TOKEN"])
    verify_d1_schema(request.config, root, credentials["EASYPROXY_CLOUDFLARE_API_TOKEN"])
    verify_published(owner, request.config)
    verify_provider_baseline(owner, request.config, credentials, release=AUDIT_TIMEOUT_PARENT_RELEASE)
    require(checked_state(owner) == (value, etag))
    checked_objects(owner)
    return value, etag


def assert_audit_timeout_baseline(request: ApplicationRequest, root, credentials):
    store = r2_store(request.config.account_id, request.config.bucket,
                     credentials["EASYPROXY_R2_ACCESS_KEY_ID"], credentials["EASYPROXY_R2_SECRET_ACCESS_KEY"])
    try:
        owner = Coordinator(store, request.config.identity("production"), request.config.inventory_digest)
        inspect_recovery(owner, request, root, credentials)
    finally:
        store.client.close()


def begin_recovery(owner, request, root, credentials):
    value, etag = inspect_recovery(owner, request, root, credentials)
    operation = uuid.uuid4().hex
    evidence = _encode({"schemaVersion": 1, "context": request.context, "taskId": request.task_id,
        "previousControlSha256": CONTROL_SHA256, "previousOperationId": OPERATION,
        "active": AUDIT_TIMEOUT_ACTIVE_RELEASE, "candidate": request.candidate, "operationId": operation})
    marker = owner.store.create_stage(OPERATION, TRANSITION, evidence)
    require(type(marker) is str and ETAG.fullmatch(marker) is not None)
    checked_objects(owner, transition=evidence)
    require(checked_state(owner) == (value, etag))
    value["revision"] = uuid.uuid4().hex
    value["writer"] = {"operationId": operation, "action": "update", "candidate": request.candidate}
    raw, new_etag = owner._write(value, etag)
    permit = Permit(operation, "update", new_etag, raw)
    owner.assert_owned(permit)
    return permit, PublicationBaseline(operation, hashlib.sha256(raw).hexdigest(), "audit26")
