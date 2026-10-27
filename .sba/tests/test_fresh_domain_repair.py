"""真实 Coordinator 的准确五对象/ETag 栅栏和单次非空移交反例。"""
import hashlib
import io
import json
from pathlib import Path
import sys

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import fresh_domain_repair as repair
from coordination import Coordinator, CoordinationError, STATE_KEY, StoreUnknown, StoreConflict
from fresh_domain_resume import FreshDomainError
from request import SECRETS
from test_coordination import MemoryStore, state
from test_fresh_domain_request import approved, parse


@pytest.fixture
def partial(approved, monkeypatch, tmp_path):
    request = parse(approved)
    store = MemoryStore()
    store.bucket = request.config.bucket
    owner = Coordinator(store, request.config.identity("production"), request.config.inventory_digest)
    old = owner.begin("deploy", repair.PARENT)
    monkeypatch.setattr(repair, "CONTROL_SHA256", hashlib.sha256(store.body).hexdigest())
    monkeypatch.setattr(repair, "CONTROL_ETAG", store.etag)
    monkeypatch.setattr(repair, "OPERATION", old.operation_id)
    stages = {}
    for key, (body, _etag, _digest) in repair.expected_objects().items():
        stage = key.rsplit("/", 1)[-1].removesuffix(".json")
        store.stages[(old.operation_id, stage)] = body
        stages[stage] = ('"' + hashlib.md5(body).hexdigest() + '"', hashlib.sha256(body).hexdigest())
    monkeypatch.setattr(repair, "STAGES", stages)
    record = {"mutation": None, "calls": [], "closed": 0}
    class Client:
        def list_objects_v2(self, **_k):
            rows = [{"Key": STATE_KEY, "ETag": store.etag, "Size": len(store.body)}]
            rows += [{"Key": f"_sba/operations/{op}/{stage}.json", "ETag": '"' + hashlib.md5(body).hexdigest() + '"',
                      "Size": len(body)} for (op, stage), body in store.stages.items()]
            mutation = record["mutation"]
            if mutation == "extra": rows.append({"Key": "extra"})
            elif mutation == "duplicate": rows[-1] = rows[-2]
            elif mutation == "list-etag": rows[-1]["ETag"] = '"' + "f" * 32 + '"'
            elif mutation == "size": rows[-1]["Size"] = True
            return {"Contents": rows, "IsTruncated": mutation == "truncated"}
        def get_object(self, Key, **_k):
            if Key == STATE_KEY: body, etag = store.body, store.etag
            else:
                op, stage = Key.split("/")[-2:]
                body = store.stages[(op, stage.removesuffix(".json"))]
                etag = '"' + hashlib.md5(body).hexdigest() + '"'
            return {"Body": io.BytesIO(body + b" " if record["mutation"] == "bytes" else body),
                    "ETag": '"' + "f" * 32 + '"' if record["mutation"] == "get-etag" else etag}
    store.client = Client()
    for symbol in ("verify_parent_receipt", "verify_initialized", "verify_configured", "verify_existing_worker", "verify_domains"):
        def check(*_a, _symbol=symbol):
            record["calls"].append(_symbol)
            if record["mutation"] == _symbol: raise FreshDomainError("fixture")
        monkeypatch.setattr(repair, symbol, check)
    class Pages:
        def __init__(self, *_): pass
        def project(self):
            if record["mutation"] == "control-etag": store.etag = '"' + "f" * 32 + '"'
            return {} if record["mutation"] == "pages" else None
        def close(self): record["closed"] += 1
    monkeypatch.setattr(repair, "PagesAPI", Pages)
    return request, owner, old, store, record, tmp_path


def run(partial):
    request, owner, _, _, _, root = partial
    return repair.begin_recovery(owner, request, root, dict.fromkeys(SECRETS, "fixture-only"))


def test_single_transfer_preserves_all_original_objects_and_invalidates_old_permit(partial):
    request, owner, old, store, record, _ = partial
    original = dict(store.stages)
    permit, resume = run(partial)
    assert len(store.writes) == 2 and len(store.stages) == 5 and record["closed"] == 1
    assert all(store.stages[key] == body for key, body in original.items())
    assert state(store)["active"] is None and state(store)["writer"] == {
        "operationId": permit.operation_id, "action": "deploy", "candidate": request.candidate}
    resume.assert_permit(owner, permit)
    with pytest.raises(CoordinationError): owner.assert_owned(old)
    with pytest.raises(FreshDomainError): run(partial)
    assert len(store.writes) == 2 and len(store.stages) == 5


@pytest.mark.parametrize("mutation", ["extra", "duplicate", "truncated", "bytes", "list-etag", "get-etag", "size",
    "control-etag", "pages", "verify_parent_receipt", "verify_initialized", "verify_configured", "verify_existing_worker", "verify_domains"])
def test_any_drift_stops_before_marker_or_cas(partial, mutation):
    _, _, old, store, record, _ = partial
    record["mutation"] = mutation
    with pytest.raises(FreshDomainError): run(partial)
    assert len(store.writes) == 1 and len(store.stages) == 4
    assert state(store)["writer"]["operationId"] == old.operation_id


@pytest.mark.parametrize("failure", ["marker-lost", "marker-bad-etag", "marker-wrong-body", "cas-unknown", "cas-lost", "cas-conflict", "after-cas-read"])
def test_ambiguous_handoff_never_replays_or_unlocks(partial, monkeypatch, failure):
    _, _, _, store, _, _ = partial
    create = store.create_stage
    if failure.startswith("marker"):
        def marker(*args):
            etag = create(*args)
            if failure == "marker-lost": raise StoreUnknown()
            if failure == "marker-wrong-body":
                store.stages[(args[0], args[1])] += b" "
                return etag
            return None
        monkeypatch.setattr(store, "create_stage", marker)
    elif failure == "cas-unknown": store.ambiguous = True
    elif failure == "cas-lost": store.fail_after_commit = True
    elif failure == "cas-conflict":
        monkeypatch.setattr(store, "compare_and_set", lambda *_: (_ for _ in ()).throw(StoreConflict()))
    else:
        original = store.compare_and_set
        def cas(*args):
            etag = original(*args)
            store.read_error = True
            return etag
        monkeypatch.setattr(store, "compare_and_set", cas)
    with pytest.raises((FreshDomainError, CoordinationError, StoreUnknown)): run(partial)
    assert json.loads(store.body)["writer"] is not None and json.loads(store.body)["active"] is None
    before = len(store.writes), len(store.stages)
    with pytest.raises((FreshDomainError, CoordinationError, StoreUnknown)): run(partial)
    assert before == (len(store.writes), len(store.stages))


def test_control_drift_after_marker_stops_before_cas(partial, monkeypatch):
    _, _, _, store, _, _ = partial
    original = store.create_stage
    def create(*args):
        result = original(*args)
        store.etag = '"' + "f" * 32 + '"'
        return result
    monkeypatch.setattr(store, "create_stage", create)
    with pytest.raises(FreshDomainError): run(partial)
    assert len(store.writes) == 1 and len(store.stages) == 5


def test_original_stage_bodies_and_hashes_are_not_a_synthetic_fixture():
    for body, _etag, digest in repair.expected_objects().values():
        assert hashlib.sha256(body).hexdigest() == digest
