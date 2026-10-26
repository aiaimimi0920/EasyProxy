"""准确 Pages UNKNOWN 的一次恢复；保留证据，不重放 D1 或 GitHub 配置。"""
from dataclasses import dataclass
from email.parser import BytesParser
from email.policy import default
import hashlib
import uuid

from cloudflare_http import decode_object, secure_session
from coordination import Coordinator, Permit, ETAG, STATE_KEY, _encode
from github_api import GitHubAPI
from github_repair import verify_initialized, EXECUTOR_SHA, ENVIRONMENT_ID
from repair_context import GITHUB_CONTEXT, PAGES_CONTEXT, PAGES_PARENT_RELEASE, ASSETS_CONTEXT, ASSETS_PARENT_RELEASE
from repair_context import GENERATION_CONTEXT, PAGES_DIAGNOSTIC_CONTEXT, PAGES_DIAGNOSTIC_PARENT_RELEASE
from request import ApplicationRequest
from worker_api import WorkerAPI, public_release


CONTROL_SHA256 = "be5546037f6e32f17a170f5f2c36a1c4c659bcc9263a41a1d0a4a11cc55556a1"
PREVIOUS_CONTROL_SHA256 = "989d6cd2512cd32a5196404969637edce48b4b8a1320077e73408a10bdaa590f"
OPERATION = "3f0d31adb4594b23bfe92e190f3d5fb3"
PREVIOUS_OPERATION = "ed766b2b6f9c426c9681a86012d0d0bd"
PROJECT_ID = "b4a57a72-f537-4938-a9d9-d92acf04284f"
WORKER_BUNDLE_SHA256 = "0216cff123ad16fc7790f7338cc4fe9479af64d7810029b47fe31dd03c5f5704"


class PagesRepairError(RuntimeError):
    pass


def require(value, _code=None):
    if not value:
        raise PagesRepairError("EASYPROXY_SBA_PAGES_REPAIR_REJECTED")


@dataclass(frozen=True)
class PagesResume:
    operation_id: str
    permit_sha256: str
    parent_source_sha: str = PAGES_PARENT_RELEASE["sourceSha"]

    @property
    def parent_release(self):
        for release in (PAGES_PARENT_RELEASE, ASSETS_PARENT_RELEASE, PAGES_DIAGNOSTIC_PARENT_RELEASE):
            if self.parent_source_sha == release["sourceSha"]:
                return dict(release)
        require(False)

    def assert_permit(self, owner: Coordinator, permit: Permit):
        self.parent_release  # Only the reviewed, exact parent versions are allowed.
        owner.assert_owned(permit)
        require(permit.action == "deploy" and owner.identity["purpose"] == "production"
                and permit.operation_id == self.operation_id
                and hashlib.sha256(permit.state_bytes).hexdigest() == self.permit_sha256)


def parent_completed(token: str, *, context=None):
    context = PAGES_CONTEXT if context is None else context
    require(context in (PAGES_CONTEXT, ASSETS_CONTEXT, GENERATION_CONTEXT, PAGES_DIAGNOSTIC_CONTEXT))
    session = secure_session()
    try:
        url = "https://api.github.com/repos/aiaimimi0920/SpringBok/actions/runs/" + str(context["parentRunId"])
        with session.get(url, headers={"Authorization": "Bearer " + token, "Accept": "application/vnd.github+json",
                         "X-GitHub-Api-Version": "2022-11-28"}, timeout=(10, 60), stream=True, allow_redirects=False) as response:
            require(response.status_code == 200)
            raw = bytearray()
            for chunk in response.iter_content(65536):
                raw.extend(chunk)
                require(len(raw) <= 1024 * 1024)
            row = decode_object(bytes(raw), require)
        require(row.get("id") == context["parentRunId"] and type(row.get("run_attempt")) is int
                and row["run_attempt"] == 1 and row.get("status") == "completed" and row.get("conclusion") == "success"
                and row.get("head_sha") == EXECUTOR_SHA and row.get("event") == "workflow_dispatch"
                and row.get("path") == ".github/workflows/sba-execute-linux.yml"
                and row.get("display_title") == "sba:" + context["parentTaskId"] + ":" + context["requestDigest"])
    finally:
        session.close()


