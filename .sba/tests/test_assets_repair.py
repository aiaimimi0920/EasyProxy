"""准确新 UNKNOWN 的十对象、单次CAS、证据保留和父版本绑定。"""
from dataclasses import replace
import hashlib
import io
import json
from pathlib import Path
import sys

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import application
import assets_repair as repair
from coordination import Coordinator, CoordinationError, StoreUnknown, StoreConflict, STATE_KEY
from pages_repair import PagesResume, PagesRepairError
from repair_context import ASSETS_CONTEXT, ASSETS_PARENT_RELEASE, PAGES_PARENT_RELEASE
from request import RequestError, SECRETS
from test_coordination import MemoryStore, state
from test_pristine_repair import request_fixture
from test_request import parse


def recovery_request():
    value = request_fixture()
    value.update(previous=dict(ASSETS_PARENT_RELEASE), context=dict(ASSETS_CONTEXT))
    return value


@pytest.mark.parametrize("field", list(ASSETS_CONTEXT))
def test_context_rejects_every_different_parent_receipt_field(field):
    value = recovery_request()
    value["context"][field] = 42 if field == "parentRunId" else "different"
    with pytest.raises(RequestError): parse(value)


def test_parent_release_cannot_be_the_earlier_unknown():
    value = recovery_request()
    value["previous"] = dict(PAGES_PARENT_RELEASE)
    with pytest.raises(RequestError): parse(value)


@pytest.fixture
def partial(tmp_path, monkeypatch):
    request = parse(recovery_request())
    store = MemoryStore()
    store.bucket = request.config.bucket
    owner = Coordinator(store, request.config.identity("production"), request.config.inventory_digest)
    old = owner.begin("deploy", ASSETS_PARENT_RELEASE)
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
            if record["mutation"] == "missing-object": keys.pop()
            return {"IsTruncated": record["mutation"] == "truncated", "Contents": [{"Key": key} for key in keys]}
        def get_object(self, Key, **_kwargs):
            operation, stage = Key.split("/")[-2:]
            body = store.stages[(operation, stage.removesuffix(".json"))]
            return {"Body": io.BytesIO(b" " + body if record["mutation"] == "stage-bytes" else body)}
    store.client = Client()
    def readonly(name):
        def call(*_args, **kwargs):
            assert kwargs == ({"context": ASSETS_CONTEXT} if name == "parent" else
                              {} if name == "d1" else {"parent_release": ASSETS_PARENT_RELEASE})
            record["calls"].append(name)
            if record["mutation"] == name: raise repair.AssetsRepairError("fixture")
            if name == "pages" and record["mutation"] == "etag": store.etag = '"' + "f" * 32 + '"'
        return call
    for name, symbol in (("parent", "parent_completed"), ("d1", "verify_initialized"),
                         ("github", "verify_configured"), ("worker", "verify_existing_worker"),
                         ("pages", "verify_existing_pages")):
        monkeypatch.setattr(repair, symbol, readonly(name))
    class Pages:
        def __init__(self, *_): pass
        def close(self): record["closed"] = True
    monkeypatch.setattr(repair, "PagesAPI", Pages)
    return request, owner, old, store, record, tmp_path


def run(partial):
    request, owner, _, _, _, root = partial
    return repair.begin_recovery(owner, request, root, dict.fromkeys(SECRETS, "fixture-only"))


def test_transfer_retains_all_ten_objects_and_invalidates_old_permit(partial):
    request, owner, old, store, record, _ = partial
    previous = dict(store.stages)
    assert len(previous) == 9
    permit, resume = run(partial)
    assert record["calls"] == ["parent", "d1", "github", "worker", "pages"] and record["closed"]
    assert state(store)["active"] is None and len(store.writes) == 2
    assert state(store)["writer"] == {"operationId": permit.operation_id, "action": "deploy", "candidate": request.candidate}
    assert len(store.stages) == 10 and all(store.stages[k] == v for k, v in previous.items())
    resume.assert_permit(owner, permit)
    assert resume.parent_release == ASSETS_PARENT_RELEASE
    transition = json.loads(store.stages[(permit.operation_id, "assets-recovery-transition")])
    assert transition["context"] == ASSETS_CONTEXT and transition["previousOperationId"] == old.operation_id
    with pytest.raises(CoordinationError): owner.assert_owned(old)
    with pytest.raises(repair.AssetsRepairError): run(partial)
    assert len(store.writes) == 2 and len(store.stages) == 10


@pytest.mark.parametrize("mutation", ["parent", "d1", "github", "worker", "pages", "extra-object", "duplicate-object",
                                      "missing-object", "truncated", "stage-bytes", "etag"])
def test_any_drift_prevents_all_transition_writes(partial, mutation):
    _, _, old, store, record, _ = partial
    record["mutation"] = mutation
    with pytest.raises(repair.AssetsRepairError): run(partial)
    assert state(store)["writer"]["operationId"] == old.operation_id
    assert len(store.writes) == 1 and len(store.stages) == 9


@pytest.mark.parametrize("failure", ["marker-unknown", "cas-unknown", "cas-response-lost", "cas-conflict"])
def test_unknown_or_conflict_never_replays_unlocks_or_removes_evidence(partial, monkeypatch, failure):
    store = partial[3]
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
    assert state(store)["active"] is None and state(store)["writer"] is not None and len(store.stages) == 10
    before = (len(store.writes), len(store.stages))
    with pytest.raises((repair.AssetsRepairError, CoordinationError)): run(partial)
    assert before == (len(store.writes), len(store.stages))


def test_noncanonical_control_and_unreviewed_resume_parent_are_rejected(partial):
    _, owner, old, store, _, _ = partial
    store.body = b" " + store.body
    with pytest.raises(repair.AssetsRepairError): run(partial)
    with pytest.raises(PagesRepairError): PagesResume(old.operation_id, "a" * 64, "f" * 40).assert_permit(owner, old)
    assert len(store.writes) == 1 and len(store.stages) == 9


def test_manifest_has_five_separate_repairs_with_existing_secrets_only():
    manifest = json.loads((Path(__file__).resolve().parents[1] / "manifest.json").read_bytes())
    assert manifest["version"] == "1.0.6" and len(manifest["repairs"]) == 5
    scope = next(row for row in manifest["repairs"] if row["id"] == ASSETS_CONTEXT["repairId"])
    assert scope["fromErrorCodes"] == [ASSETS_CONTEXT["errorCode"]] and set(scope["secretNames"]) == SECRETS


def test_application_passes_the_exact_assets_recovery_request(tmp_path, monkeypatch):
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
