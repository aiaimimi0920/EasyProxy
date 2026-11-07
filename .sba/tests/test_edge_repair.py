"""准确发布基线 UNKNOWN：保留旧历史/备份，固定 marker 与单 CAS，拒绝任何漂移。"""
from dataclasses import replace
import hashlib
import io
import json
from pathlib import Path
import sys
from types import SimpleNamespace

import pytest

sys.path.insert(0, str(Path(__file__).parents[1]))
import application
import edge_repair as repair
import fresh_pristine_config as binding
from coordination import Coordinator, CoordinationError, StoreUnknown, StoreConflict, _encode
from repair_context import EDGE_CONTEXT, EDGE_PARENT_RELEASE, MIGRATION_ACTIVE_RELEASE
from request import RequestError, SECRETS, parse_request
from suite_config import parse_configuration
from test_coordination import MemoryStore, state
from test_fresh_domain_request import recovery_request as old_request


@pytest.fixture
def approved(monkeypatch):
    value = old_request()
    value.update(applicationVersion="1.0.17", previous=dict(EDGE_PARENT_RELEASE), context=dict(EDGE_CONTEXT))
    config = parse_configuration(value["configuration"], repository=value["repository"], environment=value["environment"])
    monkeypatch.setattr(binding, "CONFIGURATION_SHA256", hashlib.sha256(_encode({
        "environment": config.environment, "configuration": config.configuration()})).hexdigest())
    return value


def parse(value):
    return parse_request(json.dumps(value).encode(), application_version="1.0.17")


@pytest.mark.parametrize("field", list(EDGE_CONTEXT))
def test_every_parent_receipt_field_is_exact(approved, field):
    approved["context"][field] = 42 if field == "parentRunId" else "different"
    with pytest.raises(RequestError): parse(approved)


@pytest.fixture
def partial(approved, monkeypatch):
    request = parse(approved); store = MemoryStore()
    store.account_id, store.bucket = request.config.account_id, request.config.bucket
    owner = Coordinator(store, request.config.identity("production"), request.config.inventory_digest)
    first = owner.begin("deploy", MIGRATION_ACTIVE_RELEASE)
    owner.commit_verified(first, {"suite-published": True, "service-ready": True})
    old = owner.begin("update", EDGE_PARENT_RELEASE, previous=MIGRATION_ACTIVE_RELEASE)
    for name, value in {"CONTROL_SHA256": hashlib.sha256(old.state_bytes).hexdigest(), "CONTROL_ETAG": old.etag,
                        "OPERATION": old.operation_id, "INVENTORY": request.config.inventory_digest}.items():
        monkeypatch.setattr(repair, name, value)
    backup = b"age-encryption.org/v1\n".ljust(11875, b"x")
    monkeypatch.setattr(repair, "BACKUP_SHA256", hashlib.sha256(backup).hexdigest())
    for stage in repair.STAGES:
        store.create_stage(old.operation_id, stage, _encode({"operationId": old.operation_id,
            "stage": stage, "permitSha256": repair.CONTROL_SHA256}))
    record = {"truncated": False, "extra": False, "corrupt": False}
    def listing(**kwargs):
        prefix = kwargs["Prefix"]
        rows = [{"Key": f"_sba/operations/{operation}/{stage}.json"} for operation, stage in store.stages
                if f"_sba/operations/{operation}/{stage}.json".startswith(prefix)]
        if record["extra"]: rows.append({"Key": prefix + "unexpected.json"})
        return {"IsTruncated": record["truncated"], "KeyCount": len(rows), "Contents": rows}
    def get(**kwargs):
        if kwargs["Key"].endswith("/misub.age"):
            return {"Body": io.BytesIO(backup if not record["corrupt"] else b"wrong")}
        _, _, operation, name = kwargs["Key"].split("/")
        return {"Body": io.BytesIO(store.stages[(operation, name[:-5])])}
    store.client = SimpleNamespace(list_objects_v2=listing, get_object=get)
    for symbol in ("verify_parent", "verify_d1", "verify_published", "verify_provider_baseline"):
        def verify(*_, symbol=symbol):
            if record.get("fail") == symbol: raise repair.EdgeRepairError("fixture")
        monkeypatch.setattr(repair, symbol, verify)
    return request, owner, old, store, record


def run(partial):
    request, owner, _, _, _ = partial
    return repair.begin_recovery(owner, request, Path("/fixture"), dict.fromkeys(SECRETS, "fixture"))


