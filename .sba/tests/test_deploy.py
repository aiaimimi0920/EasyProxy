"""首次编排合同测试：阶段用替身，持久 writer/commit 使用真实 Coordinator。"""
from dataclasses import replace
from pathlib import Path
import sys
from types import SimpleNamespace

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import deploy as orchestration
from aggregator_publish import Candidate
from audit_image import AuditImage
from request import SECRETS
from sealed_box import Sealer
from toolchain import LinuxTools
from test_aggregator_publish import BODIES
from test_coordination import MemoryStore, state
from test_request import fixture, parse


ORDER = ["d1", "github", "edge", "pages", "candidate", "audit", "promotion", "sync", "verify"]


@pytest.fixture
def prepared(tmp_path, monkeypatch):
    request = parse(fixture())
    root = tmp_path / "root"
    root.mkdir()
    tools = LinuxTools(tmp_path, tmp_path / "node", tmp_path / "npm", tmp_path / "esbuild", tmp_path / "go")
    image = AuditImage("sha256:" + "f" * 64, request.config.repository, request.source_sha, "d" * 64)
    sealer = Sealer(tmp_path / "sealer", request.source_sha, "e" * 64)
    credentials = {name: "fixture-value" for name in SECRETS}
    output = tmp_path / "output"
    store = MemoryStore()
    store.bucket = request.config.bucket
    record = {"order": [], "fail": None, "bad": None, "storeClosed": False, "publicClosed": False, "clients": 0}
    store.client = SimpleNamespace(close=lambda: record.update(storeClosed=True))
    monkeypatch.setattr(orchestration, "inspect_checkout", lambda *_: [])
    monkeypatch.setattr(orchestration, "reviewed_workflow", lambda *_: b"fixture workflow")

    def r2(account, bucket, access, secret):
        assert account == request.config.account_id and bucket == request.config.bucket
        assert access == secret == "fixture-value"
        record["clients"] += 1
        return store

    monkeypatch.setattr(orchestration, "r2_store", r2)
    monkeypatch.setattr(orchestration, "ArtifactStore", lambda current: SimpleNamespace(store=current))

    class Public:
        def __init__(self, origin):
            assert origin == request.config.worker_origin + "/aggregator"

        def close(self):
            record["publicClosed"] = True

    monkeypatch.setattr(orchestration, "PublicArtifacts", Public)

    def call(name, checks, extra=None):
        def stage(owner, permit, *_args, **_kwargs):
            owner.assert_owned(permit)
            owner.claim_stage(permit, "fixture-" + ("database" if name == "d1" else name))
            record["owner"], record["permit"] = owner, permit
            record["order"].append(name)
            if record["fail"] == name:
                raise RuntimeError("FIXTURE_UNKNOWN")
            result = {"checks": dict.fromkeys(checks, True), **(extra or {})}
            if name == "promotion":
                result.update(runId=permit.operation_id, manifestSha256="f" * 64, counts={"nodes": 1, "sources": 1})
            if record["bad"] == name:
                result["checks"][next(iter(checks))] = False
            return result
        return stage

    monkeypatch.setattr(orchestration, "initialize_d1", call("d1", ["d1-initialized", "schema-verified"]))
    monkeypatch.setattr(orchestration, "configure_environment", call("github",
        ["github-environment-configured", "github-environment-verified", "github-workflow-verified"], {"refreshEnabled": True}))
    monkeypatch.setattr(orchestration, "publish_ech", call("edge", ["ech-published", "ech-bindings-verified", "ech-version-verified"],
        {"origin": request.config.worker_origin, **request.candidate}))
    monkeypatch.setattr(orchestration, "publish_pages", call("pages", ["pages-published", "pages-bindings-verified", "pages-version-verified"],
        {"origin": request.config.pages_origin, **request.candidate}))

    def candidate(owner, permit, *_args):
        call("candidate", [])(owner, permit)
        return Candidate(permit.operation_id, dict(BODIES))

    monkeypatch.setattr(orchestration, "publish_candidate", candidate)
    monkeypatch.setattr(orchestration, "audit_candidate", call("audit", []))
    monkeypatch.setattr(orchestration, "promote_candidate", call("promotion", ["candidate-verified", "stable-verified"]))
    monkeypatch.setattr(orchestration, "sync_misub", call("sync", ["misub-refreshed"]))

    def verify(config, release, source):
        owner, permit = record["owner"], record["permit"]
        assert config == request.config and release == request.candidate and source == root
        assert state(store)["active"] is None and state(store)["writer"] is not None
        result = call("verify", ["release-versions-verified", "published-subscriptions-verified", "pages-frontend-verified"])(owner, permit)
        result.update(runId=permit.operation_id, manifestSha256="f" * 64, counts={"nodes": 1, "sources": 1})
        if record.get("manifestDrift"):
            result["manifestSha256"] = "e" * 64
        return result

    monkeypatch.setattr(orchestration, "verify_public", verify)
    return request, root, tools, image, sealer, output, credentials, store, record


