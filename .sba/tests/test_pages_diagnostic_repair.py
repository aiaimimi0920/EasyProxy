"""准确 1.0.5 十三对象恢复：零漂移、非空 writer、单次证据/CAS。"""
from concurrent.futures import ThreadPoolExecutor
from dataclasses import replace
import hashlib
import io
import json
from pathlib import Path
import sys
import threading

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import application
import assets_repair
import generation_repair
import pages_repair
import pages_diagnostic_repair as repair
from coordination import Coordinator, CoordinationError, Permit, StoreConflict, StoreUnknown, STATE_KEY, _encode
from repair_context import (PAGES_DIAGNOSTIC_CONTEXT as CONTEXT, PAGES_DIAGNOSTIC_PARENT_RELEASE as PARENT,
                            GENERATION_CONTEXT, ASSETS_CONTEXT, PAGES_CONTEXT, GITHUB_CONTEXT)
from request import RequestError, SECRETS
from test_coordination import MemoryStore, state
from test_pristine_repair import request_fixture
from test_request import parse


def recovery_request():
    value = request_fixture()
    value.update(previous=dict(PARENT), context=dict(CONTEXT))
    return value


@pytest.mark.parametrize("field", list(CONTEXT))
def test_every_parent_receipt_field_is_exact(field):
    value = recovery_request()
    value["context"][field] = 42 if field == "parentRunId" else "different"
    with pytest.raises(RequestError):
        parse(value)


@pytest.mark.parametrize("field", ["sourceSha", "applicationVersion"])
def test_wrong_previous_is_rejected(field):
    value = recovery_request()
    value["previous"][field] = "f" * 40 if field == "sourceSha" else "1.0.4"
    with pytest.raises(RequestError):
        parse(value)


def test_numerically_equal_float_run_id_is_not_an_exact_integer(partial):
    value = recovery_request()
    value["context"]["parentRunId"] = float(CONTEXT["parentRunId"])
    with pytest.raises(RequestError): parse(value)
    request, owner, _, store, record, root = partial
    with pytest.raises(repair.PagesDiagnosticRepairError):
        repair.begin_recovery(owner, replace(request, context=value["context"]), root, {})
    assert not record["calls"] and len(store.writes) == 1 and len(store.stages) == 12


@pytest.fixture
def partial(tmp_path, monkeypatch):
    request = parse(recovery_request())
    store = MemoryStore()
    store.bucket = request.config.bucket
    owner = Coordinator(store, request.config.identity("production"), request.config.inventory_digest)
    old = owner.begin("deploy", PARENT)
    monkeypatch.setattr(repair, "CONTROL_SHA256", hashlib.sha256(store.body).hexdigest())
    monkeypatch.setattr(repair, "CONTROL_ETAG", store.etag)
    monkeypatch.setattr(repair, "OPERATION", old.operation_id)
    for key, body in repair.expected_objects().items():
        operation, stage = key.split("/")[-2:]
        store.stages[(operation, stage.removesuffix(".json"))] = body
    assert len(store.stages) == 12
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
            return {"Body": io.BytesIO(store.stages[(operation, stage.removesuffix(".json"))])}
    store.client = Client()
    def parent(_token, *, context):
        assert context == (CONTEXT, GENERATION_CONTEXT, ASSETS_CONTEXT, PAGES_CONTEXT)[len(record["calls"])]
        name = context["repairId"]
        record["calls"].append(name)
        if record["mutation"] == name: raise repair.PagesDiagnosticRepairError("fixture")
    def github_parent(_token):
        record["calls"].append(GITHUB_CONTEXT["repairId"])
        if record["mutation"] == GITHUB_CONTEXT["repairId"]: raise repair.PagesDiagnosticRepairError("fixture")
    monkeypatch.setattr(repair, "parent_completed", parent)
    monkeypatch.setattr(repair, "github_parent_completed", github_parent)
    def readonly(name):
        def call(*_args, **kwargs):
            assert kwargs == ({} if name == "d1" else {"parent_release": PARENT})
            record["calls"].append(name)
            if record["mutation"] == name: raise repair.PagesDiagnosticRepairError("fixture")
            if name == "pages" and record["mutation"] == "etag": store.etag = '"' + "f" * 32 + '"'
        return call
    for name, symbol in (("d1", "verify_initialized"), ("github", "verify_configured"),
                         ("worker", "verify_existing_worker"), ("pages", "verify_existing_pages")):
        monkeypatch.setattr(repair, symbol, readonly(name))
    class Pages:
        def __init__(self, *_): pass
        def close(self): record["closed"] = True
    monkeypatch.setattr(repair, "PagesAPI", Pages)
    return request, owner, old, store, record, tmp_path


def run(partial):
    request, owner, _, _, _, root = partial
    return repair.begin_recovery(owner, request, root, dict.fromkeys(SECRETS, "fixture-only"))