def test_single_transition_preserves_active_backup_and_original_stages(partial):
    request, owner, old, store, _ = partial
    original = dict(store.stages)
    permit, resume = run(partial)
    assert type(resume) is repair.PublicationBaseline and permit.action == "update"
    resume.assert_permit(owner, permit)
    assert resume.release == {"sourceSha": "87f60967723e1fb11188ca375057dfaac52e93ab", "applicationVersion": "1.0.15"} and permit.operation_id != old.operation_id
    owner.assert_owned(permit)
    assert state(store)["active"] == MIGRATION_ACTIVE_RELEASE and state(store)["writer"]["candidate"] == request.candidate
    assert all(store.stages[key] == body for key, body in original.items())
    assert len(store.stages) == 2 and len(store.writes) == 4
    with pytest.raises(repair.EdgeRepairError): run(partial)
    assert len(store.stages) == 2 and len(store.writes) == 4


@pytest.mark.parametrize("change", ["corrupt", "truncated", "extra", "verify_parent", "verify_d1", "verify_published", "verify_provider_baseline", "etag", "publication-stage"])
def test_precondition_failures_perform_zero_writes_and_keep_parent(partial, change):
    _, _, old, store, record = partial
    if change == "etag": store.etag = '"' + "f" * 32 + '"'
    elif change == "publication-stage": store.create_stage(old.operation_id, "ech-publication", b"{}")
    elif change.startswith("verify_"): record["fail"] = change
    else: record[change] = True
    before = (len(store.writes), dict(store.stages))
    with pytest.raises(repair.EdgeRepairError): run(partial)
    assert state(store)["writer"]["operationId"] == old.operation_id
    assert before == (len(store.writes), store.stages)


@pytest.mark.parametrize("failure", ["marker-lost", "cas-unknown", "cas-lost", "cas-conflict"])
def test_unknown_writes_never_replay_or_clear_writer(partial, monkeypatch, failure):
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
    with pytest.raises(repair.EdgeRepairError): run(partial)
    assert before == (len(store.writes), len(store.stages))


def test_application_seeds_previous_inputs_and_routes_to_backed_up_update(approved, tmp_path, monkeypatch):
    request = parse(approved); calls = []
    monkeypatch.setattr(application, "inspect_checkout", lambda *_: [])
    for name in ("prepare_linux_tools", "build_runtime", "build_image", "build_sealer", "build_cli"):
        monkeypatch.setattr(application, name, lambda *_args, name=name, **_kwargs: calls.append(name))
    def previous(config, release, credentials):
        assert release == MIGRATION_ACTIVE_RELEASE
        calls.append("previous")
        return {"internal/crawledsubs.json": b"previous-sources", "subs/clash.yaml": b"previous-candidates"}
    monkeypatch.setattr(application, "load_previous_inputs", previous)
    def generate(*_, **kwargs):
        assert kwargs["previous_inputs"] == {"internal/crawledsubs.json": b"previous-sources", "subs/clash.yaml": b"previous-candidates"}
        calls.append("generation")
    monkeypatch.setattr(application, "build_aggregator", generate)
    migration = object()
    monkeypatch.setattr(application, "prepare_migration", lambda *_: migration)
    def deploy(current, *_, **kwargs):
        assert current == replace(request, action="update", previous=MIGRATION_ACTIVE_RELEASE, context=None)
        assert kwargs["recovery_request"] is request and kwargs["migration_settings"] is migration
        return {"checks": {"suite-published": True, "service-ready": True, "backup-created": True, "data-preserved": True}}
    monkeypatch.setattr(application, "deploy_suite", deploy)
    result = application.execute(request, tmp_path, tmp_path.parent / (tmp_path.name + "-out"), environment=dict.fromkeys(SECRETS, "fixture-value"))
    assert calls.index("previous") < calls.index("generation") and result["checks"]["repair-completed"]


def test_current_manifest_exposes_only_exact_edge_recovery():
    root = Path(__file__).parents[1]
    manifest = json.loads((root / 'tests/fixtures/manifest-1.0.17.json').read_bytes())
    assert manifest['version'] == '1.0.17'
    row, = manifest['repairs']
    assert row['id'] == EDGE_CONTEXT['repairId'] and row['fromErrorCodes'] == [EDGE_CONTEXT['errorCode']]
    assert row['fromActions'] == ['repair'] and set(row['secretNames']) == SECRETS
