"""最新准确失败回执的有界只读重核；签名下载不转发GitHub凭据。"""
import hashlib
import io
import json
import re
from urllib.parse import urlsplit
import zipfile

import requests

from cloudflare_http import decode_object, secure_session
from github_repair import EXECUTOR_SHA
from pages_repair import parent_completed
from repair_context import GENERATION_OUTPUTS_CONTEXT as CONTEXT, GENERATION_OUTPUTS_PARENT_RELEASE as PARENT

ARTIFACT_ID = 12666772485
ARTIFACT_SHA256 = "d37d79ef615deb27c89498be1917ec746ece30f6d24ac6330ed87fe61aef305a"
BASE = "https://api.github.com/repos/aiaimimi0920/SpringBok/actions/"


class GenerationOutputsParentError(RuntimeError):
    pass


def require(value, _code=None):
    if not value:
        raise GenerationOutputsParentError("EASYPROXY_SBA_GENERATION_OUTPUTS_PARENT_REJECTED") from None


def _read(response, limit):
    require(response.status_code == 200)
    raw = bytearray()
    for chunk in response.iter_content(16384):
        raw.extend(chunk)
        require(len(raw) <= limit)
    return bytes(raw)


def _signed_location(location):
    require(type(location) is str and all(32 < ord(c) < 127 for c in location) and "\\" not in location)
    try:
        url = urlsplit(location)
        require(url.scheme == "https" and url.username is None and url.password is None
                and url.port is None and not url.fragment and url.path.startswith("/")
                and re.fullmatch(r"[a-z0-9-]+\.blob\.core\.windows\.net", url.netloc) is not None)
    except ValueError:
        require(False)
    return location


def verify_parent_receipt(token):
    try:
        _verify_parent_receipt(token)
    except (requests.RequestException, OSError):
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
    # 与平台同一签名存储边界：新会话、无Bearer、禁止第二跳或重试。
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
    expected = {"schemaVersion": 3, "taskId": CONTEXT["parentTaskId"], "action": "repair", **PARENT,
                "status": "unknown", "checks": [], "errorCode": CONTEXT["errorCode"]}
    require(type(result) is dict and type(result.get("schemaVersion")) is int and result == expected
            and hashlib.sha256(json.dumps(result, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode()).hexdigest()
            == CONTEXT["resultDigest"])