def run(prepared):
    request, root, tools, image, sealer, output, credentials, _, _ = prepared
    return orchestration.deploy_suite(request, root, tools, image, sealer, BODIES, output, credentials=credentials)


def test_single_process_deploy_commits_only_after_every_real_stage_contract_passes(prepared):
    result = run(prepared)
    request, _, _, _, _, _, _, store, record = prepared
    assert record["order"] == ORDER
    assert result == {"checks": {"suite-published": True, "service-ready": True}}
    assert state(store)["active"] == request.candidate and state(store)["writer"] is None
    assert len(store.writes) == 2 and record["storeClosed"] and record["publicClosed"]


def test_attested_recovery_never_initializes_d1_again_and_commits_only_after_remaining_checks(prepared, monkeypatch):
    request, root, tools, image, sealer, output, credentials, store, record = prepared
    repair_request = replace(request, action="repair", previous={"sourceSha": "a" * 40, "applicationVersion": "1.0.0"}, context={})
    def begin(owner, current, source, values):
        assert current is repair_request and source == root and values is credentials
        return owner.begin("deploy", request.candidate), SimpleNamespace()
    def verified(config, source, token):
        assert config is request.config and source == root and token == "fixture-value"
        record["order"].append("d1-readonly")
        return {"checks": {"d1-initialized": True, "schema-verified": True}}
    monkeypatch.setattr(orchestration, "begin_recovery", begin)
    monkeypatch.setattr(orchestration, "verify_initialized", verified)
    monkeypatch.setattr(orchestration, "initialize_d1", lambda *_args, **_kwargs: pytest.fail("DDL must not be replayed"))
    result = orchestration.deploy_suite(request, root, tools, image, sealer, BODIES, output,
                                        credentials=credentials, recovery_request=repair_request)
    assert record["order"] == ["d1-readonly", *ORDER[1:]]
    assert result["checks"]["service-ready"] is True
    assert state(store)["active"] == request.candidate and state(store)["writer"] is None


