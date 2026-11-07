"""准确备份前 UNKNOWN：一次性 marker、单 CAS、旧 active/证据保留。"""
from dataclasses import replace
import hashlib
import io
import json
from pathlib import Path
import sys
from types import SimpleNamespace

import pytest
from botocore.exceptions import ClientError

sys.path.insert(0, str(Path(__file__).parents[1]))
import application
import migration_repair as repair
import fresh_pristine_config as binding
from coordination import Coordinator, CoordinationError, StoreUnknown, StoreConflict, _encode
from repair_context import MIGRATION_CONTEXT, MIGRATION_PARENT_RELEASE, MIGRATION_ACTIVE_RELEASE
from request import RequestError, SECRETS, parse_request
from suite_config import parse_configuration
from test_coordination import MemoryStore, state
from test_fresh_domain_request import recovery_request as old_request


@pytest.fixture
def approved(monkeypatch):
    value = old_request()
    value.update(applicationVersion="1.0.15", previous=dict(MIGRATION_PARENT_RELEASE), context=dict(MIGRATION_CONTEXT))
    config = parse_configuration(value["configuration"], repository=value["repository"], environment=value["environment"])
    raw = {"environment": config.environment, "configuration": config.configuration()}
    monkeypatch.setattr(binding, "CONFIGURATION_SHA256", hashlib.sha256(_encode(raw)).hexdigest())
    return value


def parse(value):
    return parse_request(json.dumps(value).encode(), application_version="1.0.15")


@pytest.mark.parametrize("field", list(MIGRATION_CONTEXT))
def test_every_parent_receipt_field_is_exact(approved, field):
    approved["context"][field] = 42 if field == "parentRunId" else "different"
    with pytest.raises(RequestError): parse(approved)


def test_exact_candidate_previous_configuration_and_task_are_bound(approved):
    assert parse(approved).context == MIGRATION_CONTEXT
    for patch in ({"previous": MIGRATION_ACTIVE_RELEASE}, {"taskId": MIGRATION_CONTEXT["parentTaskId"]}):
        with pytest.raises(RequestError): parse(dict(approved, **patch))
    approved["configuration"]["bucketName"] += "-other"
    with pytest.raises(RequestError): parse(approved)


@pytest.fixture
def partial(approved, monkeypatch):
    request = parse(approved); store = MemoryStore()
    store.account_id, store.bucket = request.config.account_id, request.config.bucket
    owner = Coordinator(store, request.config.identity("production"), request.config.inventory_digest)
    first = owner.begin("deploy", MIGRATION_ACTIVE_RELEASE)
    owner.commit_verified(first, {"suite-published": True, "service-ready": True})
    old = owner.begin("update", MIGRATION_PARENT_RELEASE, previous=MIGRATION_ACTIVE_RELEASE)
    monkeypatch.setattr(repair, "CONTROL_SHA256", hashlib.sha256(old.state_bytes).hexdigest())
    monkeypatch.setattr(repair, "CONTROL_ETAG", old.etag)
    monkeypatch.setattr(repair, "OPERATION", old.operation_id)
    monkeypatch.setattr(repair, "INVENTORY", request.config.inventory_digest)
    record = {"backup": False, "http": 404, "truncated": False, "extra": False, "calls": []}
    def head(**_):
        if record["backup"]: return {"ContentLength": 100}
        raise ClientError({"Error": {"Code": str(record["http"])}, "ResponseMetadata": {"HTTPStatusCode": record["http"]}}, "HeadObject")
    def listing(**kwargs):
        prefix = kwargs["Prefix"]
        rows = [{"Key": f"_sba/operations/{operation}/{stage}.json"} for operation, stage in store.stages if f"_sba/operations/{operation}/{stage}.json".startswith(prefix)]
        if record["extra"]: rows.append({"Key": prefix + "unexpected.json"})
        return {"IsTruncated": record["truncated"], "KeyCount": len(rows), "Contents": rows}
    def get(**kwargs):
        _, _, operation, name = kwargs["Key"].split("/")
        return {"Body": io.BytesIO(store.stages[(operation, name[:-5])])}
    store.client = SimpleNamespace(head_object=head, list_objects_v2=listing, get_object=get)
    for symbol in ("verify_parent", "verify_d1", "verify_published"):
        def verify(*_, symbol=symbol):
            record["calls"].append(symbol)
            if record.get("fail") == symbol: raise repair.MigrationRepairError("fixture")
        monkeypatch.setattr(repair, symbol, verify)
    return request, owner, old, store, record


