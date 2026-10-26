"""恢复的 provider 读取合同：严格归属、仅 GET、固定公开模块摘要。"""
import copy
import hashlib
import json
from pathlib import Path
import sys
from types import SimpleNamespace

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import pages_repair as repair
import github_environment
from refresh_registry import REGISTRY_VARIABLE
from test_pages_repair import recovery_request
from test_request import parse
from repair_context import ASSETS_PARENT_RELEASE, PAGES_DIAGNOSTIC_PARENT_RELEASE, PAGES_ASSET_PROTOCOL_PARENT_RELEASE


@pytest.mark.parametrize("mutation", [None, "repository", "workflow", "environment", "variables", "secrets", "registry", "policy"])
@pytest.mark.parametrize("parent", [repair.PAGES_PARENT_RELEASE, ASSETS_PARENT_RELEASE, PAGES_DIAGNOSTIC_PARENT_RELEASE, PAGES_ASSET_PROTOCOL_PARENT_RELEASE])
def test_complete_github_is_only_read_and_any_drift_stops(monkeypatch, tmp_path, mutation, parent):
    config = parse(recovery_request()).config
    owner = SimpleNamespace(identity=config.identity("production"), inventory_digest=config.inventory_digest)
    expected = github_environment.variables(owner, config, 123, repair.ENVIRONMENT_ID)
    calls = []
    monkeypatch.setattr(github_environment, "reviewed_workflow", lambda _: b"approved-workflow")
    class API:
        def __init__(self, *_): pass
        def close(self): calls.append("closed")
        def repository_info(self): return {"id": 123, "defaultBranch": "other" if mutation == "repository" else "main"}
        def workflow_at(self, ref):
            calls.append(ref)
            return b"changed" if mutation == "workflow" else b"approved-workflow"
        def active_workflow(self): calls.append("active-workflow")
        def environment_info(self):
            return {"id": 0 if mutation == "environment" else repair.ENVIRONMENT_ID,
                    "deployment_branch_policy": {"protected_branches": False, "custom_branch_policies": True},
                    "protection_rules": [{"type": "branch_policy"}]}
        def policies(self): return [{"id": 1, "name": "other" if mutation == "policy" else "main", "type": "branch"}]
        def variables(self): return {} if mutation == "variables" else expected
        def secret_names(self): return set() if mutation == "secrets" else github_environment.REFRESH_SECRETS
        def repository_variable(self, name):
            assert name == REGISTRY_VARIABLE
            return json.dumps({"schemaVersion": 1, "repository": config.repository,
                               "environments": [] if mutation == "registry" else [config.environment]})
        # No mutation methods exist. A hidden write must fail this test.
    monkeypatch.setattr(repair, "GitHubAPI", API)
    if mutation:
        with pytest.raises(RuntimeError):
            repair.verify_configured(owner, config, tmp_path, "fixture-only", parent_release=parent)
    else:
        assert repair.verify_configured(owner, config, tmp_path, "fixture-only", parent_release=parent)["refreshEnabled"] is True
        assert parent["sourceSha"] in calls
    assert calls[-1] == "closed"


@pytest.mark.parametrize("mutation", [None, "status", "wrong-name", "extra-part", "boundary", "missing-ending"])
def test_worker_module_download_is_bounded_get_and_strict_single_module(mutation):
    bundle = b"export default {};\n"
    boundary = b"a" * 32 if mutation != "boundary" else b"invalid boundary"
    name = b"edge.mjs" if mutation != "wrong-name" else b"other.mjs"
    part = b'Content-Disposition: form-data; name="' + name + b'"\r\nContent-Type: text/plain\r\n\r\n' + bundle
    raw = b"--" + boundary + b"\r\n" + part + b"\r\n"
    if mutation == "extra-part": raw += b"--" + boundary + b"\r\n" + part + b"\r\n"
    if mutation != "missing-ending": raw += b"--" + boundary + b"--\r\n"
    calls = []
    class Response:
        status_code = 307 if mutation == "status" else 200
        def __enter__(self): return self
        def __exit__(self, *_): pass
        def iter_content(self, _):
            for index in range(0, len(raw), 17): yield raw[index:index + 17]
    class Session:
        def get(self, url, **kwargs):
            assert url == "https://api.cloudflare.com/client/v4/accounts/" + "c" * 32 + "/workers/scripts/fixture-worker"
            assert kwargs["allow_redirects"] is False and kwargs["stream"] is True
            calls.append("GET")
            return Response()
    api = SimpleNamespace(account_id="c" * 32, _session=Session(), _token="fixture-only", _name=lambda value: value)
    if mutation:
        with pytest.raises(repair.PagesRepairError): repair.worker_bundle_digest(api, "fixture-worker")
    else:
        assert repair.worker_bundle_digest(api, "fixture-worker") == hashlib.sha256(bundle).hexdigest()
    assert calls == ["GET"]


@pytest.mark.parametrize("mutation", [None, "subdomain", "settings", "bundle", "public-version"])
@pytest.mark.parametrize("parent", [repair.PAGES_PARENT_RELEASE, ASSETS_PARENT_RELEASE, PAGES_DIAGNOSTIC_PARENT_RELEASE, PAGES_ASSET_PROTOCOL_PARENT_RELEASE])
def test_existing_worker_requires_settings_public_version_and_exact_module(monkeypatch, mutation, parent):
    from ech import metadata
    config = parse(recovery_request()).config
    owner = SimpleNamespace(identity=config.identity("production"), inventory_digest=config.inventory_digest)
    expected = metadata(owner, parent, "fixture-token")
    calls = []
    class API:
        def __init__(self, *_): pass
        def close(self): calls.append("closed")
        def account_subdomain(self): return "other" if mutation == "subdomain" else config.workers_subdomain
        def settings(self, _):
            value = copy.deepcopy(expected)
            if mutation == "settings": value["bindings"][0]["bucket_name"] = "other"
            return value
        def script_subdomain(self, _): calls.append("subdomain-read")
    monkeypatch.setattr(repair, "WorkerAPI", API)
    monkeypatch.setattr(repair, "worker_bundle_digest", lambda *_: "f" * 64 if mutation == "bundle" else repair.WORKER_BUNDLE_SHA256)
    monkeypatch.setattr(repair, "public_release", lambda _: {"schemaVersion": 1, "applicationId": "easyproxy-cloud",
        **parent, **({"sourceSha": "f" * 40} if mutation == "public-version" else {})})
    if mutation:
        with pytest.raises(RuntimeError): repair.verify_existing_worker(config, owner, "fixture-api", "fixture-token", parent_release=parent)
    else:
        repair.verify_existing_worker(config, owner, "fixture-api", "fixture-token", parent_release=parent)
    assert calls[-1] == "closed"
