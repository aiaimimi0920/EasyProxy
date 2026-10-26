"""准确 UNKNOWN 的一次定向恢复：保留证据、条件移交 writer，只补缺失 GitHub 项。"""
from dataclasses import dataclass
import hashlib
from pathlib import Path
import uuid

from cloudflare_http import decode_object, secure_session
from coordination import Coordinator, Permit, ETAG, STATE_KEY, _encode
from d1_init import D1API, SCHEMA_QUERY, HISTORY_QUERY, COUNT_QUERY, reviewed_plan, _schema
from github_api import GitHubAPI
from pages_api import PagesAPI
from repair_context import GITHUB_CONTEXT, GITHUB_PARENT_RELEASE
from request import ApplicationRequest
from worker_api import WorkerAPI


CONTROL_SHA256 = "989d6cd2512cd32a5196404969637edce48b4b8a1320077e73408a10bdaa590f"
ENVIRONMENT_ID = 25319178759
EXECUTOR_SHA = "4d6a8bcd8c52f0ada791729bafc2788a2cffb972"


class GitHubRepairError(RuntimeError):
    pass


def require(value, code="REJECTED"):
    if not value:
        raise GitHubRepairError("EASYPROXY_SBA_GITHUB_REPAIR_" + code)


@dataclass(frozen=True)
class GitHubResume:
    operation_id: str
    permit_sha256: str
    environment_id: int

    def assert_permit(self, owner: Coordinator, permit: Permit):
        owner.assert_owned(permit)
        require(permit.action == "deploy" and self.environment_id == ENVIRONMENT_ID
                and permit.operation_id == self.operation_id
                and hashlib.sha256(permit.state_bytes).hexdigest() == self.permit_sha256)


def verify_initialized(config, root: Path, token: str) -> dict:
    database = D1API(config.account_id, token, config.database_id)
    try:
        identity = database.identity()
        require(identity.get("uuid") == config.database_id and identity.get("name") == config.database_name)
        require(_schema(database.query(SCHEMA_QUERY)) == reviewed_plan(root)[1])
        require(database.query(HISTORY_QUERY) == [{"migration_id": 1, "name": "0001_initial"},
                                                {"migration_id": 2, "name": "0002_cron_executions"}])
        require(database.query(COUNT_QUERY) == [{"subscriptions": 0, "profiles": 0, "settings": 0, "cron_executions": 0}])
        return {"checks": {"d1-initialized": True, "schema-verified": True}}
    finally:
        database.close()


def parent_completed(token: str):
    session = secure_session()
    try:
        url = "https://api.github.com/repos/aiaimimi0920/SpringBok/actions/runs/" + str(GITHUB_CONTEXT["parentRunId"])
        with session.get(url, headers={"Authorization": "Bearer " + token, "Accept": "application/vnd.github+json",
                         "X-GitHub-Api-Version": "2022-11-28"}, timeout=(10, 60), stream=True, allow_redirects=False) as response:
            require(response.status_code == 200)
            body = bytearray()
            for chunk in response.iter_content(65536):
                body.extend(chunk)
                require(len(body) <= 1024 * 1024)
            row = decode_object(bytes(body), require)
        require(row.get("id") == GITHUB_CONTEXT["parentRunId"] and type(row.get("run_attempt")) is int and row["run_attempt"] == 1
                and row.get("status") == "completed" and row.get("conclusion") == "success"
                and row.get("head_sha") == EXECUTOR_SHA and row.get("event") == "workflow_dispatch"
                and row.get("path") == ".github/workflows/sba-execute-linux.yml"
                and row.get("display_title") == "sba:" + GITHUB_CONTEXT["parentTaskId"] + ":" + GITHUB_CONTEXT["requestDigest"])
    finally:
        session.close()


def checked_state(owner: Coordinator):
    raw = owner.store.read()
    require(raw is not None and hashlib.sha256(raw[0]).hexdigest() == CONTROL_SHA256)
    current = owner._read()
    require(current is not None)
    value, etag = current
    require(_encode(value) == raw[0] and etag == raw[1] and value["active"] is None
            and value["writer"]["action"] == "deploy" and value["writer"]["candidate"] == GITHUB_PARENT_RELEASE)
    return value, etag


