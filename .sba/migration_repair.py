"""准确 1.0.14 备份前失败的单次移交；不清锁、不重放旧升级。"""
import hashlib
import uuid

from botocore.exceptions import ClientError
from aggregator_publish import PublicArtifacts, _previous
from artifact_store import ArtifactStore
from cloudflare_http import decode_object, secure_session
from coordination import Coordinator, Permit, ETAG, _encode
from d1_init import D1API, SCHEMA_QUERY, HISTORY_QUERY, COUNT_QUERY, _schema, reviewed_plan
from repair_context import MIGRATION_CONTEXT, MIGRATION_PARENT_RELEASE, MIGRATION_ACTIVE_RELEASE
from request import ApplicationRequest
from worker_api import public_release

OPERATION = "daba9f8f380441cbb5ae64f8bbe6b792"
CONTROL_SHA256 = "bba58a72873618628986cf7882e8685dec732cbfd8f76563499b70316be0c68c"
CONTROL_ETAG = '"275a6df250a23e04fef9000dbb315822"'
INVENTORY = "79962304889e05df2e52433195c6cd8659d9481c3a6d0a175a69170ed754aabb"
STABLE_SHA256 = "ef462075998df918f4a53b52b68022805ccac220807a12cccb167b0c8d05da0c"
STABLE_ETAG = '"13a5065f2f208e10e3bee94ee52e9d45"'
TRANSITION = "update-backup-recovery-transition"


class MigrationRepairError(RuntimeError):
    pass


def require(value, *_):
    if not value:
        raise MigrationRepairError("EASYPROXY_SBA_MIGRATION_REPAIR_REJECTED")


def checked_state(owner):
    raw, etag = owner.store.read()
    require(hashlib.sha256(raw).hexdigest() == CONTROL_SHA256 and etag == CONTROL_ETAG)
    value, observed = owner._read()
    require(_encode(value) == raw and observed == etag and value["active"] == MIGRATION_ACTIVE_RELEASE
            and value["writer"] == {"action": "update", "candidate": MIGRATION_PARENT_RELEASE, "operationId": OPERATION})
    return value, etag


def checked_objects(owner, *, transition=None):
    client, bucket = owner.store.client, owner.store.bucket
    try:
        client.head_object(Bucket=bucket, Key=f"_sba/backups/{OPERATION}/misub.age")
    except ClientError as error:
        require(error.response.get("ResponseMetadata", {}).get("HTTPStatusCode") == 404)
    else:
        require(False)
    expected = set() if transition is None else {f"_sba/operations/{OPERATION}/{TRANSITION}.json"}
    for prefix, keys in ((f"_sba/operations/{OPERATION}/", expected), (f"releases/{OPERATION}/", set())):
        listing = client.list_objects_v2(Bucket=bucket, Prefix=prefix, MaxKeys=1000)
        rows = listing.get("Contents", [])
        require(listing.get("IsTruncated") is False and listing.get("KeyCount") == len(rows)
                and len(rows) == len(keys) and {row.get("Key") for row in rows} == keys)
    if transition is not None:
        response = client.get_object(Bucket=bucket, Key=next(iter(expected)))
        try:
            require(response["Body"].read(16385) == transition)
        finally:
            response["Body"].close()


def verify_parent(token):
    session = secure_session()
    try:
        def read(url):
            with session.get(url, headers={"Authorization": "Bearer " + token}, timeout=(10, 40), stream=True, allow_redirects=False) as response:
                require(response.status_code == 200)
                raw = bytearray()
                for chunk in response.iter_content(65536):
                    raw.extend(chunk)
                    require(len(raw) <= 1048576)
                return decode_object(bytes(raw), require)
        base = "https://api.github.com/repos/aiaimimi0920/SpringBok/actions/runs/42360503031"
        run = read(base)
        require(run.get("id") == MIGRATION_CONTEXT["parentRunId"] and type(run.get("run_attempt")) is int
                and run["run_attempt"] == 1 and run.get("status") == "completed" and run.get("conclusion") == "success"
                and run.get("head_sha") == "c492f332f7b35a35ef34324d79c850c3627b68de"
                and run.get("event") == "workflow_dispatch" and run.get("path") == ".github/workflows/sba-execute-linux.yml"
                and run.get("display_title") == "sba:" + MIGRATION_CONTEXT["parentTaskId"] + ":" + MIGRATION_CONTEXT["requestDigest"])
        jobs = read(base + "/attempts/1/jobs?per_page=100")
        require(jobs.get("total_count") == 1 and type(jobs.get("jobs")) is list and len(jobs["jobs"]) == 1)
        job = jobs["jobs"][0]
        require(job.get("id") == 128367678871 and job.get("status") == "completed" and job.get("conclusion") == "success"
                and job.get("labels") == ["ubuntu-24.04"] and job.get("runner_group_name") == "GitHub Actions"
                and type(job.get("completed_at")) is str)
        for status in ("queued", "in_progress", "waiting", "pending", "requested"):
            runs = read("https://api.github.com/repos/aiaimimi0920/EasyProxy/actions/workflows/sba-aggregator-refresh.yml/runs?per_page=100&status=" + status)
            require(runs.get("total_count") == 0 and runs.get("workflow_runs") == [])
    finally:
        session.close()


