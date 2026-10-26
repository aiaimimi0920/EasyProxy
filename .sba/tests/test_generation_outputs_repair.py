"""新失败与原资源分离；真实Coordinator验证十三对象保留和单次CAS。"""
from concurrent.futures import ThreadPoolExecutor
from dataclasses import replace
import hashlib
import json
import threading

import pytest

import assets_repair
import generation_repair
import generation_outputs_repair as repair
import pages_repair
import pages_diagnostic_repair as diagnostic
from coordination import CoordinationError, StoreConflict, StoreUnknown
from repair_context import PAGES_DIAGNOSTIC_CONTEXT, GENERATION_CONTEXT, ASSETS_CONTEXT, PAGES_CONTEXT, GITHUB_CONTEXT
from request import SECRETS
from test_coordination import state
from test_generation_outputs_request import CONTEXT, PARENT, RESOURCE, recovery_request
from test_pages_diagnostic_repair import partial as diagnostic_partial
from test_request import parse

ERRORS = (repair.GenerationOutputsRepairError, diagnostic.PagesDiagnosticRepairError, CoordinationError)
ANCESTRY = [PAGES_DIAGNOSTIC_CONTEXT, GENERATION_CONTEXT, ASSETS_CONTEXT, PAGES_CONTEXT, GITHUB_CONTEXT]


@pytest.fixture
def partial(diagnostic_partial, monkeypatch):
    _, owner, old, store, record, root = diagnostic_partial
    request = parse(recovery_request())
    for symbol in ("CONTROL_SHA256", "CONTROL_ETAG", "OPERATION", "verify_initialized", "verify_configured",
                   "verify_existing_worker", "verify_existing_pages", "PagesAPI", "github_parent_completed"):
        monkeypatch.setattr(repair, symbol, getattr(diagnostic, symbol))
    def latest(_token):
        record["calls"].append("latest-receipt")
        if record["mutation"] == "latest-receipt": raise repair.GenerationOutputsRepairError("fixture")
    def parent(_token, *, context):
        assert context == ANCESTRY[len(record["calls"]) - 1]
        record["calls"].append(context["repairId"])
        if record["mutation"] == context["repairId"]: raise repair.GenerationOutputsRepairError("fixture")
    monkeypatch.setattr(repair, "verify_parent_receipt", latest)
    monkeypatch.setattr(repair, "parent_completed", parent)
    return request, owner, old, store, record, root


def run(partial):
    request, owner, _, _, _, root = partial
    return repair.begin_recovery(owner, request, root, dict.fromkeys(SECRETS, "fixture-only"))


def test_latest_failure_resource_version_and_all_history_are_distinct_and_preserved(partial):
    request, owner, old, store, record, _ = partial
    original = dict(store.stages)
    permit, resume = run(partial)
    assert record["calls"] == ["latest-receipt", *[c["repairId"] for c in ANCESTRY], "d1", "github", "worker", "pages"]
    assert record["closed"] and len(store.writes) == 2 and len(store.stages) == 13
    assert all(store.stages[k] == v for k, v in original.items())
    assert state(store)["active"] is None and state(store)["writer"]["candidate"] == request.candidate
    assert all(json.loads(body)["writer"] is not None for body, _ in store.writes)
    resume.assert_permit(owner, permit)
    assert request.previous == PARENT and resume.parent_release == RESOURCE != PARENT
    marker = json.loads(store.stages[(permit.operation_id, "generation-outputs-recovery-transition")])
    assert marker == {"schemaVersion": 1, "context": CONTEXT, "ancestry": ANCESTRY,
        "taskId": request.task_id, "previousControlSha256": hashlib.sha256(old.state_bytes).hexdigest(),
        "previousControlEtag": old.etag, "previousOperationId": old.operation_id,
        "resourceRelease": RESOURCE, "candidate": request.candidate}
    with pytest.raises(CoordinationError): owner.assert_owned(old)
    before = (len(store.writes), len(store.stages))
    record["calls"].clear()
    with pytest.raises(ERRORS): run(partial)
    assert before == (len(store.writes), len(store.stages))


@pytest.mark.parametrize("mutation", ["latest-receipt", *[c["repairId"] for c in ANCESTRY], "d1", "github", "worker", "pages",
    "extra-object", "duplicate-object", "missing-object", "truncated", "etag"])
def test_any_parent_ancestry_resource_or_listing_drift_prevents_all_writes(partial, mutation):
    _, _, old, store, record, _ = partial
    record["mutation"] = mutation
    with pytest.raises(ERRORS): run(partial)
    assert state(store)["writer"]["operationId"] == old.operation_id
    assert len(store.writes) == 1 and len(store.stages) == 12


