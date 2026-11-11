"""已解除 writer 的准确 update 恢复：父回执、空闲现场和正常升级路径。"""
from dataclasses import replace
import hashlib
import io
import json
from unittest.mock import Mock

import pytest

import application
import fresh_pristine_config as binding
import writer_idle_repair as repair
from diagnostics import StageError
from repair_context import WRITER_IDLE_CONTEXT as CONTEXT, WRITER_IDLE_PARENT_RELEASE as PARENT
from repair_context import WRITER_IDLE_ACTIVE_RELEASE as ACTIVE
from request import RequestError, SECRETS, parse_request
from suite_config import parse_configuration
from test_fresh_pristine_request import recovery_request


def parse(value):
    return parse_request(json.dumps(value).encode(), application_version="1.0.23")


@pytest.fixture
def approved(monkeypatch):
    value = recovery_request()
    value.update(applicationVersion="1.0.23", previous=dict(PARENT), context=dict(CONTEXT))
    config = parse_configuration(value["configuration"], repository=value["repository"], environment=value["environment"])
    raw = {"environment": config.environment, "configuration": config.configuration()}
    monkeypatch.setattr(binding, "CONFIGURATION_SHA256", hashlib.sha256(json.dumps(raw,
        ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode()).hexdigest())
    return value


@pytest.mark.parametrize("field", list(CONTEXT))
def test_every_parent_field_is_bound(approved, field):
    approved["context"][field] = 42 if field == "parentRunId" else "different"
    with pytest.raises(RequestError): parse(approved)


@pytest.mark.parametrize("mutation", ["version", "previous", "same-task", "environment", "float-run", "boolean-run"])
def test_wrong_target_or_instance_is_refused(approved, mutation):
    if mutation == "version": approved["applicationVersion"] = "1.0.24"
    elif mutation == "previous": approved["previous"] = dict(ACTIVE)
    elif mutation == "same-task": approved["taskId"] = CONTEXT["parentTaskId"]
    elif mutation == "environment": approved["environment"] = "other-main"
    else: approved["context"]["parentRunId"] = True if mutation == "boolean-run" else float(CONTEXT["parentRunId"])
    with pytest.raises(RequestError): parse(approved)


@pytest.mark.parametrize("fail_at", [None, 1, 2])
def test_execute_runs_normal_update_with_registered_sources_and_migration(approved, monkeypatch, tmp_path, fail_at):
    request = parse(approved)
    calls = []
    monkeypatch.setattr(application, "inspect_checkout", lambda *_: None)
    def check(current, credentials):
        assert current is request and set(credentials) == SECRETS
        calls.append("preflight")
        if calls.count("preflight") == fail_at: raise RuntimeError("fixture-failure")
    monkeypatch.setattr(application, "assert_writer_idle", check)
    for name in ("prepare_linux_tools", "build_runtime", "build_image", "build_sealer", "build_cli"):
        monkeypatch.setattr(application, name, lambda *_a, **_k: object())
    def previous(config, release, values):
        assert release == ACTIVE
        calls.append("previous")
        return "previous-inputs"
    monkeypatch.setattr(application, "load_previous_inputs", previous)
    monkeypatch.setattr(application, "load_registered_subscriptions", lambda *_: ["metered-source"])
    def generation(*_a, **kwargs):
        assert kwargs["previous_inputs"] == "previous-inputs" and kwargs["registered_subscriptions"] == ["metered-source"]
        calls.append("generation")
    monkeypatch.setattr(application, "build_aggregator", generation)
    monkeypatch.setattr(application, "prepare_migration", lambda *_: "migration-settings")
    def deploy(current, *_a, **kwargs):
        assert current == replace(request, action="update", previous=dict(ACTIVE), context=None)
        assert kwargs["migration_settings"] == "migration-settings" and "recovery_request" not in kwargs
        calls.append("update")
        return {"checks": dict.fromkeys(("suite-published", "service-ready", "backup-created", "data-preserved"), True)}
    monkeypatch.setattr(application, "deploy_suite", deploy)
    # 与真实 runner 一样，输出必须位于源码目录之外。
    run = lambda: application.execute(request, tmp_path, tmp_path.parent / (tmp_path.name + "-output"), environment=dict.fromkeys(SECRETS, "fixture-only"))
    if fail_at:
        with pytest.raises(StageError, match="WRITER_UNKNOWN"): run()
        assert "update" not in calls
    else:
        assert run()["checks"]["repair-completed"] is True
        assert calls == ["preflight", "previous", "generation", "preflight", "update"]


@pytest.mark.parametrize("fault", [None, "parent", "busy", "active", "evidence", "artifacts", "drift"])
def test_read_only_preflight_fails_closed(approved, monkeypatch, fault):
    request = parse(approved)
    store = Mock()
    store.read.side_effect = [b"state", b"drift" if fault == "drift" else b"state"]
    store.client.get_object.return_value = {"Body": io.BytesIO(b"proof")}
    monkeypatch.setattr(repair, "RECOVERY_SHA256", hashlib.sha256(b"proof" if fault != "evidence" else b"different").hexdigest())
    parent = Mock(side_effect=RuntimeError("parent") if fault == "parent" else None)
    monkeypatch.setattr(repair, "verify_parent_receipt", parent)
    monkeypatch.setattr(repair, "r2_store", lambda *_: store)
    owner = Mock()
    if fault == "busy": owner.current_release.side_effect = RuntimeError("busy")
    else: owner.current_release.return_value = PARENT if fault == "active" else ACTIVE
    monkeypatch.setattr(repair, "Coordinator", lambda *_: owner)
    public = Mock()
    monkeypatch.setattr(repair, "PublicArtifacts", lambda *_a, **_k: public)
    monkeypatch.setattr(repair, "ArtifactStore", lambda *_: object())
    monkeypatch.setattr(repair, "_previous", lambda *_: (object(), {}, dict.fromkeys(range(6 if fault == "artifacts" else 7))))
    if fault:
        with pytest.raises(RuntimeError): repair.assert_writer_idle(request, dict.fromkeys(SECRETS, "fixture"))
    else: repair.assert_writer_idle(request, dict.fromkeys(SECRETS, "fixture"))
    store.compare_and_set.assert_not_called()
    store.create_stage.assert_not_called()
    store.client.put_object.assert_not_called()
    if fault != "parent":
        store.client.close.assert_called_once()
        public.close.assert_called_once()