def test_success_preserves_twelve_bodies_and_transfers_nonempty_writer_once(partial):
    request, owner, old, store, record, _ = partial
    original = dict(store.stages)
    permit, resume = run(partial)
    assert record["calls"] == [c["repairId"] for c in (CONTEXT, GENERATION_CONTEXT, ASSETS_CONTEXT, PAGES_CONTEXT, GITHUB_CONTEXT)] + ["d1", "github", "worker", "pages"]
    assert record["closed"] and len(store.writes) == 2 and len(store.stages) == 13
    assert all(store.stages[k] == v for k, v in original.items())
    assert state(store)["active"] is None
    assert state(store)["writer"] == {"operationId": permit.operation_id, "action": "deploy", "candidate": request.candidate}
    assert all(json.loads(body)["writer"] is not None for body, _ in store.writes)
    resume.assert_permit(owner, permit)
    assert resume.parent_release == PARENT
    marker = json.loads(store.stages[(permit.operation_id, "diagnostic-recovery-transition")])
    assert marker == {"schemaVersion": 1, "context": CONTEXT,
        "ancestry": [GENERATION_CONTEXT, ASSETS_CONTEXT, PAGES_CONTEXT, GITHUB_CONTEXT],
        "taskId": request.task_id, "previousControlSha256": hashlib.sha256(old.state_bytes).hexdigest(),
        "previousControlEtag": old.etag, "previousOperationId": old.operation_id,
        "resourceRelease": PARENT, "candidate": request.candidate}
    with pytest.raises(CoordinationError): owner.assert_owned(old)
    record["calls"].clear()
    with pytest.raises(repair.PagesDiagnosticRepairError): run(partial)
    assert len(store.writes) == 2 and len(store.stages) == 13


@pytest.mark.parametrize("mutation", [c["repairId"] for c in (CONTEXT, GENERATION_CONTEXT, ASSETS_CONTEXT, PAGES_CONTEXT, GITHUB_CONTEXT)] +
    ["d1", "github", "worker", "pages", "extra-object", "duplicate-object", "missing-object", "truncated", "etag"])
def test_any_parent_ancestry_resource_or_listing_drift_prevents_writes(partial, mutation):
    _, _, old, store, record, _ = partial
    record["mutation"] = mutation
    with pytest.raises(repair.PagesDiagnosticRepairError): run(partial)
    assert state(store)["writer"]["operationId"] == old.operation_id
    assert len(store.writes) == 1 and len(store.stages) == 12


@pytest.mark.parametrize("index", range(12))
def test_every_historical_body_is_checked_byte_for_byte(partial, index):
    store = partial[3]
    key = sorted(store.stages)[index]
    store.stages[key] = b" " + store.stages[key]
    with pytest.raises(repair.PagesDiagnosticRepairError): run(partial)
    assert len(store.writes) == 1 and len(store.stages) == 12


@pytest.mark.parametrize("mutation", ["control", "canonical", "etag", "identity", "inventory"])
def test_control_identity_digest_and_exact_etag_are_required(partial, mutation, monkeypatch):
    _, owner, _, store, _, _ = partial
    if mutation in ("control", "canonical"): store.body = b" " + store.body
    if mutation == "canonical": monkeypatch.setattr(repair, "CONTROL_SHA256", hashlib.sha256(store.body).hexdigest())
    if mutation == "etag": store.etag = '"' + "f" * 32 + '"'
    if mutation == "identity": owner.identity["environment"] = "different"
    if mutation == "inventory": owner.inventory_digest = "f" * 64
    with pytest.raises((repair.PagesDiagnosticRepairError, CoordinationError)): run(partial)
    assert len(store.writes) == 1 and len(store.stages) == 12


@pytest.mark.parametrize("failure", ["marker-unknown", "marker-invalid", "cas-unknown", "cas-response-lost", "cas-conflict", "cas-invalid", "post-cas-read"])
def test_ambiguous_write_retains_all_evidence_and_never_replays(partial, monkeypatch, failure):
    _, _, _, store, record, _ = partial
    if failure.startswith("marker"):
        create = store.create_stage
        def lost(*args):
            create(*args)
            if failure == "marker-unknown": raise StoreUnknown()
            return None
        monkeypatch.setattr(store, "create_stage", lost)
    elif failure == "cas-unknown": store.ambiguous = True
    elif failure == "cas-response-lost": store.fail_after_commit = True
    elif failure == "cas-conflict": monkeypatch.setattr(store, "compare_and_set", lambda *_: (_ for _ in ()).throw(StoreConflict()))
    else:
        cas = store.compare_and_set
        def bad(*args):
            result = cas(*args)
            if failure == "post-cas-read": store.read_error = True
            return None if failure == "cas-invalid" else result
        monkeypatch.setattr(store, "compare_and_set", bad)
    with pytest.raises((repair.PagesDiagnosticRepairError, StoreUnknown, CoordinationError)): run(partial)
    assert state(store)["active"] is None and state(store)["writer"] is not None and len(store.stages) == 13
    before = (len(store.writes), len(store.stages))
    record["calls"].clear()
    with pytest.raises((repair.PagesDiagnosticRepairError, StoreUnknown, CoordinationError)): run(partial)
    assert before == (len(store.writes), len(store.stages))