@pytest.mark.parametrize("index", range(12))
def test_each_historical_body_is_reconstructed_and_checked_not_just_counted(partial, index):
    store = partial[3]
    key = sorted(store.stages)[index]
    store.stages[key] = b" " + store.stages[key]
    with pytest.raises(ERRORS): run(partial)
    assert len(store.writes) == 1 and len(store.stages) == 12


@pytest.mark.parametrize("mutation", ["control", "canonical", "etag", "identity", "inventory"])
def test_control_and_identity_drift_cannot_be_recovered(partial, mutation, monkeypatch):
    _, owner, _, store, _, _ = partial
    if mutation in ("control", "canonical"): store.body = b" " + store.body
    if mutation == "canonical": monkeypatch.setattr(diagnostic, "CONTROL_SHA256", hashlib.sha256(store.body).hexdigest())
    if mutation == "etag": store.etag = '"' + "f" * 32 + '"'
    if mutation == "identity": owner.identity["environment"] = "different"
    if mutation == "inventory": owner.inventory_digest = "f" * 64
    with pytest.raises(ERRORS): run(partial)
    assert len(store.writes) == 1 and len(store.stages) == 12


@pytest.mark.parametrize("failure", ["marker-unknown", "marker-invalid", "cas-unknown", "cas-response-lost", "cas-conflict", "cas-invalid", "post-cas-read"])
def test_unknown_or_conflict_retains_everything_and_never_replays(partial, monkeypatch, failure):
    store = partial[3]
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
    with pytest.raises((*ERRORS, StoreUnknown)): run(partial)
    assert state(store)["active"] is None and state(store)["writer"] is not None and len(store.stages) == 13
    before = (len(store.writes), len(store.stages))
    partial[4]["calls"].clear()
    with pytest.raises((*ERRORS, StoreUnknown)): run(partial)
    assert before == (len(store.writes), len(store.stages))


@pytest.mark.parametrize("module", [pages_repair, assets_repair, generation_repair, diagnostic])
def test_old_entry_points_cannot_accept_new_context(partial, module):
    request, owner, _, store, record, root = partial
    with pytest.raises(RuntimeError): module.begin_recovery(owner, request, root, {})
    with pytest.raises(repair.GenerationOutputsRepairError):
        repair.begin_recovery(owner, replace(request, previous=RESOURCE), root, {})
    with pytest.raises(repair.GenerationOutputsRepairError):
        repair.begin_recovery(owner, replace(request, context=PAGES_DIAGNOSTIC_CONTEXT), root, {})
    with pytest.raises(repair.GenerationOutputsRepairError):
        repair.begin_recovery(owner, replace(request, context={**CONTEXT, "parentRunId": float(CONTEXT["parentRunId"])}), root, {})
    assert not record["calls"] and len(store.writes) == 1


def test_control_drift_after_evidence_prevents_the_single_cas(partial, monkeypatch):
    store = partial[3]
    create = store.create_stage
    def changed(*args):
        result = create(*args)
        store.etag = '"' + "f" * 32 + '"'
        return result
    monkeypatch.setattr(store, "create_stage", changed)
    with pytest.raises(ERRORS): run(partial)
    assert len(store.writes) == 1 and len(store.stages) == 13


def test_two_contenders_get_only_one_permit_and_cannot_duplicate_a_stage(partial, monkeypatch):
    _, owner, _, store, _, _ = partial
    for name in ("verify_parent_receipt", "parent_completed", "github_parent_completed"):
        monkeypatch.setattr(repair, name, lambda *_a, **_k: None)
    checked, objects = repair.checked_state, repair.checked_objects
    state_barrier, object_barrier = threading.Barrier(2), threading.Barrier(2)
    local = threading.local()
    def synchronized_state(current):
        result = checked(current)
        local.reads = getattr(local, "reads", 0) + 1
        if local.reads == 3: state_barrier.wait(timeout=5)
        return result
    def synchronized_objects(current):
        objects(current)
        local.objects = getattr(local, "objects", 0) + 1
        if local.objects == 2: object_barrier.wait(timeout=5)
    monkeypatch.setattr(repair, "checked_state", synchronized_state)
    monkeypatch.setattr(repair, "checked_objects", synchronized_objects)
    def attempt(_):
        try: return run(partial)
        except ERRORS: return None
    with ThreadPoolExecutor(max_workers=2) as pool: results = list(pool.map(attempt, range(2)))
    assert sum(row is not None for row in results) == 1 and len(store.writes) == 2
    permit, resume = next(row for row in results if row is not None)
    resume.assert_permit(owner, permit)
    def claim(_):
        try: owner.claim_stage(permit, "fixture-publication"); return True
        except CoordinationError: return False
    with ThreadPoolExecutor(max_workers=2) as pool: claims = list(pool.map(claim, range(2)))
    assert claims.count(True) == 1
