"""最新回执只读获取：单次GET、有界ZIP、准确摘要和不转发凭据。"""
import copy
import hashlib
import io
import json
from types import SimpleNamespace
import zipfile
import traceback
from pathlib import Path
import sys

import pytest
import requests

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import fresh_domain_parent as parent
from repair_context import FRESH_DOMAIN_CONTEXT as CONTEXT, FRESH_DOMAIN_PARENT_RELEASE as PARENT


def envelope():
    return {"schemaVersion": 1, "permitId": "fixture-permit", "requestDigest": CONTEXT["requestDigest"],
        "runId": CONTEXT["parentRunId"], "runAttempt": 1, "executorSha": parent.EXECUTOR_SHA,
        "result": {"schemaVersion": 3, "taskId": CONTEXT["parentTaskId"], "action": "repair", **PARENT,
                   "status": "unknown", "checks": [], "errorCode": CONTEXT["errorCode"]}}


def zipped(value, *, extra=False):
    stream = io.BytesIO()
    with zipfile.ZipFile(stream, "w") as archive:
        archive.writestr("receipt.json", value if isinstance(value, bytes) else json.dumps(value).encode())
        if extra: archive.writestr("extra.json", b"{}")
    return stream.getvalue()


@pytest.fixture
def wire(monkeypatch):
    raw = zipped(envelope())
    digest = hashlib.sha256(raw).hexdigest()
    monkeypatch.setattr(parent, "ARTIFACT_SHA256", digest)
    state = SimpleNamespace(raw=raw, location="https://fixture.blob.core.windows.net/path?sig=PRIVATE_SIGNED_QUERY",
        redirect_status=302, download_status=200, list_status=200, calls=[], sessions=0, closed=[])
    state.page = {"total_count": 1, "artifacts": [{"id": parent.ARTIFACT_ID, "expired": False,
        "digest": "sha256:" + digest, "name": "sba-result-" + CONTEXT["parentTaskId"] + "-" + CONTEXT["requestDigest"]}]}
    def terminal(token, *, context):
        assert token == "fixture-private-token" and context == CONTEXT
        state.calls.append(("parent", {}))
    monkeypatch.setattr(parent, "parent_completed", terminal)
    class Response:
        def __init__(self, body, status, headers=None):
            self.body, self.status_code, self.headers = body, status, headers or {}
        def __enter__(self): return self
        def __exit__(self, *_): pass
        def iter_content(self, size):
            for start in range(0, len(self.body), size): yield self.body[start:start + size]
    class Session:
        def __init__(self):
            self.number = state.sessions
            state.sessions += 1
        def close(self): state.closed.append(self.number)
        def get(self, url, **kwargs):
            assert kwargs["allow_redirects"] is False and kwargs["stream"] is True
            assert kwargs["timeout"] == (10, 60)
            state.calls.append((url, kwargs))
            if self.number == 1:
                assert url == state.location and "headers" not in kwargs
                return Response(state.raw, state.download_status)
            assert kwargs["headers"]["Authorization"] == "Bearer fixture-private-token"
            if "/artifacts?" in url:
                return Response(json.dumps(state.page).encode(), state.list_status)
            assert url == parent.BASE + "artifacts/" + str(parent.ARTIFACT_ID) + "/zip"
            return Response(b"", state.redirect_status, {"Location": state.location})
    monkeypatch.setattr(parent, "secure_session", Session)
    return state


def test_exact_parent_is_read_once_and_signed_download_is_a_new_credential_free_session(wire):
    parent.verify_parent_receipt("fixture-private-token")
    assert len(wire.calls) == 4 and wire.sessions == 2 and wire.closed == [0, 1]


@pytest.mark.parametrize("mutation", ["count", "float-count", "extra", "id", "float-id", "name", "digest", "expired", "http"])
def test_artifact_identity_drift_stops_before_any_signed_request(wire, mutation):
    artifact = wire.page["artifacts"][0]
    if mutation == "count": wire.page["total_count"] = 2
    elif mutation == "float-count": wire.page["total_count"] = 1.0
    elif mutation == "extra": wire.page["artifacts"].append(copy.deepcopy(artifact))
    elif mutation == "float-id": artifact["id"] = float(parent.ARTIFACT_ID)
    elif mutation in artifact: artifact[mutation] = True if mutation == "expired" else "different"
    else: wire.list_status = 500
    with pytest.raises(parent.FreshDomainParentError): parent.verify_parent_receipt("fixture-private-token")
    assert len(wire.calls) == 2 and wire.sessions == 1 and wire.closed == [0]


@pytest.mark.parametrize("url", [None, "http://fixture.blob.core.windows.net/a", "https://other.invalid/a",
    "https://token@fixture.blob.core.windows.net/a", "https://fixture.blob.core.windows.net:443/a",
    "https://fixture.blob.core.windows.net:bad/a", "https://fixture.blob.core.windows.net/a#fragment",
    "https://fixture.blob.core.windows.net/a\nprivate", "https://fixture.blob.core.windows.net\\@other.invalid/a"])
def test_redirect_is_only_a_single_reviewed_https_blob_get_without_credentials(wire, url):
    wire.location = url
    with pytest.raises(parent.FreshDomainParentError): parent.verify_parent_receipt("fixture-private-token")
    assert len(wire.calls) == 3 and wire.sessions == 1 and wire.closed == [0]


