"""Pages 恢复仅匹配准确父任务；持锁 CAS、证据保留和不重放均使用真实 Coordinator。"""
from dataclasses import replace
import hashlib
import io
import json
from pathlib import Path
import sys

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import application
import pages_repair as repair
from coordination import Coordinator, CoordinationError, StoreUnknown, StoreConflict, STATE_KEY
from repair_context import PAGES_CONTEXT, PAGES_PARENT_RELEASE, ASSETS_CONTEXT, GENERATION_CONTEXT
from repair_context import PAGES_DIAGNOSTIC_CONTEXT
from request import RequestError, SECRETS
from test_coordination import MemoryStore, state
from test_pristine_repair import request_fixture
from test_request import parse


def recovery_request():
    value = request_fixture()
    value.update(previous=dict(PAGES_PARENT_RELEASE), context=dict(PAGES_CONTEXT))
    return value


@pytest.mark.parametrize("field", list(PAGES_CONTEXT))
def test_context_rejects_every_different_parent_receipt_field(field):
    value = recovery_request()
    value["context"][field] = 42 if field == "parentRunId" else "different"
    with pytest.raises(RequestError):
        parse(value)


@pytest.fixture
def partial(tmp_path, monkeypatch):
    request = parse(recovery_request())
    store = MemoryStore()
    store.bucket = request.config.bucket
    owner = Coordinator(store, request.config.identity("production"), request.config.inventory_digest)
    old = owner.begin("deploy", PAGES_PARENT_RELEASE)
    monkeypatch.setattr(repair, "CONTROL_SHA256", hashlib.sha256(store.body).hexdigest())
    monkeypatch.setattr(repair, "OPERATION", old.operation_id)
    for key, body in repair.expected_objects().items():
        operation, stage = key.split("/")[-2:]
        store.stages[(operation, stage.removesuffix(".json"))] = body
    record = {"calls": [], "mutation": None, "closed": False}
    class Client:
        def list_objects_v2(self, **_kwargs):
            keys = [STATE_KEY] + [f"_sba/operations/{op}/{stage}.json" for op, stage in store.stages]
            if record["mutation"] == "extra-object": keys.append("unexpected-object")
            if record["mutation"] == "duplicate-object": keys[-1] = keys[-2]
            return {"IsTruncated": record["mutation"] == "truncated", "Contents": [{"Key": key} for key in keys]}
        def get_object(self, Key, **_kwargs):
            operation, stage = Key.split("/")[-2:]
            body = store.stages[(operation, stage.removesuffix(".json"))]
            return {"Body": io.BytesIO(b" " + body if record["mutation"] == "stage-bytes" else body)}
    store.client = Client()
    def readonly(name):
        def call(*_args):
            record["calls"].append(name)
            if record["mutation"] == name: raise repair.PagesRepairError("fixture")
            if name == "pages" and record["mutation"] == "etag": store.etag = '"' + "f" * 32 + '"'
        return call
    for name, symbol in (("parent", "parent_completed"), ("d1", "verify_initialized"),
                         ("github", "verify_configured"), ("worker", "verify_existing_worker"),
                         ("pages", "verify_existing_pages")):
        monkeypatch.setattr(repair, symbol, readonly(name))
    class Pages:
        def __init__(self, *_): pass
        def close(self): record["closed"] = True
    monkeypatch.setattr(sys.modules["pages_api"], "PagesAPI", Pages)
    return request, owner, old, store, record, tmp_path


def run(partial):
    request, owner, _, _, _, root = partial
    return repair.begin_recovery(owner, request, root, dict.fromkeys(SECRETS, "fixture-only"))


def test_transfer_retains_every_old_object_and_invalidates_the_old_permit(partial):
    request, owner, old, store, record, _ = partial
    previous = dict(store.stages)
    permit, resume = run(partial)
    assert record["calls"] == ["parent", "d1", "github", "worker", "pages"] and record["closed"]
    assert state(store)["active"] is None and len(store.writes) == 2
    assert state(store)["writer"] == {"operationId": permit.operation_id, "action": "deploy", "candidate": request.candidate}
    assert len(store.stages) == 7 and all(store.stages[k] == v for k, v in previous.items())
    resume.assert_permit(owner, permit)
    with pytest.raises(CoordinationError): owner.assert_owned(old)
    with pytest.raises(repair.PagesRepairError): run(partial)
    assert len(store.writes) == 2 and len(store.stages) == 7


@pytest.mark.parametrize("mutation", ["parent", "d1", "github", "worker", "pages", "extra-object", "duplicate-object",
                                      "truncated", "stage-bytes", "etag"])
