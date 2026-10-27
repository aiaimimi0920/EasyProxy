"""第十入口绑定准确新 repair UNKNOWN 与配置；九旧入口逐项保持。"""
from dataclasses import replace
import hashlib
import json
from pathlib import Path
import sys

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import application
import fresh_pristine_config as binding
from repair_context import FRESH_DOMAIN_CONTEXT as CONTEXT, FRESH_DOMAIN_PARENT_RELEASE as PARENT
from request import parse_request, RequestError, SECRETS
from suite_config import parse_configuration
from test_fresh_pristine_request import recovery_request as pristine_request


def recovery_request():
    value = pristine_request()
    value.update(applicationVersion="1.0.13", previous=dict(PARENT), context=dict(CONTEXT))
    return value


def parse(value):
    return parse_request(json.dumps(value).encode(), application_version="1.0.13")


@pytest.fixture
def approved(monkeypatch):
    value = recovery_request()
    config = parse_configuration(value["configuration"], repository=value["repository"], environment=value["environment"])
    raw = {"environment": config.environment, "configuration": config.configuration()}
    monkeypatch.setattr(binding, "CONFIGURATION_SHA256", hashlib.sha256(json.dumps(raw, ensure_ascii=False,
        sort_keys=True, separators=(",", ":")).encode()).hexdigest())
    return value


def test_parent_and_old_executor_are_fixed_not_the_new_platform_pin(approved):
    import fresh_domain_parent as parent
    request = parse(approved)
    assert request.previous == PARENT and request.context == CONTEXT
    assert parent.EXECUTOR_SHA == "35511c8b7740cae708abc7a2c3376453a9e39bac"
    assert parent.ARTIFACT_ID == 12722643150
    assert parent.ARTIFACT_SHA256 == "56aa7f5bc4cc16d8f882eeb428a3a9a9fc7d882c19afe4c3c9ef0b9ee4d97c52"


@pytest.mark.parametrize("field", list(CONTEXT))
def test_all_parent_context_fields_are_exact(approved, field):
    approved["context"][field] = 42 if field == "parentRunId" else "different"
    with pytest.raises(RequestError): parse(approved)


@pytest.mark.parametrize("field", ["accountId", "repository", "pagesName", "workerName", "workersSubdomain",
    "databaseId", "databaseName", "bucketName", "refreshTarget", "pagesOrigin", "workerOrigin"])
def test_complete_configuration_is_bound(approved, field):
    approved["configuration"][field] = "d" * 32 if field == "accountId" else "different"
    with pytest.raises(RequestError): parse(approved)


@pytest.mark.parametrize("field,value", [("parentRunId", True), ("parentRunId", float(CONTEXT["parentRunId"])),
    ("parentRunId", str(CONTEXT["parentRunId"]))])
def test_integer_types_cannot_be_coerced(approved, field, value):
    approved["context"][field] = value
    with pytest.raises(RequestError): parse(approved)


@pytest.mark.parametrize("mutation", ["previous-source", "previous-version", "same-task", "future-version"])
def test_same_receipt_cannot_be_retargeted_or_reused_in_future_versions(approved, mutation):
    if mutation == "previous-source": approved["previous"]["sourceSha"] = "f" * 40
    elif mutation == "previous-version": approved["previous"]["applicationVersion"] = "1.0.11"
    elif mutation == "same-task": approved["taskId"] = CONTEXT["parentTaskId"]
    else:
        approved["applicationVersion"] = "1.0.14"
        with pytest.raises(RequestError): parse_request(json.dumps(approved).encode(), application_version="1.0.14")
        return
    with pytest.raises(RequestError): parse(approved)


def test_production_configuration_digest_is_not_the_synthetic_fixture():
    assert binding.CONFIGURATION_SHA256 == "7dae01c58ecd80eaa34df6f3e2f5dbbb8e32ac3227f464ce348123dc709776a1"
    with pytest.raises(RequestError): parse(recovery_request())


def test_manifest_preserves_all_nine_prior_repairs_and_eleven_secrets():
    value = json.loads((Path(__file__).parents[1] / "manifest.json").read_bytes())
    assert value["version"] == "1.0.13" and len(value["repairs"]) == 10
    scope = value["repairs"][-1]
    assert scope["id"] == CONTEXT["repairId"] and scope["fromErrorCodes"] == [CONTEXT["errorCode"]]
    assert set(scope["secretNames"]) == SECRETS
    old = {**value, "version": "1.0.12", "repairs": value["repairs"][:9]}
    assert hashlib.sha256(json.dumps(old, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode()).hexdigest() == "a045ec3e91b87e6bbbb6134f873eba22c594b5924bacb50f3c31a10ce0a13985"


def test_application_routes_new_repair_without_reusing_pristine_or_old_pages(tmp_path, monkeypatch, approved):
    request = parse(approved)
    monkeypatch.setattr(application, "inspect_checkout", lambda *_: [])
    for name in ("assert_pristine", "assert_fresh_pristine"):
        monkeypatch.setattr(application, name, lambda *_: pytest.fail("not pristine"))
    for name in ("prepare_linux_tools", "build_runtime", "build_image", "build_sealer", "build_aggregator"):
        monkeypatch.setattr(application, name, lambda *_a, **_k: None)
    def publish(current, *_a, **kwargs):
        assert current == replace(request, action="deploy", previous=None, context=None)
        assert kwargs["recovery_request"] is request
        return {"checks": {"suite-published": True, "service-ready": True}}
    monkeypatch.setattr(application, "deploy_suite", publish)
    result = application.execute(request, tmp_path, tmp_path.parent / (tmp_path.name + "-output"),
        environment=dict.fromkeys(SECRETS, "fixture-only"))
    assert set(result["checks"]) == {"repair-completed", "data-preserved", "unchanged-resources-verified", "service-ready"}
