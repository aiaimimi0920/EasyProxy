"""准确1.0.9资产验证UNKNOWN的只读回执重核；不转发签名下载凭据。"""
import hashlib
import io
import json
import zipfile

import requests

from cloudflare_http import decode_object, secure_session
from generation_outputs_parent import _signed_location
from github_repair import EXECUTOR_SHA
from pages_repair import parent_completed
from repair_context import PAGES_ASSET_PROTOCOL_CONTEXT as CONTEXT, PAGES_ASSET_PROTOCOL_PARENT_RELEASE as PARENT

ARTIFACT_ID = 12685769611
ARTIFACT_SHA256 = "3e709407808da0458b786788d09ce52af49f8e9fdb727f08768aec33918938c4"
BASE = "https://api.github.com/repos/aiaimimi0920/SpringBok/actions/"


class PagesAssetProtocolParentError(RuntimeError):
    pass


def require(value, _code=None):
    if not value:
        raise PagesAssetProtocolParentError("EASYPROXY_SBA_PAGES_ASSET_PROTOCOL_PARENT_REJECTED") from None


def _read(response, limit):
    require(response.status_code == 200)
    raw = bytearray()
    for chunk in response.iter_content(16384):
        raw.extend(chunk)
        require(len(raw) <= limit)
    return bytes(raw)


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
    expected = {"schemaVersion": 3, "taskId": CONTEXT["parentTaskId"], "action": "repair", **PARENT,
                "status": "unknown", "checks": [], "errorCode": CONTEXT["errorCode"]}
    require(type(result) is dict and type(result.get("schemaVersion")) is int and result == expected
            and hashlib.sha256(json.dumps(result, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode()).hexdigest()
            == CONTEXT["resultDigest"])