@pytest.mark.parametrize("failure", [None, "edge", "pages", "sync"])
def test_pages_recovery_does_not_replay_d1_or_github_and_passes_resume_to_publication(prepared, monkeypatch, failure):
    from pages_repair import PagesResume
    from repair_context import PAGES_CONTEXT, PAGES_PARENT_RELEASE
    import hashlib
    request, root, tools, image, sealer, output, credentials, store, record = prepared
    recovery = replace(request, action="repair", previous=PAGES_PARENT_RELEASE, context=PAGES_CONTEXT)
    handles = []
    def begin(owner, current, source, values):
        assert current is recovery and source == root and values is credentials
        permit = owner.begin("deploy", request.candidate)
        resume = PagesResume(permit.operation_id, hashlib.sha256(permit.state_bytes).hexdigest())
        handles.append(resume)
        return permit, resume
    monkeypatch.setattr(orchestration, "begin_pages_recovery", begin)
    monkeypatch.setattr(orchestration, "initialize_d1", lambda *_a, **_k: pytest.fail("no DDL"))
    monkeypatch.setattr(orchestration, "configure_environment", lambda *_a, **_k: pytest.fail("no GitHub write"))
    def d1(*_):
        record["order"].append("d1-readonly")
        return {"checks": {"d1-initialized": True, "schema-verified": True}}
    def github(*_):
        record["order"].append("github-readonly")
        return {"checks": dict.fromkeys(("github-environment-configured", "github-environment-verified", "github-workflow-verified"), True),
                "refreshEnabled": True}
    monkeypatch.setattr(orchestration, "verify_initialized", d1)
    monkeypatch.setattr(orchestration, "verify_configured", github)
    for symbol in ("publish_ech", "publish_pages"):
        original = getattr(orchestration, symbol)
        def publish(*args, _original=original, **kwargs):
            assert kwargs.pop("resume") is handles[0]
            return _original(*args, **kwargs)
        monkeypatch.setattr(orchestration, symbol, publish)
    record["fail"] = failure
    if failure:
        with pytest.raises(RuntimeError, match="EASYPROXY_SBA_" + failure.upper() + "_UNKNOWN"):
            orchestration.deploy_suite(request, root, tools, image, sealer, BODIES, output,
                                        credentials=credentials, recovery_request=recovery)
        assert state(store)["writer"] is not None and state(store)["active"] is None
        assert record["order"] == ["d1-readonly", "github-readonly", *ORDER[2:ORDER.index(failure) + 1]]
    else:
        assert orchestration.deploy_suite(request, root, tools, image, sealer, BODIES, output,
                    credentials=credentials, recovery_request=recovery)["checks"]["service-ready"] is True
        assert state(store)["active"] == request.candidate and state(store)["writer"] is None
        assert record["order"] == ["d1-readonly", "github-readonly", *ORDER[2:]]


@pytest.mark.parametrize("stage", ORDER)
def test_each_unknown_stage_keeps_writer_and_never_runs_later_stages(prepared, stage):
    record, store = prepared[-1], prepared[-2]
    record["fail"] = stage
    with pytest.raises(RuntimeError, match="EASYPROXY_SBA_" + stage.upper() + "_UNKNOWN"):
        run(prepared)
    assert record["order"] == ORDER[:ORDER.index(stage) + 1]
    assert state(store)["writer"] is not None and state(store)["active"] is None and len(store.writes) == 1
    assert record["storeClosed"] and record["publicClosed"]
    with pytest.raises(orchestration.DeploymentError):
        run(prepared)  # Even the failed output directory cannot silently be reused.
    assert record["clients"] == 1 and len(store.writes) == 1


@pytest.mark.parametrize("stage", ["d1", "github", "edge", "pages", "promotion", "sync", "verify"])
def test_a_stage_cannot_claim_success_with_false_checks(prepared, stage):
    prepared[-1]["bad"] = stage
    with pytest.raises(orchestration.DeploymentError):
        run(prepared)
    assert state(prepared[-2])["writer"] is not None and state(prepared[-2])["active"] is None


def test_final_independent_readback_is_bound_to_the_exact_promoted_manifest(prepared):
    prepared[-1]["manifestDrift"] = True
    with pytest.raises(orchestration.DeploymentError):
        run(prepared)
    assert state(prepared[-2])["writer"] is not None


@pytest.mark.parametrize("mutation", ["action", "source", "secret", "unknown-secret", "relative-output"])
def test_invalid_local_inputs_fail_before_cloud_client_or_writer(prepared, mutation):
    values = list(prepared)
    if mutation == "action":
        values[0] = replace(values[0], action="verify")
    elif mutation == "source":
        values[3] = replace(values[3], source_sha="a" * 40)
    elif mutation == "secret":
        values[6]["EASYPROXY_ECH_TOKEN"] = "verification-not-applicable"
    elif mutation == "unknown-secret":
        values[6]["GITHUB_TOKEN"] = "must-not-enter"
    else:
        values[5] = Path("relative-output")
    with pytest.raises(orchestration.DeploymentError):
        run(values)
    assert prepared[-1]["clients"] == 0 and not prepared[-2].writes
