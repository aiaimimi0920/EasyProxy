"""准确1.0.9失败与资源父版本绑定；旧七个入口和十一秘密保持。"""
from dataclasses import replace
import hashlib
import json
from pathlib import Path

import pytest

import application
from repair_context import PAGES_ASSET_PROTOCOL_CONTEXT as CONTEXT, PAGES_ASSET_PROTOCOL_PARENT_RELEASE as PARENT
from repair_context import PAGES_DIAGNOSTIC_PARENT_RELEASE
from request import RequestError, SECRETS
from test_pristine_repair import request_fixture
from test_request import parse


def recovery_request():
    value = request_fixture()
    value.update(previous=dict(PARENT), context=dict(CONTEXT))
    return value


def test_only_exact_failed_parent_is_the_resource_version():
    request = parse(recovery_request())
    assert request.context == CONTEXT and request.previous == PARENT
    assert PARENT == {"sourceSha": "50d33cfed123439c77b05e89e059863185cd75b1", "applicationVersion": "1.0.9"}


@pytest.mark.parametrize("field", list(CONTEXT))
def test_every_parent_field_is_exact(field):
    value = recovery_request()
    value["context"][field] = 42 if field == "parentRunId" else "different"
    with pytest.raises(RequestError): parse(value)


@pytest.mark.parametrize("run", [True, float(CONTEXT["parentRunId"]), str(CONTEXT["parentRunId"])])
def test_run_id_has_exact_integer_type(run):
    value = recovery_request(); value["context"]["parentRunId"] = run
    with pytest.raises(RequestError): parse(value)


@pytest.mark.parametrize("previous", [PAGES_DIAGNOSTIC_PARENT_RELEASE,
    {**PARENT, "sourceSha": "f" * 40}, {**PARENT, "applicationVersion": "1.0.5"}])
def test_old_resource_version_and_unreviewed_parents_are_rejected(previous):
    value = recovery_request(); value["previous"] = previous
    with pytest.raises(RequestError): parse(value)


def test_manifest_keeps_all_seven_old_scopes_and_all_secrets():
    manifest = json.loads((Path(__file__).resolve().parents[1] / "manifest.json").read_bytes())
    assert manifest["version"] == "1.0.10" and len(manifest["repairs"]) == 8
    scope = manifest["repairs"][-1]
    assert scope["id"] == CONTEXT["repairId"] and scope["fromErrorCodes"] == [CONTEXT["errorCode"]]
    assert set(scope["secretNames"]) == SECRETS
    old = {**manifest, "version": "1.0.9", "repairs": manifest["repairs"][:-1]}
    assert hashlib.sha256(json.dumps(old, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode()).hexdigest() == "376198a879a1c1980e46a3e87da2ab56c0b1cabc1023507f8af66aee92f302a2"


def test_application_routes_exact_recovery_without_pristine(tmp_path, monkeypatch):
    request = parse(recovery_request())
    monkeypatch.setattr(application, "inspect_checkout", lambda *_: [])
    monkeypatch.setattr(application, "assert_pristine", lambda *_: pytest.fail("not pristine"))
    for name in ("prepare_linux_tools", "build_runtime", "build_image", "build_sealer", "build_aggregator"):
        monkeypatch.setattr(application, name, lambda *_a, **_k: None)
    def deploy(current, *_args, **kwargs):
        assert current == replace(request, action="deploy", previous=None, context=None)
        assert kwargs["recovery_request"] is request
        return {"checks": {"suite-published": True, "service-ready": True}}
    monkeypatch.setattr(application, "deploy_suite", deploy)
    assert application.execute(request, tmp_path, tmp_path.parent / (tmp_path.name + "-output"),
        environment=dict.fromkeys(SECRETS, "fixture-only"))["checks"]["repair-completed"] is True
