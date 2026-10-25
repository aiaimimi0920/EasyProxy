"""同一 bucket 的发布/升级必须经过同一不可过期的 CAS 栅栏。"""
import hashlib
import importlib.util
import json
from pathlib import Path
import sys
import threading
from concurrent.futures import ThreadPoolExecutor

import pytest

spec = importlib.util.spec_from_file_location("coordination", Path(__file__).parents[1] / "coordination.py")
if spec.name not in sys.modules:
    coordination = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = coordination
    spec.loader.exec_module(coordination)
coordination = sys.modules[spec.name]

OLD = {"sourceSha": "a" * 40, "applicationVersion": "1.0.0"}
NEW = {"sourceSha": "b" * 40, "applicationVersion": "1.1.0"}
IDENTITY = {"repository": "fixture/easyproxy", "environment": "testing", "accountId": "c" * 32,
            "bucket": "easyproxy-testing", "purpose": "production"}
INVENTORY = "d" * 64
CHECKS = {
    "deploy": {"suite-published": True, "service-ready": True},
    "update": {"suite-published": True, "backup-created": True, "data-preserved": True, "service-ready": True},
    "refresh": {"candidate-verified": True, "stable-verified": True, "misub-refreshed": True},
    "preview": {"suite-published": True, "snapshot-copied": True, "migration-verified": True,
                "source-unchanged": True, "side-effects-isolated": True, "service-ready": True},
    "destroy-preview": {"preview-removed": True, "source-unchanged": True},
}


class MemoryStore:
    def __init__(self):
        self.account_id = IDENTITY["accountId"]
        self.bucket = IDENTITY["bucket"]
        self.body = self.etag = None
        self.other_objects = False
        self.mutex = threading.Lock()
        self.writes = []
        self.read_error = False
        self.ambiguous = False
        self.fail_after_commit = False
        self.stages = {}

    def read(self):
        with self.mutex:
            if self.read_error:
                raise coordination.StoreUnknown()
            return (self.body, self.etag) if self.body is not None else None

    def is_empty(self):
        return self.body is None and not self.other_objects

    def compare_and_set(self, body, etag):
        with self.mutex:
            if self.ambiguous:
                raise coordination.StoreUnknown()
            if etag != self.etag:
                raise coordination.StoreConflict()
            self.body = body
            self.etag = '"' + hashlib.md5(body).hexdigest() + '"'
            self.writes.append((body, etag))
            if self.fail_after_commit:
                raise coordination.StoreUnknown()
            return self.etag

    def create_stage(self, operation_id, stage, body):
        with self.mutex:
            key = (operation_id, stage)
            if key in self.stages:
                raise coordination.StoreConflict()
            self.stages[key] = body
            return '"' + hashlib.md5(body).hexdigest() + '"'


def manager(store, identity=None, inventory=INVENTORY):
    return coordination.Coordinator(store, identity or IDENTITY, inventory)


def deployed(store, identity=None):
    owner = manager(store, identity)
    permit = owner.begin("preview" if (identity or IDENTITY)["purpose"] == "preview" else "deploy", OLD)
    owner.commit_verified(permit, CHECKS[permit.action])
    return owner


def state(store):
    return json.loads(store.body)


def test_initial_deployment_commits_active_version_only_after_verified_completion():
    store = MemoryStore()
    owner = manager(store)
    permit = owner.begin("deploy", OLD)
    assert state(store)["active"] is None
    assert state(store)["writer"]["candidate"] == OLD
    with pytest.raises(coordination.CoordinationError, match="BUSY"):
        manager(store).begin("deploy", OLD)
    owner.commit_verified(permit, CHECKS["deploy"])
    assert state(store)["active"] == OLD
    assert state(store)["writer"] is None
    assert len(store.writes) == 2


def test_update_blocks_refresh_and_activates_new_sha_with_same_inventory():
    store = MemoryStore()
    owner = deployed(store)
    permit = owner.begin("update", NEW, previous=OLD)
    with pytest.raises(coordination.CoordinationError, match="BUSY"):
        manager(store).begin("refresh", OLD, previous=OLD)
    assert state(store)["active"] == OLD
    owner.commit_verified(permit, CHECKS["update"])
    with pytest.raises(coordination.CoordinationError, match="VERSION"):
        owner.begin("refresh", OLD, previous=OLD)
    refreshed = owner.begin("refresh", NEW, previous=NEW)
    owner.commit_verified(refreshed, CHECKS["refresh"])
    assert state(store)["active"] == NEW


