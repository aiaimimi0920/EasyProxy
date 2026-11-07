"""前晋级失败可结束；晋级未知、并发归属和失败证据未知仍保持闭锁。"""
import json
from pathlib import Path
import sys

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from coordination import CoordinationError, StoreUnknown
from test_coordination import CHECKS, OLD, MemoryStore, manager, state


def started():
    store = MemoryStore()
    owner = manager(store)
    deployed = owner.begin("deploy", OLD)
    owner.commit_verified(deployed, CHECKS["deploy"])
    return store, owner, owner.begin("refresh", OLD, previous=OLD)


def test_failed_candidate_is_preserved_and_next_refresh_can_start():
    store, owner, permit = started()
    owner.claim_stage(permit, "aggregator-candidate")
    owner.claim_stage(permit, "aggregator-audit")
    before = dict(store.stages)
    owner.abort_unpublished_refresh(permit)
    assert owner.current_release() == OLD
    assert all(store.stages[key] == body for key, body in before.items())
    evidence = json.loads(store.stages[permit.operation_id, "refresh-aborted"])
    assert evidence["outcome"] == "failed-before-promotion" and "checks" not in evidence
    next_permit = owner.begin("refresh", OLD, previous=OLD)
    assert next_permit.operation_id != permit.operation_id
    with pytest.raises(CoordinationError, match="OWNERSHIP"):
        owner.abort_unpublished_refresh(permit)


@pytest.mark.parametrize("stage", ["aggregator-promotion", "misub-managed-sync", "unexpected-stage"])
def test_any_non_candidate_stage_prevents_abort(stage):
    store, owner, permit = started()
    owner.claim_stage(permit, stage)
    before = store.read()
    with pytest.raises(CoordinationError, match="SIDE_EFFECTS"):
        owner.abort_unpublished_refresh(permit)
    assert store.read() == before


def test_unknown_promotion_marker_is_also_not_safe_to_abort(monkeypatch):
    store, owner, permit = started()
    def unknown(*_args):
        raise StoreUnknown()
    monkeypatch.setattr(store, "create_stage", unknown)
    with pytest.raises(CoordinationError, match="UNKNOWN"):
        owner.claim_stage(permit, "aggregator-promotion")
    with pytest.raises(CoordinationError, match="SIDE_EFFECTS"):
        owner.abort_unpublished_refresh(permit)
    assert state(store)["writer"] is not None


def test_failure_evidence_unknown_keeps_writer(monkeypatch):
    store, owner, permit = started()
    def unknown(*_args):
        raise StoreUnknown()
    monkeypatch.setattr(store, "create_stage", unknown)
    with pytest.raises(CoordinationError, match="UNKNOWN"):
        owner.abort_unpublished_refresh(permit)
    assert state(store)["writer"]["operationId"] == permit.operation_id


def test_abort_cas_unknown_is_not_retried():
    store, owner, permit = started()
    store.ambiguous = True
    with pytest.raises(CoordinationError, match="UNKNOWN"):
        owner.abort_unpublished_refresh(permit)
    assert state(store)["writer"] is not None
    with pytest.raises(CoordinationError, match="STAGE_ALREADY_ATTEMPTED"):
        owner.abort_unpublished_refresh(permit)


def test_update_writer_cannot_use_refresh_abort():
    store = MemoryStore()
    owner = manager(store)
    permit = owner.begin("deploy", OLD)
    with pytest.raises(CoordinationError, match="OWNERSHIP"):
        owner.abort_unpublished_refresh(permit)
    assert state(store)["writer"]["action"] == "deploy"