def test_control_change_after_marker_blocks_cas(partial, monkeypatch):
    store = partial[3]
    create = store.create_stage
    def drift(*args):
        result = create(*args)
        store.etag = '"' + "f" * 32 + '"'
        return result
    monkeypatch.setattr(store, "create_stage", drift)
    with pytest.raises(repair.PagesDiagnosticRepairError): run(partial)
    assert len(store.writes) == 1 and len(store.stages) == 13 and state(store)["writer"] is not None


@pytest.mark.parametrize("old_module", [pages_repair, assets_repair, generation_repair])
def test_new_request_cannot_enter_old_recovery(partial, old_module):
    request, owner, _, store, record, root = partial
    with pytest.raises(RuntimeError): old_module.begin_recovery(owner, request, root, {})
    with pytest.raises(repair.PagesDiagnosticRepairError):
        repair.begin_recovery(owner, replace(request, context=PAGES_CONTEXT), root, {})
    with pytest.raises(repair.PagesDiagnosticRepairError):
        repair.begin_recovery(owner, replace(request, previous=assets_repair.ASSETS_PARENT_RELEASE), root, {})
    assert not record["calls"] and len(store.writes) == 1 and len(store.stages) == 12


@pytest.mark.parametrize("sha", ["f" * 40, "b" * 40, "c1dee04af9b0157551f316586d1a9a881e3f13ac"])
def test_unreviewed_or_failed_before_transfer_parent_is_not_a_resume(sha):
    with pytest.raises(pages_repair.PagesRepairError):
        pages_repair.PagesResume("a" * 32, "b" * 64, sha).parent_release


def test_two_cas_contenders_cannot_both_get_permits(partial, monkeypatch):
    # Both callers pass the last read; atomic CAS, not a read-time mutex, arbitrates.
    _, owner, _, store, _, _ = partial
    monkeypatch.setattr(repair, "parent_completed", lambda *_a, **_k: None)
    monkeypatch.setattr(repair, "github_parent_completed", lambda *_: None)
    barrier = threading.Barrier(2)
    objects_barrier = threading.Barrier(2)
    checked = repair.checked_state
    objects = repair.checked_objects
    local = threading.local()
    def synchronized_objects(current):
        objects(current)
        local.objects = getattr(local, "objects", 0) + 1
        if local.objects == 2: objects_barrier.wait(timeout=5)
    def synchronized(current):
        result = checked(current)
        local.reads = getattr(local, "reads", 0) + 1
        if local.reads == 3: barrier.wait(timeout=5)
        return result
    monkeypatch.setattr(repair, "checked_state", synchronized)
    monkeypatch.setattr(repair, "checked_objects", synchronized_objects)
    def attempt(_):
        try: return run(partial)
        except (repair.PagesDiagnosticRepairError, CoordinationError): return None
    with ThreadPoolExecutor(max_workers=2) as pool: results = list(pool.map(attempt, range(2)))
    assert sum(result is not None for result in results) == 1
    assert len(store.writes) == 2 and state(store)["writer"] is not None
    permit, resume = next(result for result in results if result is not None)
    resume.assert_permit(owner, permit)
    def claim(_):
        try: owner.claim_stage(permit, "fixture-publication"); return True
        except CoordinationError: return False
    with ThreadPoolExecutor(max_workers=2) as pool: claims = list(pool.map(claim, range(2)))
    assert claims.count(True) == 1


def test_manifest_has_six_exact_scopes_and_unchanged_secret_set():
    manifest = json.loads((Path(__file__).resolve().parents[1] / "manifest.json").read_bytes())
    assert manifest["version"] == "1.0.13" and len(manifest["repairs"]) == 10
    scope = next(row for row in manifest["repairs"] if row["id"] == CONTEXT["repairId"])
    assert scope["fromErrorCodes"] == [CONTEXT["errorCode"]] and set(scope["secretNames"]) == SECRETS


def test_application_routes_only_this_exact_recovery_request(tmp_path, monkeypatch):
    request = parse(recovery_request())
    monkeypatch.setattr(application, "inspect_checkout", lambda *_: [])
    monkeypatch.setattr(application, "assert_pristine", lambda *_: pytest.fail("not pristine"))
    for name in ("prepare_linux_tools", "build_runtime", "build_image", "build_sealer", "build_aggregator"):
        monkeypatch.setattr(application, name, lambda *_a, **_k: None)
    def deploy(current, *_args, **kwargs):
        assert current == replace(request, action="deploy", previous=None, context=None)
        assert kwargs["recovery_request"] is request
        return {"checks": {"suite-published": True, "service-ready": True}}
    monkeypatch.setattr(application, "deploy_suite", deploy)
    assert application.execute(request, tmp_path, tmp_path.parent / (tmp_path.name + "-output"),
        environment=dict.fromkeys(SECRETS, "fixture-only"))["checks"]["repair-completed"] is True
