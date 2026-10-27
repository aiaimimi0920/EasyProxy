"""真实部署编排/Coordinator：D1、GitHub只读，Worker恢复，Pages首次，逐阶段停写。"""
from dataclasses import replace
import hashlib
from pathlib import Path
import sys

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import deploy
from fresh_domain_resume import FreshDomainResume
from repair_context import FRESH_DOMAIN_CONTEXT, FRESH_DOMAIN_PARENT_RELEASE
from test_aggregator_publish import BODIES
from test_coordination import state
from test_deploy import prepared, ORDER


@pytest.mark.parametrize("failure", [None, *ORDER])
def test_only_worker_receives_resume_and_no_completed_initialization_is_replayed(prepared, monkeypatch, failure):
    request, root, tools, image, sealer, output, credentials, store, record = prepared
    request = replace(request, application_version="1.0.13")
    recovery = replace(request, action="repair", previous=FRESH_DOMAIN_PARENT_RELEASE, context=FRESH_DOMAIN_CONTEXT)
    handles = []
    def begin(owner, current, *_):
        assert current is recovery
        permit = owner.begin("deploy", request.candidate)
        resume = FreshDomainResume(permit.operation_id, hashlib.sha256(permit.state_bytes).hexdigest(), request.config.workers_subdomain)
        handles.append(resume)
        return permit, resume
    monkeypatch.setattr(deploy, "begin_fresh_domain_recovery", begin)
    monkeypatch.setattr(deploy, "preflight_domains", lambda *_a, **_k: pytest.fail("must not require both domains absent"))
    monkeypatch.setattr(deploy, "initialize_d1", lambda *_a, **_k: pytest.fail("no DDL replay"))
    monkeypatch.setattr(deploy, "configure_environment", lambda *_a, **_k: pytest.fail("no GitHub writes"))
    def readonly(name, checks):
        def call(*_):
            record["order"].append(name)
            if failure == name: raise RuntimeError("PRIVATE_PROVIDER_RESPONSE")
            return {"checks": dict.fromkeys(checks, True), "refreshEnabled": True}
        return call
    monkeypatch.setattr(deploy, "verify_initialized", readonly("d1", ["d1-initialized", "schema-verified"]))
    monkeypatch.setattr(deploy, "verify_fresh_configured", readonly("github", ["github-environment-configured",
        "github-environment-verified", "github-workflow-verified"]))
    for symbol, resumed in (("publish_ech", True), ("publish_pages", False)):
        original = getattr(deploy, symbol)
        def stage(*args, _original=original, _resumed=resumed, **kwargs):
            assert (kwargs.pop("resume") is handles[0]) if _resumed else "resume" not in kwargs
            result = _original(*args, **kwargs)
            result.update(request.candidate)
            return result
        monkeypatch.setattr(deploy, symbol, stage)
    original_verify = deploy.verify_public
    def verify(config, release, source):
        assert release == request.candidate
        return original_verify(config, prepared[0].candidate, source)
    monkeypatch.setattr(deploy, "verify_public", verify)
    record["fail"] = failure
    if failure:
        with pytest.raises(RuntimeError, match="EASYPROXY_SBA_" + failure.upper() + "_UNKNOWN"):
            deploy.deploy_suite(request, root, tools, image, sealer, BODIES, output, credentials=credentials, recovery_request=recovery)
        assert state(store)["writer"] is not None and state(store)["active"] is None
        assert record["order"] == ORDER[:ORDER.index(failure) + 1]
    else:
        assert deploy.deploy_suite(request, root, tools, image, sealer, BODIES, output,
            credentials=credentials, recovery_request=recovery)["checks"]["service-ready"] is True
        assert state(store)["active"] == request.candidate and state(store)["writer"] is None
        assert record["order"] == ORDER
