"""最新失败回执与原Pages祖先分离；十对象保留、单次移交及不重放。"""
from dataclasses import replace
import hashlib
import json
from pathlib import Path
import sys

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import application
import assets_repair
import generation_repair as repair
from coordination import CoordinationError, StoreConflict, StoreUnknown
from repair_context import ASSETS_CONTEXT, ASSETS_PARENT_RELEASE, GENERATION_CONTEXT, GENERATION_PARENT_RELEASE
from request import RequestError, SECRETS
from test_assets_repair import partial as assets_partial
from test_coordination import state
from test_pristine_repair import request_fixture
from test_request import parse


def recovery_request():
    value = request_fixture()
    value.update(previous=dict(GENERATION_PARENT_RELEASE), context=dict(GENERATION_CONTEXT))
    return value


@pytest.mark.parametrize("field", list(GENERATION_CONTEXT))
def test_every_latest_parent_receipt_field_is_exact(field):
    value = recovery_request()
    value["context"][field] = 42 if field == "parentRunId" else "different"
    with pytest.raises(RequestError):
        parse(value)


def test_latest_failed_version_cannot_be_replaced_by_actual_resource_version():
    value = recovery_request()
    value["previous"] = dict(ASSETS_PARENT_RELEASE)
    with pytest.raises(RequestError):
        parse(value)


@pytest.fixture
def partial(assets_partial, monkeypatch):
    _, owner, old, store, record, root = assets_partial
    request = parse(recovery_request())
    monkeypatch.setattr(repair, "CONTROL_SHA256", assets_repair.CONTROL_SHA256)
    monkeypatch.setattr(repair, "OPERATION", old.operation_id)
    for symbol in ("verify_initialized", "verify_configured", "verify_existing_worker", "verify_existing_pages", "PagesAPI"):
        monkeypatch.setattr(repair, symbol, getattr(assets_repair, symbol))
    def parent(_token, *, context):
        expected = GENERATION_CONTEXT if not record["calls"] else ASSETS_CONTEXT
        assert context == expected
        name = "generation-parent" if context == GENERATION_CONTEXT else "pages-parent"
        record["calls"].append(name)
        if record["mutation"] == name:
            raise repair.GenerationRepairError("fixture")
    monkeypatch.setattr(repair, "parent_completed", parent)
    return request, owner, old, store, record, root


def run(partial):
    request, owner, _, _, _, root = partial
    return repair.begin_recovery(owner, request, root, dict.fromkeys(SECRETS, "fixture-only"))


def test_preserves_original_objects_and_records_both_latest_failure_and_ancestry(partial):
    request, owner, old, store, record, _ = partial
    previous = dict(store.stages)
    permit, resume = run(partial)
    assert record["calls"] == ["generation-parent", "pages-parent", "d1", "github", "worker", "pages"]
    assert record["closed"] and len(store.writes) == 2 and len(store.stages) == 10
    assert all(store.stages[k] == v for k, v in previous.items())
    assert state(store)["active"] is None
    assert state(store)["writer"] == {"operationId": permit.operation_id, "action": "deploy", "candidate": request.candidate}
    resume.assert_permit(owner, permit)
    assert resume.parent_release == ASSETS_PARENT_RELEASE != request.previous
    transition = json.loads(store.stages[(permit.operation_id, "generation-recovery-transition")])
    assert transition == {"schemaVersion": 1, "context": GENERATION_CONTEXT, "ancestry": ASSETS_CONTEXT,
        "taskId": request.task_id, "previousControlSha256": hashlib.sha256(old.state_bytes).hexdigest(),
        "previousOperationId": old.operation_id, "resourceRelease": ASSETS_PARENT_RELEASE, "candidate": request.candidate}
    with pytest.raises(CoordinationError):
        owner.assert_owned(old)


@pytest.mark.parametrize("mutation", ["generation-parent", "pages-parent", "d1", "github", "worker", "pages",
    "extra-object", "duplicate-object", "missing-object", "truncated", "stage-bytes", "etag"])
def test_any_latest_parent_ancestry_resource_or_object_drift_prevents_writes(partial, mutation):
    _, _, old, store, record, _ = partial
    record["mutation"] = mutation
    with pytest.raises((repair.GenerationRepairError, assets_repair.AssetsRepairError)):
        run(partial)
    assert state(store)["writer"]["operationId"] == old.operation_id
    assert len(store.writes) == 1 and len(store.stages) == 9


@pytest.mark.parametrize("failure", ["marker-unknown", "cas-unknown", "cas-response-lost", "cas-conflict"])
def test_ambiguous_write_keeps_nonempty_writer_all_evidence_and_no_replay(partial, monkeypatch, failure):
    store = partial[3]
    if failure == "marker-unknown":
        create = store.create_stage
        def lost(*args):
            create(*args)
            raise StoreUnknown()
        monkeypatch.setattr(store, "create_stage", lost)
    elif failure == "cas-unknown":
        store.ambiguous = True
    elif failure == "cas-response-lost":
        store.fail_after_commit = True
    else:
        monkeypatch.setattr(store, "compare_and_set", lambda *_: (_ for _ in ()).throw(StoreConflict()))
    with pytest.raises((StoreUnknown, CoordinationError)):
        run(partial)
    assert state(store)["active"] is None and state(store)["writer"] is not None and len(store.stages) == 10
    before = (len(store.writes), len(store.stages))
    partial[4]["calls"].clear()
    with pytest.raises((repair.GenerationRepairError, assets_repair.AssetsRepairError, CoordinationError)):
        run(partial)
    assert before == (len(store.writes), len(store.stages))


def test_success_cannot_be_repeated_and_wrong_context_never_observes_resources(partial):
    request, owner, _, store, record, root = partial
    with pytest.raises(repair.GenerationRepairError):
        repair.begin_recovery(owner, replace(request, context=ASSETS_CONTEXT), root, {})
    assert not record["calls"] and len(store.writes) == 1
    run(partial)
    before = (len(store.writes), len(store.stages))
    record["calls"].clear()
    with pytest.raises(assets_repair.AssetsRepairError):
        run(partial)
    assert before == (len(store.writes), len(store.stages))


def test_manifest_has_five_separate_scopes_and_no_additional_secrets():
    manifest = json.loads((Path(__file__).resolve().parents[1] / "manifest.json").read_bytes())
    assert manifest["version"] == "1.0.11" and len(manifest["repairs"]) == 8
    scope = next(row for row in manifest["repairs"] if row["id"] == GENERATION_CONTEXT["repairId"])
    assert scope["fromErrorCodes"] == [GENERATION_CONTEXT["errorCode"]] and set(scope["secretNames"]) == SECRETS


def test_application_only_passes_the_exact_latest_recovery_request(tmp_path, monkeypatch):
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