def checked_state(owner: Coordinator):
    raw = owner.store.read()
    require(raw is not None and hashlib.sha256(raw[0]).hexdigest() == CONTROL_SHA256)
    current = owner._read()
    require(current is not None)
    value, etag = current
    require(_encode(value) == raw[0] and etag == raw[1] and value["active"] is None
            and value["writer"] == {"action": "deploy", "candidate": PAGES_PARENT_RELEASE, "operationId": OPERATION})
    return value, etag


def expected_objects():
    result = {}
    for operation, digest, stages in (
        (PREVIOUS_OPERATION, PREVIOUS_CONTROL_SHA256, ("database-initialization", "github-environment")),
        (OPERATION, CONTROL_SHA256, ("github-environment", "ech-publication", "pages-publication")),
    ):
        for stage in stages:
            result[f"_sba/operations/{operation}/{stage}.json"] = _encode(
                {"operationId": operation, "permitSha256": digest, "stage": stage})
    result[f"_sba/operations/{OPERATION}/github-recovery-transition.json"] = _encode({
        "schemaVersion": 1, "context": GITHUB_CONTEXT, "taskId": PAGES_CONTEXT["parentTaskId"],
        "previousControlSha256": PREVIOUS_CONTROL_SHA256, "previousOperationId": PREVIOUS_OPERATION,
        "candidate": PAGES_PARENT_RELEASE})
    return result


def checked_objects(owner: Coordinator):
    expected = expected_objects()
    listing = owner.store.client.list_objects_v2(Bucket=owner.store.bucket, MaxKeys=100)
    rows = listing.get("Contents")
    require(listing.get("IsTruncated") is False and type(rows) is list and len(rows) == len(expected) + 1
            and {row.get("Key") for row in rows} == {STATE_KEY, *expected})
    for key, body in expected.items():
        response = owner.store.client.get_object(Bucket=owner.store.bucket, Key=key)
        stream = response["Body"]
        try:
            require(stream.read(16385) == body)
        finally:
            stream.close()


def verify_configured(owner, config, root, token, *, parent_release=None):
    parent_release = PAGES_PARENT_RELEASE if parent_release is None else parent_release
    require(parent_release in (PAGES_PARENT_RELEASE, ASSETS_PARENT_RELEASE, PAGES_DIAGNOSTIC_PARENT_RELEASE))
    from github_environment import variables, verify_policy, reviewed_workflow, REFRESH_SECRETS
    from refresh_registry import REGISTRY_VARIABLE, parse_registry
    api = GitHubAPI(config.repository, config.environment, token)
    try:
        repository = api.repository_info()
        require(repository["defaultBranch"] == "main")
        require(api.workflow_at("main") == api.workflow_at(parent_release["sourceSha"]) == reviewed_workflow(root))
        api.active_workflow()
        environment = api.environment_info()
        require(environment is not None and environment["id"] == ENVIRONMENT_ID)
        verify_policy(environment, api.policies(), "main")
        require(api.variables() == variables(owner, config, repository["id"], ENVIRONMENT_ID)
                and api.secret_names() == REFRESH_SECRETS)
        require(parse_registry(api.repository_variable(REGISTRY_VARIABLE), config.repository)["environments"] == [config.environment])
        return {"checks": dict.fromkeys(("github-environment-configured", "github-environment-verified",
                                        "github-workflow-verified"), True), "refreshEnabled": True}
    finally:
        api.close()


def worker_bundle_digest(api: WorkerAPI, name: str):
    # GET only; the provider returns the single public module as multipart.
    path = f"https://api.cloudflare.com/client/v4/accounts/{api.account_id}/workers/scripts/{api._name(name)}"
    with api._session.get(path, headers={"Authorization": "Bearer " + api._token},
                          timeout=(10, 60), stream=True, allow_redirects=False) as response:
        require(response.status_code == 200)
        raw = bytearray()
        for chunk in response.iter_content(65536):
            raw.extend(chunk)
            require(len(raw) <= 3 * 1024 * 1024)
        raw = bytes(raw)
    require(raw.startswith(b"--") and b"\r\n" in raw)
    boundary = raw.split(b"\r\n", 1)[0][2:]
    require(0 < len(boundary) <= 128 and all(c in b"0123456789abcdef" for c in boundary))
    message = BytesParser(policy=default).parsebytes(b"Content-Type: multipart/form-data; boundary=" + boundary + b"\r\n\r\n" + raw)
    require(message.is_multipart() and not message.defects)
    parts = list(message.iter_parts())
    require(len(parts) == 1 and not parts[0].defects
            and parts[0].get_param("name", header="content-disposition") == "edge.mjs")
    return hashlib.sha256(parts[0].get_payload(decode=True)).hexdigest()


