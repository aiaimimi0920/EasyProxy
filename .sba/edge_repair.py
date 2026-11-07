"""准确 1.0.16 发布前失败的一次移交；不重放旧操作、不清控制键、不降低门禁。"""
import hashlib
import uuid

from cloudflare_http import decode_object, secure_session
from coordination import Coordinator, Permit, ETAG, _encode
from migration_repair import verify_d1
from repair_context import EDGE_CONTEXT, EDGE_PARENT_RELEASE, MIGRATION_ACTIVE_RELEASE
from promotion_repair import verify_published
from publication_baseline import PublicationBaseline
from request import ApplicationRequest

OPERATION = "f14880e4dd234124adf038b20c4b3264"
CONTROL_SHA256 = "82a98589af3dc3731178dda3661161d95712236149a61121658b6044eeac3a48"
CONTROL_ETAG = '"904f1468c9f3a4650bbf93ad6d9e4866"'
INVENTORY = "79962304889e05df2e52433195c6cd8659d9481c3a6d0a175a69170ed754aabb"
BACKUP_SHA256 = "4e0fcd04cf835f334188d940c5cd230b1620a2b6596a5599277fba4e47302381"
STAGES = {"github-environment"}
TRANSITION = "update-edge-baseline-recovery-transition"


class EdgeRepairError(RuntimeError):
    pass


def require(value, *_):
    if not value:
        raise EdgeRepairError("EASYPROXY_SBA_EDGE_REPAIR_REJECTED")


def checked_state(owner):
    raw, etag = owner.store.read()
    require(hashlib.sha256(raw).hexdigest() == CONTROL_SHA256 and etag == CONTROL_ETAG)
    value, observed = owner._read()
    require(_encode(value) == raw and observed == etag and value["active"] == MIGRATION_ACTIVE_RELEASE
            and value["writer"] == {"action": "update", "candidate": EDGE_PARENT_RELEASE, "operationId": OPERATION})
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
    backup = read(f"_sba/backups/{OPERATION}/misub.age", 11875)
    require(len(backup) == 11875 and backup.startswith(b"age-encryption.org/v1\n")
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
        base = "https://api.github.com/repos/aiaimimi0920/SpringBok/actions/runs/42376441530"
        run = read(base)
        require(run.get("id") == EDGE_CONTEXT["parentRunId"] and type(run.get("run_attempt")) is int
                and run["run_attempt"] == 1 and run.get("status") == "completed" and run.get("conclusion") == "success"
                and run.get("head_sha") == "de26192f0be5c1f4df23e82e3ac3104ab59bcf30"
                and run.get("event") == "workflow_dispatch" and run.get("path") == ".github/workflows/sba-execute-linux.yml"
                and run.get("display_title") == "sba:" + EDGE_CONTEXT["parentTaskId"] + ":" + EDGE_CONTEXT["requestDigest"])
        jobs = read(base + "/attempts/1/jobs?per_page=100")
        require(jobs.get("total_count") == 1 and type(jobs.get("jobs")) is list and len(jobs["jobs"]) == 1)
        job = jobs["jobs"][0]
        require(job.get("id") == 128414886232 and job.get("status") == "completed" and job.get("conclusion") == "success"
                and job.get("labels") == ["ubuntu-24.04"] and job.get("runner_group_name") == "GitHub Actions"
                and type(job.get("completed_at")) is str)
        for status in ("queued", "in_progress", "waiting", "pending", "requested"):
            runs = read("https://api.github.com/repos/aiaimimi0920/EasyProxy/actions/workflows/sba-aggregator-refresh.yml/runs?per_page=100&status=" + status)
            require(runs.get("total_count") == 0 and runs.get("workflow_runs") == [])
    finally:
        session.close()


def verify_provider_baseline(owner, config, credentials, *, release=None):
    from ech import metadata, _verify_settings
    from worker_api import WorkerAPI
    from pages import configuration, _verify_project, _verify_deployment
    from pages_api import PagesAPI
    from repair_context import PROMOTION_PARENT_RELEASE
    release = PROMOTION_PARENT_RELEASE if release is None else release
    worker = WorkerAPI(config.account_id, credentials['EASYPROXY_CLOUDFLARE_API_TOKEN'])
    pages = PagesAPI(config.account_id, config.pages_name, credentials['EASYPROXY_CLOUDFLARE_API_TOKEN'])
    try:
        _verify_settings(worker.settings(config.worker_name), metadata(owner, release,
                         credentials['EASYPROXY_ECH_TOKEN']))
        secrets = {'ADMIN_PASSWORD': credentials['EASYPROXY_MISUB_ADMIN_PASSWORD'],
                   'COOKIE_SECRET': credentials['EASYPROXY_MISUB_COOKIE_SECRET'],
                   'MANIFEST_TOKEN': credentials['EASYPROXY_MANIFEST_TOKEN']}
        expected = configuration(owner, release, config.pages_name, config.database_id, secrets,
            **({'origin': config.pages_origin} if config.custom_pages_origin is not None else {}))
        project = pages.project()
        project_id = _verify_project(project, config.pages_name, expected)
        canonical = project.get('canonical_deployment')
        _verify_deployment(canonical, config.pages_name, project_id, release)
        require(canonical.get('latest_stage', {}).get('name') == 'deploy'
                and canonical['latest_stage'].get('status') == 'success')
    finally:
        worker.close()
        pages.close()


def inspect_recovery(owner: Coordinator, request: ApplicationRequest, root, credentials):
    require(type(request) is ApplicationRequest and request.action == "repair" and request.context == EDGE_CONTEXT
            and request.previous == EDGE_PARENT_RELEASE and request.application_version == "1.0.17"
            and owner.identity == request.config.identity("production") and owner.inventory_digest == INVENTORY
            and owner.inventory_digest == request.config.inventory_digest)
    value, etag = checked_state(owner)
    checked_objects(owner)
    verify_parent(credentials["EASYPROXY_GITHUB_TOKEN"])
    verify_d1(request.config, root, credentials["EASYPROXY_CLOUDFLARE_API_TOKEN"])
    verify_published(owner, request.config)
    verify_provider_baseline(owner, request.config, credentials)
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
    return permit, PublicationBaseline(operation, hashlib.sha256(raw).hexdigest())
