"""新实例准确原回执与完整配置绑定；八旧入口、十一秘密保持。"""
from dataclasses import replace
import hashlib
import json
from pathlib import Path
import sys

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import application
import fresh_pristine_config as binding
from diagnostics import StageError
from repair_context import FRESH_PRISTINE_CONTEXT as CONTEXT, FRESH_PRISTINE_PARENT_RELEASE as PARENT
from request import RequestError, SECRETS
from suite_config import parse_configuration, WORKFLOW
from test_pristine_repair import request_fixture
from test_request import parse


def recovery_request():
    value = request_fixture()
    value.update(environment="easyproxy-main", previous=dict(PARENT), context=dict(CONTEXT))
    value["configuration"].update(pagesName="ep-easyproxy-main-misub", workerName="ep-easyproxy-main-edge",
        databaseId="036bf79f-3077-483b-9119-49588e965f19", databaseName="ep-easyproxy-main-misub",
        bucketName="ep-easyproxy-main-store", refreshTarget=WORKFLOW + "@easyproxy-main",
        pagesOrigin="https://easyproxy.yamiyu.com", workerOrigin="https://easyproxy-edge.yamiyu.com")
    return value


@pytest.fixture
def approved(monkeypatch):
    value = recovery_request()
    config = parse_configuration(value["configuration"], repository=value["repository"], environment=value["environment"])
    raw = {"environment": config.environment, "configuration": config.configuration()}
    monkeypatch.setattr(binding, "CONFIGURATION_SHA256", hashlib.sha256(json.dumps(raw, ensure_ascii=False,
        sort_keys=True, separators=(",", ":")).encode()).hexdigest())
    return value


def test_real_binding_is_fixed_without_publishing_private_account():
    assert binding.CONFIGURATION_SHA256 == "7dae01c58ecd80eaa34df6f3e2f5dbbb8e32ac3227f464ce348123dc709776a1"
    assert binding.configuration_matches(None) is False
    with pytest.raises(RequestError): parse(recovery_request())


def test_only_exact_new_deploy_parent_is_accepted(approved):
    current = parse(approved)
    assert current.context == CONTEXT and current.previous == PARENT
    assert PARENT == {"sourceSha": "105e7128da842f738501c48866a5b6dfe597a827", "applicationVersion": "1.0.11"}


@pytest.mark.parametrize("field", list(CONTEXT))
def test_every_parent_field_is_exact(approved, field):
    approved["context"][field] = 42 if field == "parentRunId" else "different"
    with pytest.raises(RequestError): parse(approved)


@pytest.mark.parametrize("run", [True, float(CONTEXT["parentRunId"]), str(CONTEXT["parentRunId"])])
def test_run_id_has_exact_integer_type(approved, run):
    approved["context"]["parentRunId"] = run
    with pytest.raises(RequestError): parse(approved)


@pytest.mark.parametrize("field", ["sourceSha", "applicationVersion"])
def test_previous_is_exact(approved, field):
    approved["previous"][field] = "f" * 40 if field == "sourceSha" else "1.0.10"
    with pytest.raises(RequestError): parse(approved)


@pytest.mark.parametrize("field", ["accountId", "repository", "pagesName", "workerName", "workersSubdomain",
    "databaseId", "databaseName", "bucketName", "refreshTarget", "pagesOrigin", "workerOrigin"])
def test_every_configuration_field_is_bound(approved, field):
    original = approved["configuration"][field]
    approved["configuration"][field] = "d" * 32 if field == "accountId" else original.replace("easyproxy", "otherproxy")
    if approved["configuration"][field] == original:
        approved["configuration"][field] = "different"
    with pytest.raises(RequestError): parse(approved)


def test_even_another_well_formed_instance_with_same_parent_is_refused(approved):
    approved["environment"] = "other-main"
    for key, value in approved["configuration"].items():
        approved["configuration"][key] = value.replace("easyproxy-main", "other-main")
    with pytest.raises(RequestError): parse(approved)


def test_manifest_preserves_all_eight_old_repairs_and_all_other_contracts():
    manifest = json.loads((Path(__file__).resolve().parents[1] / "manifest.json").read_bytes())
    assert manifest["version"] == "1.0.12" and len(manifest["repairs"]) == 9
    scope = manifest["repairs"][-1]
    assert scope["id"] == CONTEXT["repairId"] and scope["fromErrorCodes"] == [CONTEXT["errorCode"]]
    assert set(scope["secretNames"]) == SECRETS
    old = {**manifest, "version": "1.0.11", "repairs": manifest["repairs"][:-1]}
    # Full reviewed previous manifest; no historical scope or secret is weakened.
    assert hashlib.sha256(json.dumps(old, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode()).hexdigest() == "3e0c5c4144a6e7df74188b7c9040e3eb270660d00775a4d0ea1e60f38f6213fc"


@pytest.mark.parametrize("fail_at", [None, 1, 2])
def test_application_checks_twice_and_never_uses_writer_handoff(approved, tmp_path, monkeypatch, fail_at):
    request = parse(approved)
    calls = []
    monkeypatch.setattr(application, "inspect_checkout", lambda *_: calls.append("source"))
    monkeypatch.setattr(application, "assert_pristine", lambda *_: pytest.fail("must not masquerade as old pristine"))
    def check(current, credentials):
        assert current is request and set(credentials) == SECRETS
        calls.append("pristine")
        if calls.count("pristine") == fail_at: raise RuntimeError("PRIVATE_PROVIDER_RESPONSE")
    monkeypatch.setattr(application, "assert_fresh_pristine", check)
    for name in ("prepare_linux_tools", "build_runtime", "build_image", "build_sealer", "build_aggregator"):
        monkeypatch.setattr(application, name, lambda *_a, _name=name, **_k: calls.append(_name))
    def publish(current, *_args, **kwargs):
        assert current == replace(request, action="deploy", previous=None, context=None)
        assert "recovery_request" not in kwargs and calls.count("pristine") == 2
        calls.append("publish")
        return {"checks": {"suite-published": True, "service-ready": True}}
    monkeypatch.setattr(application, "deploy_suite", publish)
    run = lambda: application.execute(request, tmp_path, tmp_path.parent / (tmp_path.name + "-output"),
        environment=dict.fromkeys(SECRETS, "fixture-only"))
    if fail_at is not None:
        with pytest.raises(StageError, match="EASYPROXY_SBA_FRESH_PRISTINE_UNKNOWN"): run()
        assert "publish" not in calls
        if fail_at == 1: assert calls == ["source", "pristine"]
    else:
        assert run()["checks"]["repair-completed"] is True
        assert calls == ["source", "pristine", "prepare_linux_tools", "build_runtime", "build_image",
            "build_sealer", "build_aggregator", "pristine", "publish"]