def test_concurrent_updates_and_refresh_have_exactly_one_winner():
    store = MemoryStore()
    deployed(store)
    barrier = threading.Barrier(24)

    def attempt(index):
        barrier.wait(timeout=10)
        try:
            action = "refresh" if index % 2 else "update"
            return manager(store).begin(action, OLD if action == "refresh" else NEW, previous=OLD)
        except coordination.CoordinationError:
            return None

    with ThreadPoolExecutor(max_workers=24) as pool:
        results = list(pool.map(attempt, range(24)))
    assert sum(result is not None for result in results) == 1
    assert len(store.writes) == 3


@pytest.mark.parametrize("mode", ["not-committed", "committed-response-lost"])
def test_ambiguous_acquisition_never_retries_or_starts_business_writes(mode):
    store = MemoryStore()
    deployed(store)
    store.ambiguous = mode == "not-committed"
    store.fail_after_commit = mode == "committed-response-lost"
    with pytest.raises(coordination.CoordinationError, match="UNKNOWN"):
        manager(store).begin("refresh", OLD, previous=OLD)
    assert len(store.writes) == (3 if mode == "committed-response-lost" else 2)
    if mode == "committed-response-lost":
        store.fail_after_commit = False
        with pytest.raises(coordination.CoordinationError, match="BUSY"):
            manager(store).begin("update", NEW, previous=OLD)


def test_failed_or_missing_completion_checks_leave_writer_held_without_ttl():
    store = MemoryStore()
    owner = deployed(store)
    permit = owner.begin("update", NEW, previous=OLD)
    for checks in ({}, {**CHECKS["update"], "backup-created": False}, {**CHECKS["update"], "data-preserved": 1}):
        with pytest.raises(coordination.CoordinationError, match="CHECKS"):
            owner.commit_verified(permit, checks)
    assert state(store)["writer"] is not None
    assert state(store)["active"] == OLD
    assert not any("expire" in key.lower() or "lease" in key.lower() for key in state(store)["writer"])
    assert len(store.writes) == 3


def test_stale_handle_cannot_release_another_writer_or_reuse_a_completed_operation():
    store = MemoryStore()
    owner = deployed(store)
    first = owner.begin("refresh", OLD, previous=OLD)
    owner.commit_verified(first, CHECKS["refresh"])
    second = owner.begin("refresh", OLD, previous=OLD)
    assert first.operation_id != second.operation_id
    with pytest.raises(coordination.CoordinationError, match="OWNERSHIP"):
        owner.commit_verified(first, CHECKS["refresh"])
    owner.commit_verified(second, CHECKS["refresh"])
    with pytest.raises(coordination.CoordinationError, match="OWNERSHIP"):
        owner.commit_verified(second, CHECKS["refresh"])


def test_restart_retains_writer_and_forbids_forced_takeover():
    store = MemoryStore()
    owner = deployed(store)
    owner.begin("refresh", OLD, previous=OLD)
    restarted = manager(store)
    with pytest.raises(coordination.CoordinationError, match="BUSY"):
        restarted.begin("update", NEW, previous=OLD)
    assert len(store.writes) == 3


@pytest.mark.parametrize("change", ["repository", "environment", "accountId", "bucket", "purpose", "inventory"])
def test_identity_or_inventory_drift_is_rejected_without_writes(change):
    store = MemoryStore()
    deployed(store)
    identity = {**IDENTITY}
    inventory = INVENTORY
    if change == "inventory":
        inventory = "e" * 64
    else:
        identity[change] = {"repository": "other/repo", "environment": "other", "accountId": "e" * 32,
                            "bucket": "other-bucket", "purpose": "preview"}[change]
    with pytest.raises(coordination.CoordinationError, match="IDENTITY"):
        manager(store, identity, inventory).begin("refresh", OLD, previous=OLD)
    assert len(store.writes) == 2