def test_any_drift_prevents_all_transition_writes(partial, mutation):
    _, owner, old, store, record, _ = partial
    record["mutation"] = mutation
    with pytest.raises(repair.PagesRepairError): run(partial)
    assert state(store)["writer"]["operationId"] == old.operation_id
    assert len(store.writes) == 1 and len(store.stages) == 6


@pytest.mark.parametrize("failure", ["marker-unknown", "cas-unknown", "cas-response-lost", "cas-conflict"])
def test_unknown_or_conflict_never_replays_unlocks_or_removes_evidence(partial, monkeypatch, failure):
    _, _, _, store, _, _ = partial
    if failure == "marker-unknown":
        create = store.create_stage
        def lost(*args):
            create(*args)
            raise StoreUnknown()
        monkeypatch.setattr(store, "create_stage", lost)
    elif failure == "cas-unknown": store.ambiguous = True
    elif failure == "cas-response-lost": store.fail_after_commit = True
    else:
        monkeypatch.setattr(store, "compare_and_set", lambda *_: (_ for _ in ()).throw(StoreConflict()))
    with pytest.raises((StoreUnknown, CoordinationError)): run(partial)
    assert state(store)["active"] is None and state(store)["writer"] is not None and len(store.stages) == 7
    before = (len(store.writes), len(store.stages))
    with pytest.raises((repair.PagesRepairError, CoordinationError)): run(partial)
    assert before == (len(store.writes), len(store.stages))


def test_semantically_equal_noncanonical_control_is_rejected(partial):
    store = partial[3]
    store.body = b" " + store.body
    with pytest.raises(repair.PagesRepairError): run(partial)
    assert len(store.writes) == 1 and len(store.stages) == 6


def test_manifest_has_a_third_separate_error_scope_with_existing_secrets_only():
    manifest = json.loads((Path(__file__).resolve().parents[1] / "manifest.json").read_bytes())
    assert manifest["version"] == "1.0.8" and len(manifest["repairs"]) == 6
    scope = next(row for row in manifest["repairs"] if row["id"] == PAGES_CONTEXT["repairId"])
    assert scope["fromErrorCodes"] == [PAGES_CONTEXT["errorCode"]] and set(scope["secretNames"]) == SECRETS


def test_application_passes_only_the_exact_pages_recovery_request(tmp_path, monkeypatch):
    request = parse(recovery_request())
    monkeypatch.setattr(application, "inspect_checkout", lambda *_: [])
    monkeypatch.setattr(application, "assert_pristine", lambda *_: pytest.fail("not pristine"))
    for name in ("prepare_linux_tools", "build_runtime", "build_image", "build_sealer", "build_aggregator"):
        monkeypatch.setattr(application, name, lambda *_args, **_kwargs: None)
    def deploy(current, *_args, **kwargs):
        assert current == replace(request, action="deploy", previous=None, context=None)
        assert kwargs["recovery_request"] is request
        return {"checks": {"suite-published": True, "service-ready": True}}
    monkeypatch.setattr(application, "deploy_suite", deploy)
    assert application.execute(request, tmp_path, tmp_path.parent / (tmp_path.name + "-output"),
                               environment=dict.fromkeys(SECRETS, "fixture-only"))["checks"]["repair-completed"] is True


@pytest.mark.parametrize("mutation", [None, "status", "conclusion", "run_attempt", "head_sha", "path", "display_title", "event", "http", "json"])
@pytest.mark.parametrize("context", [PAGES_CONTEXT, ASSETS_CONTEXT, GENERATION_CONTEXT, PAGES_DIAGNOSTIC_CONTEXT])
def test_parent_is_terminal_first_attempt_with_exact_executor_and_request(monkeypatch, mutation, context):
    row = {"id": context["parentRunId"], "run_attempt": 1, "status": "completed", "conclusion": "success",
           "head_sha": repair.EXECUTOR_SHA, "event": "workflow_dispatch", "path": ".github/workflows/sba-execute-linux.yml",
           "display_title": "sba:" + context["parentTaskId"] + ":" + context["requestDigest"]}
    if mutation in row: row[mutation] = True if mutation == "run_attempt" else "wrong"
    calls = []
    class Response:
        status_code = 307 if mutation == "http" else 200
        def __enter__(self): return self
        def __exit__(self, *_): pass
        def iter_content(self, _): yield b"invalid" if mutation == "json" else json.dumps(row).encode()
    class Session:
        def close(self): calls.append("closed")
        def get(self, url, **kwargs):
            assert url.endswith("/actions/runs/" + str(context["parentRunId"])) and kwargs["allow_redirects"] is False
            calls.append("GET")
            return Response()
    monkeypatch.setattr(repair, "secure_session", Session)
    if mutation is None: repair.parent_completed("fixture-only", context=context)
    else:
        with pytest.raises(repair.PagesRepairError): repair.parent_completed("fixture-only", context=context)
    assert calls == ["GET", "closed"]
