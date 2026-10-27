"""准确新 deploy UNKNOWN 的有界只读重核；不修改旧回执或转发下载凭据。"""
import hashlib
import io
import json
import zipfile

import requests

from cloudflare_http import decode_object, secure_session
from generation_outputs_parent import _signed_location
from repair_context import FRESH_PRISTINE_CONTEXT as CONTEXT, FRESH_PRISTINE_PARENT_RELEASE as PARENT

EXECUTOR_SHA = "4d6a8bcd8c52f0ada791729bafc2788a2cffb972"
ARTIFACT_ID = 12717732426
ARTIFACT_SHA256 = "b346daf5e5c69a86932e635d7bf314dfffa27ab5fc447aee6b4b051c60cba838"
BASE = "https://api.github.com/repos/aiaimimi0920/SpringBok/actions/"


class FreshPristineParentError(RuntimeError):
    pass


def require(value, _code=None):
    if not value:
        raise FreshPristineParentError("EASYPROXY_SBA_FRESH_PRISTINE_PARENT_REJECTED") from None


def _read(response, limit):
    require(response.status_code == 200)
    raw = bytearray()
    for chunk in response.iter_content(16384):
        raw.extend(chunk)
        require(len(raw) <= limit)
    return bytes(raw)


def parent_completed(token, *, context):
    require(context == CONTEXT and type(context["parentRunId"]) is int)
    session = secure_session()
    try:
        with session.get(BASE + "runs/" + str(CONTEXT["parentRunId"]),
                headers={"Authorization": "Bearer " + token, "Accept": "application/vnd.github+json",
                         "X-GitHub-Api-Version": "2022-11-28"},
                timeout=(10, 60), stream=True, allow_redirects=False) as response:
            row = decode_object(_read(response, 1024 * 1024), require)
        require(type(row.get("id")) is int and row["id"] == CONTEXT["parentRunId"]
                and type(row.get("run_attempt")) is int and row["run_attempt"] == 1
                and row.get("status") == "completed" and row.get("conclusion") == "success"
                and row.get("head_sha") == EXECUTOR_SHA and row.get("event") == "workflow_dispatch"
                and row.get("path") == ".github/workflows/sba-execute-linux.yml"
                and row.get("display_title") == "sba:" + CONTEXT["parentTaskId"] + ":" + CONTEXT["requestDigest"])
    finally:
        session.close()


def verify_parent_receipt(token):
    try:
        _verify_parent_receipt(token)
    except (requests.RequestException, OSError, RuntimeError):
        require(False)


def _verify_parent_receipt(token):
    parent_completed(token, context=CONTEXT)
    session = secure_session()
    headers = {"Authorization": "Bearer " + token, "Accept": "application/vnd.github+json",
               "X-GitHub-Api-Version": "2022-11-28"}
    options = {"timeout": (10, 60), "allow_redirects": False, "stream": True}
    try:
        with session.get(BASE + "runs/" + str(CONTEXT["parentRunId"]) + "/artifacts?per_page=100",
                         headers=headers, **options) as response:
            page = decode_object(_read(response, 1024 * 1024), require)
        require(type(page.get("total_count")) is int and page["total_count"] == 1
                and type(page.get("artifacts")) is list and len(page["artifacts"]) == 1)
        artifact, = page["artifacts"]
        require(type(artifact) is dict and type(artifact.get("id")) is int and artifact["id"] == ARTIFACT_ID
                and artifact.get("expired") is False and artifact.get("digest") == "sha256:" + ARTIFACT_SHA256
                and artifact.get("name") == "sba-result-" + CONTEXT["parentTaskId"] + "-" + CONTEXT["requestDigest"])
        with session.get(BASE + "artifacts/" + str(ARTIFACT_ID) + "/zip", headers=headers, **options) as response:
            require(response.status_code == 302)
            location = _signed_location(response.headers.get("Location"))
    finally:
        session.close()
    download = secure_session()
    try:
        with download.get(location, **options) as response:
            raw = _read(response, 65536)
    finally:
        download.close()
    require(hashlib.sha256(raw).hexdigest() == ARTIFACT_SHA256)
    try:
        with zipfile.ZipFile(io.BytesIO(raw)) as archive:
            files = archive.infolist()
            require(len(files) == 1 and 0 < files[0].file_size <= 16384 and not files[0].is_dir())
            envelope = decode_object(archive.read(files[0]), require)
    except (ValueError, OSError, zipfile.BadZipFile, RuntimeError, NotImplementedError):
        require(False)
    require(set(envelope) == {"schemaVersion", "permitId", "requestDigest", "result", "runId", "runAttempt", "executorSha"}
            and type(envelope["schemaVersion"]) is int and envelope["schemaVersion"] == 1
            and type(envelope["runId"]) is int and envelope["runId"] == CONTEXT["parentRunId"]
            and type(envelope["runAttempt"]) is int and envelope["runAttempt"] == 1
            and envelope["executorSha"] == EXECUTOR_SHA and envelope["requestDigest"] == CONTEXT["requestDigest"]
            and type(envelope["permitId"]) is str and bool(envelope["permitId"]))
    result = envelope["result"]
    expected = {"schemaVersion": 3, "taskId": CONTEXT["parentTaskId"], "action": "deploy", **PARENT,
                "status": "unknown", "checks": [], "errorCode": CONTEXT["errorCode"]}
    require(type(result) is dict and type(result.get("schemaVersion")) is int and result == expected
            and hashlib.sha256(json.dumps(result, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode()).hexdigest()
            == CONTEXT["resultDigest"])
