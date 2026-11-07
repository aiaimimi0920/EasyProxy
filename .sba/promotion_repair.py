"""准确 1.0.15 晋级前失败的一次移交；不重放旧操作、不清控制键、不降低门禁。"""
import hashlib
import uuid

from aggregator_publish import PublicArtifacts, RAW, _previous, _validate
from artifact_store import ArtifactStore
from cloudflare_http import decode_object, secure_session
from coordination import Coordinator, Permit, ETAG, _encode
from migration_repair import verify_d1
from repair_context import PROMOTION_CONTEXT, PROMOTION_PARENT_RELEASE, MIGRATION_ACTIVE_RELEASE
from request import ApplicationRequest
from worker_api import public_release

OPERATION = "276a780cf1d649bf9bb5a7e6a0d4d983"
CONTROL_SHA256 = "c040256342e7ac384eef4f779abc81f0217ec4719493143ee40e510e63d95a25"
CONTROL_ETAG = '"bc38d2d127f38e019792a45124caad42"'
INVENTORY = "79962304889e05df2e52433195c6cd8659d9481c3a6d0a175a69170ed754aabb"
STABLE_SHA256 = "ef462075998df918f4a53b52b68022805ccac220807a12cccb167b0c8d05da0c"
STABLE_ETAG = '"13a5065f2f208e10e3bee94ee52e9d45"'
BACKUP_SHA256 = "9a5efb5a25e3ab6afa59ee44bb62e18d02673d2f0127053ac6a3188a3e237163"
STAGES = {"aggregator-audit", "aggregator-candidate", "ech-publication", "github-environment", "pages-publication"}
TRANSITION = "update-promotion-recovery-transition"


class PromotionRepairError(RuntimeError):
    pass


def require(value, *_):
    if not value:
        raise PromotionRepairError("EASYPROXY_SBA_PROMOTION_REPAIR_REJECTED")


def checked_state(owner):
    raw, etag = owner.store.read()
    require(hashlib.sha256(raw).hexdigest() == CONTROL_SHA256 and etag == CONTROL_ETAG)
    value, observed = owner._read()
    require(_encode(value) == raw and observed == etag and value["active"] == MIGRATION_ACTIVE_RELEASE
            and value["writer"] == {"action": "update", "candidate": PROMOTION_PARENT_RELEASE, "operationId": OPERATION})
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
    backup = read(f"_sba/backups/{OPERATION}/misub.age", 11873)
    require(len(backup) == 11873 and backup.startswith(b"age-encryption.org/v1\n")
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
            with session.get(url, headers={"Authorization": "Bearer " + token}, timeout=(10, 40), stream=True, allow_redirects=False) as response:
                require(response.status_code == 200)
                raw = bytearray()
                for chunk in response.iter_content(65536):
                    raw.extend(chunk)
                    require(len(raw) <= 1048576)
                return decode_object(bytes(raw), require)
        base = "https://api.github.com/repos/aiaimimi0920/SpringBok/actions/runs/42365774495"
        run = read(base)
        require(run.get("id") == PROMOTION_CONTEXT["parentRunId"] and type(run.get("run_attempt")) is int
                and run["run_attempt"] == 1 and run.get("status") == "completed" and run.get("conclusion") == "success"
                and run.get("head_sha") == "de26192f0be5c1f4df23e82e3ac3104ab59bcf30"
                and run.get("event") == "workflow_dispatch" and run.get("path") == ".github/workflows/sba-execute-linux.yml"
                and run.get("display_title") == "sba:" + PROMOTION_CONTEXT["parentTaskId"] + ":" + PROMOTION_CONTEXT["requestDigest"])
        jobs = read(base + "/attempts/1/jobs?per_page=100")
        require(jobs.get("total_count") == 1 and type(jobs.get("jobs")) is list and len(jobs["jobs"]) == 1)
        job = jobs["jobs"][0]
        require(job.get("id") == 128383393845 and job.get("status") == "completed" and job.get("conclusion") == "success"
                and job.get("labels") == ["ubuntu-24.04"] and job.get("runner_group_name") == "GitHub Actions"
                and type(job.get("completed_at")) is str)
        for status in ("queued", "in_progress", "waiting", "pending", "requested"):
            runs = read("https://api.github.com/repos/aiaimimi0920/EasyProxy/actions/workflows/sba-aggregator-refresh.yml/runs?per_page=100&status=" + status)
            require(runs.get("total_count") == 0 and runs.get("workflow_runs") == [])
    finally:
        session.close()


def verify_published(owner, config):
    expected = {"schemaVersion": 1, "applicationId": "easyproxy-cloud", **PROMOTION_PARENT_RELEASE}
    require(public_release(config.worker_origin) == public_release(config.pages_origin) == expected)
    public = PublicArtifacts(config.worker_origin + "/aggregator", inventory=config.inventory)
    try:
        artifacts = ArtifactStore(owner.store)
        prior, _, verified = _previous(artifacts, public, MIGRATION_ACTIVE_RELEASE)
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
    require(type(request) is ApplicationRequest and request.action == "repair" and request.context == PROMOTION_CONTEXT
            and request.previous == PROMOTION_PARENT_RELEASE and request.application_version == "1.0.16"
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
