"""真实Coordinator证明16原对象保持、非空writer单次移交及所有失败停写。"""
from concurrent.futures import ThreadPoolExecutor
from dataclasses import replace
import hashlib
import io
import json
import threading

import pytest

import assets_repair
import generation_repair
import generation_outputs_repair
import pages_diagnostic_repair
import pages_repair
import pages_asset_protocol_repair as repair
from coordination import Coordinator, CoordinationError, StoreConflict, StoreUnknown, STATE_KEY
from request import SECRETS
from test_coordination import MemoryStore, state
from test_pages_asset_protocol_request import CONTEXT, PARENT, recovery_request
from test_request import parse

ERRORS = (repair.PagesAssetProtocolRepairError, CoordinationError)


@pytest.fixture
def partial(tmp_path, monkeypatch):
    request = parse(recovery_request())
    store = MemoryStore(); store.bucket = request.config.bucket
    owner = Coordinator(store, request.config.identity("production"), request.config.inventory_digest)
    old = owner.begin("deploy", PARENT)
    monkeypatch.setattr(repair, "CONTROL_SHA256", hashlib.sha256(store.body).hexdigest())
    monkeypatch.setattr(repair, "CONTROL_ETAG", store.etag)
    monkeypatch.setattr(repair, "OPERATION", old.operation_id)
    for key, body in repair.expected_objects().items():
        op, stage = key.split("/")[-2:]; store.stages[(op, stage.removesuffix(".json"))] = body
    assert len(store.stages) == 15
    record = {"calls": [], "mutation": None, "closed": False}
    class Client:
        def list_objects_v2(self, **kwargs):
            keys = [STATE_KEY] + [f"_sba/operations/{op}/{stage}.json" for op, stage in store.stages]
            if record["mutation"] == "extra-object": keys.append("unexpected-object")
            if record["mutation"] == "duplicate-object": keys[-1] = keys[-2]
            if record["mutation"] == "missing-object": keys.pop()
            return {"IsTruncated": record["mutation"] == "truncated", "Contents": [{"Key": key} for key in keys]}
        def get_object(self, Key, **kwargs):
            op, stage = Key.split("/")[-2:]
            return {"Body": io.BytesIO(store.stages[(op, stage.removesuffix(".json"))])}
    store.client = Client()
    def check(name):
        record["calls"].append(name)
        if record["mutation"] == name: raise repair.PagesAssetProtocolRepairError("fixture")
    monkeypatch.setattr(repair, "verify_parent_receipt", lambda _: check("latest-receipt"))
    monkeypatch.setattr(repair, "verify_generation_parent_receipt", lambda _: check(repair.ANCESTRY[0]["repairId"]))
    monkeypatch.setattr(repair, "parent_completed", lambda _, context: check(context["repairId"]))
    monkeypatch.setattr(repair, "github_parent_completed", lambda _: check(repair.ANCESTRY[-1]["repairId"]))
    def readonly(name):
        def call(*args, **kwargs):
            assert kwargs == ({} if name == "d1" else {"parent_release": PARENT})
            check(name)
            if name == "pages" and record["mutation"] == "etag": store.etag = '"' + "f"*32 + '"'
        return call
    for name, symbol in (("d1", "verify_initialized"), ("github", "verify_configured"),
                         ("worker", "verify_existing_worker"), ("pages", "verify_existing_pages")):
        monkeypatch.setattr(repair, symbol, readonly(name))
    class Pages:
        def __init__(self, *args): pass
        def close(self): record["closed"] = True
    monkeypatch.setattr(repair, "PagesAPI", Pages)
    return request, owner, old, store, record, tmp_path


def run(partial):
    request, owner, _, _, _, root = partial
    return repair.begin_recovery(owner, request, root, dict.fromkeys(SECRETS, "fixture-only"))


def test_success_retains_all_fifteen_bodies_and_invalidates_old_permit(partial):
    request, owner, old, store, record, _ = partial
    original = dict(store.stages); permit, resume = run(partial)
    assert record["calls"] == ["latest-receipt", *[c["repairId"] for c in repair.ANCESTRY], "d1", "github", "worker", "pages"]
    assert record["closed"] and len(store.writes) == 2 and len(store.stages) == 16
    assert all(store.stages[key] == body for key, body in original.items())
    assert state(store)["active"] is None and state(store)["writer"]["candidate"] == request.candidate
    assert all(json.loads(body)["writer"] is not None for body, _ in store.writes)
    marker = json.loads(store.stages[(permit.operation_id, "pages-asset-protocol-recovery-transition")])
    assert marker == {"schemaVersion":1, "context":CONTEXT, "ancestry":repair.ANCESTRY, "taskId":request.task_id,
        "previousControlSha256":hashlib.sha256(old.state_bytes).hexdigest(), "previousControlEtag":old.etag,
        "previousOperationId":old.operation_id, "resourceRelease":PARENT, "candidate":request.candidate}
    assert resume.parent_release == PARENT; resume.assert_permit(owner, permit)
    with pytest.raises(CoordinationError): owner.assert_owned(old)
    before = len(store.writes), len(store.stages)
    with pytest.raises(ERRORS): run(partial)
    assert before == (len(store.writes),len(store.stages))