def verify_existing_worker(config, owner, token, ech_token, *, parent_release=None):
    parent_release = PAGES_PARENT_RELEASE if parent_release is None else parent_release
    require(parent_release in (PAGES_PARENT_RELEASE, ASSETS_PARENT_RELEASE, PAGES_DIAGNOSTIC_PARENT_RELEASE))
    from ech import metadata, _verify_settings
    api = WorkerAPI(config.account_id, token)
    try:
        require(api.account_subdomain() == config.workers_subdomain)
        _verify_settings(api.settings(config.worker_name), metadata(owner, parent_release, ech_token))
        api.script_subdomain(config.worker_name)
        require(worker_bundle_digest(api, config.worker_name) == WORKER_BUNDLE_SHA256)
        require(public_release(config.worker_origin) == {"schemaVersion": 1, "applicationId": "easyproxy-cloud", **parent_release})
    finally:
        api.close()


def verify_existing_pages(api, owner, config, secrets, *, parent_release=None):
    parent_release = PAGES_PARENT_RELEASE if parent_release is None else parent_release
    require(parent_release in (PAGES_PARENT_RELEASE, ASSETS_PARENT_RELEASE, PAGES_DIAGNOSTIC_PARENT_RELEASE))
    from pages import configuration, _verify_project
    existing = api.project()
    _verify_project(existing, config.pages_name, configuration(owner, parent_release,
                    config.pages_name, config.database_id, secrets), PROJECT_ID)
    require(existing.get("latest_deployment") is None and existing.get("canonical_deployment") is None
            and api.deployments() == [])


def begin_recovery(owner: Coordinator, request: ApplicationRequest, root, credentials):
    from pages_api import PagesAPI
    require(type(request) is ApplicationRequest and request.action == "repair"
            and request.context == PAGES_CONTEXT and request.previous == PAGES_PARENT_RELEASE)
    config = request.config
    require(owner.identity == config.identity("production") and owner.inventory_digest == config.inventory_digest)
    parent_completed(credentials["EASYPROXY_GITHUB_TOKEN"])
    value, etag = checked_state(owner)
    checked_objects(owner)
    token = credentials["EASYPROXY_CLOUDFLARE_API_TOKEN"]
    verify_initialized(config, root, token)
    verify_configured(owner, config, root, credentials["EASYPROXY_GITHUB_TOKEN"])
    verify_existing_worker(config, owner, token, credentials["EASYPROXY_ECH_TOKEN"])
    api = PagesAPI(config.account_id, config.pages_name, token)
    try:
        verify_existing_pages(api, owner, config, {
            "ADMIN_PASSWORD": credentials["EASYPROXY_MISUB_ADMIN_PASSWORD"],
            "COOKIE_SECRET": credentials["EASYPROXY_MISUB_COOKIE_SECRET"],
            "MANIFEST_TOKEN": credentials["EASYPROXY_MANIFEST_TOKEN"]})
    finally:
        api.close()
    require(checked_state(owner) == (value, etag))
    checked_objects(owner)
    operation = uuid.uuid4().hex
    evidence = {"schemaVersion": 1, "context": request.context, "taskId": request.task_id,
                "previousControlSha256": CONTROL_SHA256, "previousOperationId": OPERATION, "candidate": request.candidate}
    marker = owner.store.create_stage(operation, "pages-recovery-transition", _encode(evidence))
    require(type(marker) is str and ETAG.fullmatch(marker) is not None)
    require(checked_state(owner) == (value, etag))
    value["revision"] = uuid.uuid4().hex
    value["writer"] = {"operationId": operation, "action": "deploy", "candidate": request.candidate}
    raw, new_etag = owner._write(value, etag)
    permit = Permit(operation, "deploy", new_etag, raw)
    owner.assert_owned(permit)
    return permit, PagesResume(operation, hashlib.sha256(raw).hexdigest())