def verify_d1(config, root, token):
    api = D1API(config.account_id, token, config.database_id)
    try:
        row = api.identity()
        require(row.get("uuid") == config.database_id and row.get("name") == config.database_name)
        require(_schema(api.query(SCHEMA_QUERY)) == reviewed_plan(root)[1]
                and api.query(HISTORY_QUERY) == [{"migration_id": 1, "name": "0001_initial"}, {"migration_id": 2, "name": "0002_cron_executions"}]
                and api.query(COUNT_QUERY) == [{"subscriptions": 44, "profiles": 1, "settings": 0, "cron_executions": 0}])
    finally:
        api.close()


def verify_published(owner, config):
    expected = {"schemaVersion": 1, "applicationId": "easyproxy-cloud", **MIGRATION_ACTIVE_RELEASE}
    require(public_release(config.worker_origin) == public_release(config.pages_origin) == expected)
    public = PublicArtifacts(config.worker_origin + "/aggregator", inventory=config.inventory)
    try:
        prior, _, artifacts = _previous(ArtifactStore(owner.store), public, MIGRATION_ACTIVE_RELEASE)
        require(prior is not None and hashlib.sha256(prior.body).hexdigest() == STABLE_SHA256
                and prior.etag == STABLE_ETAG and len(artifacts) == 7)
    finally:
        public.close()


def inspect_recovery(owner: Coordinator, request: ApplicationRequest, root, credentials):
    require(type(request) is ApplicationRequest and request.action == "repair" and request.context == MIGRATION_CONTEXT
            and request.previous == MIGRATION_PARENT_RELEASE and request.application_version == "1.0.15"
            and owner.identity == request.config.identity("production") and owner.inventory_digest == INVENTORY
            and owner.inventory_digest == request.config.inventory_digest)
    value, etag = checked_state(owner)
    checked_objects(owner)
    verify_parent(credentials["EASYPROXY_GITHUB_TOKEN"])
    verify_d1(request.config, root, credentials["EASYPROXY_CLOUDFLARE_API_TOKEN"])
    verify_published(owner, request.config)
    require(checked_state(owner) == (value, etag))
    checked_objects(owner)
    return value, etag


def begin_recovery(owner, request, root, credentials):
    value, etag = inspect_recovery(owner, request, root, credentials)
    operation = uuid.uuid4().hex
    evidence = _encode({"schemaVersion": 1, "context": request.context, "taskId": request.task_id,
        "previousControlSha256": CONTROL_SHA256, "previousOperationId": OPERATION,
        "active": MIGRATION_ACTIVE_RELEASE, "candidate": request.candidate, "operationId": operation})
    # 原 operation 下的固定 create-only marker 是一次性消费记录；响应丢失也不另开移交。
    marker = owner.store.create_stage(OPERATION, TRANSITION, evidence)
    require(type(marker) is str and ETAG.fullmatch(marker) is not None)
    checked_objects(owner, transition=evidence)
    require(checked_state(owner) == (value, etag))
    value["revision"] = uuid.uuid4().hex
    value["writer"] = {"operationId": operation, "action": "update", "candidate": request.candidate}
    raw, new_etag = owner._write(value, etag)
    permit = Permit(operation, "update", new_etag, raw)
    owner.assert_owned(permit)
    return permit, None