@pytest.mark.parametrize("action,candidate,previous", [
    ("deploy", NEW, None), ("update", OLD, OLD), ("update", {**NEW, "sourceSha": OLD["sourceSha"]}, OLD),
    ("update", {**NEW, "applicationVersion": "0.9.0"}, OLD), ("update", NEW, NEW),
    ("refresh", NEW, OLD), ("refresh", OLD, None), ("destroy-preview", OLD, OLD),
])
def test_invalid_transition_is_rejected_without_mutation(action, candidate, previous):
    store = MemoryStore()
    owner = deployed(store)
    with pytest.raises(coordination.CoordinationError):
        owner.begin(action, candidate, previous=previous)
    assert len(store.writes) == 2


def test_cannot_adopt_legacy_or_nonempty_bucket():
    store = MemoryStore()
    store.other_objects = True
    with pytest.raises(coordination.CoordinationError, match="UNOWNED_BUCKET"):
        manager(store).begin("deploy", OLD)
    assert store.writes == []


def test_missing_state_does_not_authorize_refresh_or_update():
    for action in ("update", "refresh", "destroy-preview"):
        store = MemoryStore()
        with pytest.raises(coordination.CoordinationError):
            manager(store).begin(action, NEW, previous=OLD)
        assert store.writes == []


def test_preview_retirement_is_terminal_and_does_not_delete_coordination_tombstone():
    store = MemoryStore()
    identity = {**IDENTITY, "purpose": "preview"}
    owner = deployed(store, identity)
    with pytest.raises(coordination.CoordinationError, match="SIDE_EFFECTS"):
        owner.begin("refresh", OLD, previous=OLD)
    permit = owner.begin("destroy-preview", OLD, previous=OLD)
    owner.commit_verified(permit, CHECKS["destroy-preview"])
    assert state(store)["retired"] is True
    assert store.body is not None
    for action in ("preview", "deploy", "refresh", "destroy-preview"):
        with pytest.raises(coordination.CoordinationError, match="RETIRED"):
            owner.begin(action, OLD, previous=OLD)


@pytest.mark.parametrize("mutation", ["truncated", "oversized", "unexpected", "boolean-schema", "active-version", "duplicate-key"])
def test_corrupt_state_fails_closed_and_is_not_reset(mutation):
    store = MemoryStore()
    deployed(store)
    value = state(store)
    if mutation == "truncated":
        store.body = b'{"schemaVersion":'
    elif mutation == "oversized":
        store.body = b" " * 16385
    elif mutation == "duplicate-key":
        store.body = store.body.replace(b'"schemaVersion":1', b'"schemaVersion":1,"schemaVersion":1')
    else:
        if mutation == "unexpected":
            value["privateSecret"] = "must not be accepted"
        elif mutation == "boolean-schema":
            value["schemaVersion"] = True
        else:
            value["active"]["applicationVersion"] = "01.0.0"
        store.body = json.dumps(value).encode()
    with pytest.raises(coordination.CoordinationError, match="STATE"):
        manager(store).begin("refresh", OLD, previous=OLD)
    assert len(store.writes) == 2


def test_unknown_release_retains_evidence_and_does_not_replay_write():
    store = MemoryStore()
    owner = deployed(store)
    permit = owner.begin("update", NEW, previous=OLD)
    store.fail_after_commit = True
    with pytest.raises(coordination.CoordinationError, match="UNKNOWN"):
        owner.commit_verified(permit, CHECKS["update"])
    assert len(store.writes) == 4
    assert state(store)["active"] == NEW
    assert state(store)["writer"] is None
    store.fail_after_commit = False
    with pytest.raises(coordination.CoordinationError, match="OWNERSHIP"):
        owner.commit_verified(permit, CHECKS["update"])
    assert len(store.writes) == 4


def test_store_read_failure_has_no_writes_or_secret_error_text():
    store = MemoryStore()
    store.read_error = True
    with pytest.raises(coordination.CoordinationError) as error:
        manager(store).begin("deploy", OLD)
    assert str(error.value) == "EASYPROXY_SBA_COORDINATION_UNKNOWN"
    assert store.writes == []