@pytest.mark.parametrize("mutation", ["latest-receipt", *[c["repairId"] for c in repair.ANCESTRY],
    "d1", "github", "worker", "pages", "extra-object", "duplicate-object", "missing-object", "truncated", "etag"])
def test_all_preflight_drift_stops_before_writing(partial, mutation):
    partial[4]["mutation"] = mutation
    with pytest.raises(ERRORS): run(partial)
    assert len(partial[3].writes) == 1 and len(partial[3].stages) == 15


@pytest.mark.parametrize("index", range(15))
def test_each_historical_body_is_reconstructed(partial, index):
    store = partial[3]; key = sorted(store.stages)[index]; store.stages[key] = b" " + store.stages[key]
    with pytest.raises(ERRORS): run(partial)
    assert len(store.writes) == 1


@pytest.mark.parametrize("mutation", ["bytes", "canonical", "etag", "identity", "inventory"])
def test_control_drift_stops_before_writing(partial, monkeypatch, mutation):
    _, owner, _, store, _, _ = partial
    if mutation in ("bytes", "canonical"): store.body = b" " + store.body
    if mutation == "canonical": monkeypatch.setattr(repair,"CONTROL_SHA256",hashlib.sha256(store.body).hexdigest())
    if mutation == "etag": store.etag = '"' + "f"*32 + '"'
    if mutation == "identity": owner.identity["environment"] = "different"
    if mutation == "inventory": owner.inventory_digest = "f"*64
    with pytest.raises(ERRORS): run(partial)
    assert len(store.writes) == 1


@pytest.mark.parametrize("failure", ["marker-unknown","marker-invalid","cas-unknown","cas-response-lost","cas-conflict","cas-invalid","post-cas-read"])
def test_ambiguous_write_keeps_evidence_and_never_replays(partial, monkeypatch, failure):
    store = partial[3]
    if failure.startswith("marker"):
        create = store.create_stage
        def lost(*args):
            create(*args)
            if failure == "marker-unknown": raise StoreUnknown()
            return None
        monkeypatch.setattr(store,"create_stage",lost)
    elif failure == "cas-unknown": store.ambiguous = True
    elif failure == "cas-response-lost": store.fail_after_commit = True
    elif failure == "cas-conflict": monkeypatch.setattr(store,"compare_and_set",lambda *_: (_ for _ in ()).throw(StoreConflict()))
    else:
        cas = store.compare_and_set
        def bad(*args):
            result = cas(*args)
            if failure == "post-cas-read": store.read_error = True
            return None if failure == "cas-invalid" else result
        monkeypatch.setattr(store,"compare_and_set",bad)
    with pytest.raises((*ERRORS,StoreUnknown)): run(partial)
    before = len(store.writes),len(store.stages)
    assert before[1] == 16 and state(store)["writer"] is not None and state(store)["active"] is None
    with pytest.raises((*ERRORS,StoreUnknown)): run(partial)
    assert before == (len(store.writes),len(store.stages))


@pytest.mark.parametrize("module",[pages_repair,assets_repair,generation_repair,pages_diagnostic_repair,generation_outputs_repair])
def test_old_entry_cannot_accept_new_context(partial,module):
    request,owner,_,store,record,root = partial
    with pytest.raises(RuntimeError): module.begin_recovery(owner,request,root,{})
    with pytest.raises(repair.PagesAssetProtocolRepairError):
        repair.begin_recovery(owner,replace(request,previous=pages_repair.PAGES_PARENT_RELEASE),root,{})
    assert not record["calls"] and len(store.writes) == 1


def test_drift_after_marker_prevents_cas(partial,monkeypatch):
    store=partial[3]; create=store.create_stage
    def drift(*args):
        value=create(*args); store.etag='"'+"f"*32+'"'; return value
    monkeypatch.setattr(store,"create_stage",drift)
    with pytest.raises(ERRORS): run(partial)
    assert len(store.writes)==1 and len(store.stages)==16


def test_concurrent_recovery_has_only_one_cas_winner(partial,monkeypatch):
    for symbol in ("verify_parent_receipt","verify_generation_parent_receipt","parent_completed","github_parent_completed"):
        monkeypatch.setattr(repair,symbol,lambda *_a,**_k: None)
    barrier, object_barrier = threading.Barrier(2),threading.Barrier(2)
    original,objects=repair.checked_state,repair.checked_objects; local=threading.local()
    def check(owner):
        value=original(owner); local.reads=getattr(local,"reads",0)+1
        if local.reads==3: barrier.wait(timeout=5)
        return value
    def check_objects(owner):
        objects(owner); local.objects=getattr(local,"objects",0)+1
        if local.objects==2: object_barrier.wait(timeout=5)
    monkeypatch.setattr(repair,"checked_state",check); monkeypatch.setattr(repair,"checked_objects",check_objects)
    def attempt(_):
        try: return run(partial)
        except ERRORS: return None
    with ThreadPoolExecutor(max_workers=2) as pool: results=list(pool.map(attempt,range(2)))
    assert sum(value is not None for value in results)==1 and len(partial[3].writes)==2