@pytest.mark.parametrize("mutation", ["redirect-http", "second-redirect", "oversize", "digest"])
def test_no_retry_or_second_redirect_on_bad_download(wire, mutation):
    if mutation == "redirect-http": wire.redirect_status = 307
    elif mutation == "second-redirect": wire.download_status = 302
    elif mutation == "oversize": wire.raw = b"x" * 65537
    else: wire.raw += b"changed"
    with pytest.raises(parent.FreshDomainParentError): parent.verify_parent_receipt("fixture-private-token")
    assert len(wire.calls) == (3 if mutation == "redirect-http" else 4)
    assert wire.closed == list(range(wire.sessions))


@pytest.mark.parametrize("mutation", ["extra-file", "large-entry", "invalid-json", "duplicate-json", "schema", "runId", "runAttempt",
    "executorSha", "requestDigest", "permitId", "result-status", "result-sourceSha", "result-schema", "result-action", "extra-field", "zip"])
def test_even_a_digest_bound_fixture_requires_the_exact_envelope_and_unknown_result(wire, monkeypatch, mutation, capsys):
    value = envelope()
    if mutation in ("schema", "runId", "runAttempt"):
        key = "schemaVersion" if mutation == "schema" else mutation
        value[key] = float(value[key])
    elif mutation in ("executorSha", "requestDigest", "permitId"): value[mutation] = "" if mutation == "permitId" else "private-invalid"
    elif mutation.startswith("result-"):
        key = mutation.removeprefix("result-")
        value["result"]["schemaVersion" if key == "schema" else key] = True if key == "schema" else "private-invalid"
    elif mutation == "extra-field": value["private"] = "private-invalid"
    if mutation == "large-entry": value = b" " * 16385
    if mutation == "invalid-json": value = b"private-invalid"
    if mutation == "duplicate-json": value = json.dumps(value).encode().replace(b'{', b'{"schemaVersion":1,', 1)
    wire.raw = b"not-a-zip" if mutation == "zip" else zipped(value, extra=mutation == "extra-file")
    digest = hashlib.sha256(wire.raw).hexdigest()
    monkeypatch.setattr(parent, "ARTIFACT_SHA256", digest)
    wire.page["artifacts"][0]["digest"] = "sha256:" + digest
    with pytest.raises(parent.FreshDomainParentError) as error: parent.verify_parent_receipt("fixture-private-token")
    assert str(error.value) == "EASYPROXY_SBA_FRESH_DOMAIN_PARENT_REJECTED"
    assert "private" not in "".join(capsys.readouterr()) and len(wire.calls) == 4


@pytest.mark.parametrize("stage", ["parent", "list-get", "list-body", "redirect-get", "signed-get", "signed-body"])
def test_transport_exceptions_are_sanitized_without_chained_signed_urls_or_retries(wire, monkeypatch, stage):
    marker = "PRIVATE_TOKEN_AND_SIGNED_QUERY"
    def failed(*_a, **_k): raise requests.ConnectionError(marker)
    if stage == "parent": monkeypatch.setattr(parent, "parent_completed", failed)
    original = parent.secure_session
    def session():
        current = original()
        get = current.get
        def request(url, **kwargs):
            part = "signed" if current.number == 1 else "list" if "/artifacts?" in url else "redirect"
            if stage == part + "-get": failed()
            response = get(url, **kwargs)
            if stage == part + "-body": response.iter_content = failed
            return response
        current.get = request
        return current
    monkeypatch.setattr(parent, "secure_session", session)
    with pytest.raises(parent.FreshDomainParentError) as error:
        parent.verify_parent_receipt("fixture-private-token")
    assert str(error.value) == "EASYPROXY_SBA_FRESH_DOMAIN_PARENT_REJECTED"
    assert marker not in "".join(traceback.format_exception(error.value))
    assert error.value.__suppress_context__ and wire.closed == list(range(wire.sessions))
    assert len(wire.calls) <= 4 and sum(url == wire.location for url, _ in wire.calls) <= 1


@pytest.mark.parametrize("field", [None, "id", "run_attempt", "status", "conclusion", "head_sha", "event", "path", "display_title"])
def test_original_run_must_be_exact_terminal_attempt_before_artifacts(monkeypatch, field):
    row = {"id": CONTEXT["parentRunId"], "run_attempt": 1, "status": "completed", "conclusion": "success",
        "head_sha": parent.EXECUTOR_SHA, "event": "workflow_dispatch", "path": ".github/workflows/sba-execute-linux.yml",
        "display_title": "sba:" + CONTEXT["parentTaskId"] + ":" + CONTEXT["requestDigest"]}
    if field is not None: row[field] = float(row[field]) if field in ("id", "run_attempt") else "different"
    calls, closed = [], []
    class Response:
        status_code = 200
        def __enter__(self): return self
        def __exit__(self, *_): pass
        def iter_content(self, _): yield json.dumps(row).encode()
    class Session:
        def close(self): closed.append(True)
        def get(self, url, **kwargs):
            assert url == parent.BASE + "runs/" + str(CONTEXT["parentRunId"])
            assert kwargs["allow_redirects"] is False and kwargs["stream"] is True
            assert kwargs["headers"]["Authorization"] == "Bearer fixture-token"
            calls.append(url)
            return Response()
    monkeypatch.setattr(parent, "secure_session", Session)
    if field is None: parent.parent_completed("fixture-token", context=CONTEXT)
    else:
        with pytest.raises(parent.FreshDomainParentError): parent.parent_completed("fixture-token", context=CONTEXT)
    assert len(calls) == 1 and closed == [True]
