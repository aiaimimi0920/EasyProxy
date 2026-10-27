"""准确1.0.7失败与仍为1.0.5的资源分离；旧六个入口不扩大。"""
from dataclasses import replace
import hashlib
import json
from pathlib import Path
import sys

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import application
from repair_context import PAGES_DIAGNOSTIC_PARENT_RELEASE as RESOURCE
from request import RequestError, SECRETS
from test_pristine_repair import request_fixture
from test_request import parse

PARENT = {"sourceSha": "89cb8f23394c988570141d0cc15105c86aa98248", "applicationVersion": "1.0.7"}
CONTEXT = {"repairId": "continue-generation-outputs-deployment",
    "parentTaskId": "dc-d8fbaaa6c7404904928900e9f6a8c9f1", "parentRunId": 40358960520,
    "requestDigest": "34d4657c1a29867fd8f0921e2498cc496e04927183dd08388afdc9fdaf80fb5f",
    "resultDigest": "99c35c8e9074de444ff6eb71dec0cd7e1b5c9ec40622317e3f51a17c18f8a4d8",
    "errorCode": "EASYPROXY_SBA_GENERATION_OUTPUTS_UNKNOWN"}


def recovery_request():
    value = request_fixture()
    value.update(previous=dict(PARENT), context=dict(CONTEXT))
    return value


def test_only_exact_new_parent_is_accepted_and_resources_are_not_the_previous_candidate():
    request = parse(recovery_request())
    assert request.context == CONTEXT and request.previous == PARENT != RESOURCE


@pytest.mark.parametrize("field", list(CONTEXT))
def test_every_parent_field_is_exact(field):
    value = recovery_request()
    value["context"][field] = 42 if field == "parentRunId" else "different"
    with pytest.raises(RequestError): parse(value)


@pytest.mark.parametrize("run", [True, float(CONTEXT["parentRunId"]), str(CONTEXT["parentRunId"])])
def test_run_id_has_exact_integer_type(run):
    value = recovery_request()
    value["context"]["parentRunId"] = run
    with pytest.raises(RequestError): parse(value)


@pytest.mark.parametrize("previous", [RESOURCE, {**PARENT, "sourceSha": "f" * 40}, {**PARENT, "applicationVersion": "1.0.5"}])
def test_failed_candidate_and_resource_version_cannot_be_confused(previous):
    value = recovery_request()
    value["previous"] = previous
    with pytest.raises(RequestError): parse(value)


def test_manifest_only_appends_one_exact_scope_and_keeps_all_old_scopes_and_secrets():
    root = Path(__file__).resolve().parents[2]
    manifest = json.loads((root / ".sba/manifest.json").read_bytes())
    assert manifest["version"] == "1.0.11" and len(manifest["repairs"]) == 8
    assert manifest["repairs"][-2]["id"] == CONTEXT["repairId"]
    assert manifest["repairs"][-2]["fromErrorCodes"] == [CONTEXT["errorCode"]]
    assert set(manifest["repairs"][-2]["secretNames"]) == SECRETS
    # Canonical old manifest from reviewed 7df6b7d; also works in shallow checkouts.
    old_shape = {**manifest, "version": "1.0.8", "repairs": manifest["repairs"][:-2]}
    raw = json.dumps(old_shape, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode()
    assert hashlib.sha256(raw).hexdigest() == "55b82f088c765627c0a9fcffc9b526d97b865b90fefc04424b92ebf778a59d2b"


def test_application_routes_new_recovery_without_touching_pristine(tmp_path, monkeypatch):
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