def run(partial):
    request, owner, _, _, _ = partial
    return repair.begin_recovery(owner, request, Path("/fixture"), dict.fromkeys(SECRETS, "fixture"))


def test_single_transition_preserves_active_and_parent_receipt_then_returns_update_permit(partial):
    request, owner, old, store, _ = partial
    permit, resume = run(partial)
    assert resume is None and permit.action == "update" and permit.operation_id != old.operation_id
    owner.assert_owned(permit)
    assert state(store)["active"] == MIGRATION_ACTIVE_RELEASE and state(store)["writer"]["candidate"] == request.candidate
    evidence = json.loads(store.stages[(old.operation_id, repair.TRANSITION)])
    assert evidence["context"] == MIGRATION_CONTEXT and evidence["previousOperationId"] == old.operation_id
    assert len(store.writes) == 4 and len(store.stages) == 1
    with pytest.raises(repair.MigrationRepairError): run(partial)
    assert len(store.writes) == 4 and len(store.stages) == 1


@pytest.mark.parametrize("change", ["backup", "denied", "truncated", "extra", "verify_parent", "verify_d1", "verify_published", "etag"])
def test_all_precondition_failures_keep_old_writer_and_perform_zero_writes(partial, change):
    _, _, old, store, record = partial
    if change == "denied": record["http"] = 403
    elif change == "etag": store.etag = '"' + "f" * 32 + '"'
    elif change.startswith("verify_"): record["fail"] = change
    else: record[change] = True
    with pytest.raises(repair.MigrationRepairError): run(partial)
    assert state(store)["writer"]["operationId"] == old.operation_id and len(store.writes) == 3 and not store.stages


@pytest.mark.parametrize("failure", ["marker-lost", "cas-unknown", "cas-lost", "cas-conflict"])
def test_unknown_marker_or_cas_never_replays_or_clears_writer(partial, monkeypatch, failure):
    _, _, _, store, _ = partial
    if failure == "marker-lost":
        create = store.create_stage
        def lost(*args): create(*args); raise StoreUnknown()
        monkeypatch.setattr(store, "create_stage", lost)
    elif failure == "cas-unknown": store.ambiguous = True
    elif failure == "cas-lost": store.fail_after_commit = True
    else: monkeypatch.setattr(store, "compare_and_set", lambda *_: (_ for _ in ()).throw(StoreConflict()))
    with pytest.raises((StoreUnknown, StoreConflict, CoordinationError)): run(partial)
    assert state(store)["writer"] is not None and state(store)["active"] == MIGRATION_ACTIVE_RELEASE
    before = (len(store.writes), len(store.stages))
    with pytest.raises(repair.MigrationRepairError): run(partial)
    assert before == (len(store.writes), len(store.stages))


def test_application_routes_recovery_through_backup_migration_update_not_initialization(approved, tmp_path, monkeypatch):
    request = parse(approved); calls = []
    monkeypatch.setattr(application, "inspect_checkout", lambda *_: [])
    for name in ("prepare_linux_tools", "build_runtime", "build_image", "build_sealer", "build_aggregator", "build_cli"):
        monkeypatch.setattr(application, name, lambda *_args, name=name, **_kwargs: calls.append(name))
    migration = object()
    monkeypatch.setattr(application, "prepare_migration", lambda *_: migration)
    def deploy(current, *_, **kwargs):
        assert current == replace(request, action="update", previous=MIGRATION_ACTIVE_RELEASE, context=None)
        assert kwargs["recovery_request"] is request and kwargs["migration_settings"] is migration
        return {"checks": {"suite-published": True, "service-ready": True, "backup-created": True, "data-preserved": True}}
    monkeypatch.setattr(application, "deploy_suite", deploy)
    result = application.execute(request, tmp_path, tmp_path.parent / (tmp_path.name + "-out"), environment=dict.fromkeys(SECRETS, "fixture-value"))
    assert "build_cli" in calls and result["checks"]["repair-completed"]


def test_manifest_declares_only_exact_migration_error_and_existing_secrets():
    manifest = json.loads((Path(__file__).parents[1] / "tests/fixtures/manifest-1.0.15.json").read_text())
    row = next(row for row in manifest["repairs"] if row["id"] == MIGRATION_CONTEXT["repairId"])
    assert manifest["version"] == "1.0.15" and len(manifest["repairs"]) == 11
    assert row["fromErrorCodes"] == [MIGRATION_CONTEXT["errorCode"]] and set(row["secretNames"]) == SECRETS