def checked_objects(owner: Coordinator, value: dict):
    operation = value["writer"]["operationId"]
    stages = ("database-initialization", "github-environment")
    prefix = "_sba/operations/" + operation + "/"
    expected = {prefix + stage + ".json": _encode({"operationId": operation, "stage": stage,
                "permitSha256": CONTROL_SHA256}) for stage in stages}
    listing = owner.store.client.list_objects_v2(Bucket=owner.store.bucket, MaxKeys=100)
    rows = listing.get("Contents")
    require(listing.get("IsTruncated") is False and type(rows) is list and len(rows) == 3
            and {row.get("Key") for row in rows} == {STATE_KEY, *expected})
    for key, body in expected.items():
        response = owner.store.client.get_object(Bucket=owner.store.bucket, Key=key)
        stream = response["Body"]
        try:
            require(stream.read(16385) == body)
        finally:
            stream.close()


def checked_github(config, root: Path, token: str):
    from github_environment import variables, verify_policy, reviewed_workflow, CONFIG_VARIABLE
    from refresh_registry import REGISTRY_VARIABLE
    api = GitHubAPI(config.repository, config.environment, token)
    try:
        repository = api.repository_info()
        require(repository["defaultBranch"] == "main")
        require(api.workflow_at(repository["defaultBranch"]) == reviewed_workflow(root))
        api.active_workflow()
        environment = api.environment_info()
        require(environment is not None and environment["id"] == ENVIRONMENT_ID)
        verify_policy(environment, api.policies(), repository["defaultBranch"])
        # 配置变量不包含批准 SHA，必须与新版本的同实例配置逐字节一致。
        class Identity:
            identity = config.identity("production")
            inventory_digest = config.inventory_digest
        expected = variables(Identity(), config, repository["id"], ENVIRONMENT_ID)
        require(api.variables() == {CONFIG_VARIABLE: expected[CONFIG_VARIABLE]} and api.secret_names() == set()
                and api.repository_variable(REGISTRY_VARIABLE) is None)
    finally:
        api.close()


def begin_recovery(owner: Coordinator, request: ApplicationRequest, root: Path, credentials: dict):
    require(type(request) is ApplicationRequest and request.action == "repair"
            and request.context == GITHUB_CONTEXT and request.previous == GITHUB_PARENT_RELEASE)
    config = request.config
    require(owner.identity == config.identity("production") and owner.inventory_digest == config.inventory_digest)
    token = credentials["EASYPROXY_GITHUB_TOKEN"]
    parent_completed(token)
    value, etag = checked_state(owner)
    checked_objects(owner, value)
    cf_token = credentials["EASYPROXY_CLOUDFLARE_API_TOKEN"]
    verify_initialized(config, root, cf_token)
    worker = WorkerAPI(config.account_id, cf_token)
    try:
        require(worker.settings_or_absent(config.worker_name) is None and worker.account_subdomain() == config.workers_subdomain)
    finally:
        worker.close()
    pages = PagesAPI(config.account_id, config.pages_name, cf_token)
    try:
        require(pages.project() is None)
    finally:
        pages.close()
    checked_github(config, root, token)
    require(checked_state(owner) == (value, etag))
    checked_objects(owner, value)
    operation = uuid.uuid4().hex
    evidence = {"schemaVersion": 1, "context": request.context, "taskId": request.task_id,
                "previousControlSha256": CONTROL_SHA256, "previousOperationId": value["writer"]["operationId"],
                "candidate": request.candidate}
    # 先 create-only 保存移交证据；任何响应不确定都停止，不重复 marker 或 CAS。
    marker = owner.store.create_stage(operation, "github-recovery-transition", _encode(evidence))
    require(type(marker) is str and ETAG.fullmatch(marker) is not None)
    require(checked_state(owner) == (value, etag))
    value["revision"] = uuid.uuid4().hex
    value["writer"] = {"operationId": operation, "action": "deploy", "candidate": request.candidate}
    raw, new_etag = owner._write(value, etag)  # 始终保持 writer 非空，旧 Permit 因 ETag 改变立即失效。
    permit = Permit(operation, "deploy", new_etag, raw)
    owner.assert_owned(permit)
    return permit, GitHubResume(operation, hashlib.sha256(raw).hexdigest(), ENVIRONMENT_ID)
