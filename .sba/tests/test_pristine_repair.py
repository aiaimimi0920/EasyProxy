"""修复只接受原固定版本和全资源 pristine；不清理、复用或重放任何旧 writer。"""
from dataclasses import replace
import json
from pathlib import Path
import sys
from types import SimpleNamespace

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import application
import pristine_repair as repair
from diagnostics import StageError
from repair_context import PARENT_ERROR, PARENT_RELEASE, REPAIR_ID
from request import RequestError, SECRETS
from test_request import fixture, parse


def request_fixture():
    value = fixture("update")
    value.update(action="repair", repository="aiaimimi0920/EasyProxy", previous=dict(PARENT_RELEASE))
    value["configuration"]["repository"] = value["repository"]
    value["context"] = {"repairId": REPAIR_ID, "parentTaskId": "dc-" + "2" * 32, "parentRunId": 42,
                        "requestDigest": "d" * 64, "resultDigest": "e" * 64, "errorCode": PARENT_ERROR}
    return value


@pytest.mark.parametrize("mutation", [
    lambda v: v["context"].update(repairId="other"), lambda v: v["context"].update(errorCode="OTHER_UNKNOWN"),
    lambda v: v["context"].update(parentRunId=True), lambda v: v["context"].update(parentRunId=0),
    lambda v: v["context"].update(parentTaskId=v["taskId"]), lambda v: v["context"].update(parentTaskId="wrong"),
    lambda v: v["context"].update(requestDigest="bad"), lambda v: v["context"].update(resultDigest="bad"),
    lambda v: v["context"].update(secrets={}), lambda v: v["previous"].update(sourceSha="f" * 40),
    lambda v: v["previous"].update(applicationVersion="0.9.0"), lambda v: v.update(sourceSha=PARENT_RELEASE["sourceSha"]),
    lambda v: v.pop("context"), lambda v: v.update(repository="fixture/easyproxy"),
])
def test_repair_context_is_exact_and_bound_to_the_original_fixed_release(mutation):
    value = request_fixture()
    mutation(value)
    with pytest.raises(RequestError):
        parse(value)


@pytest.fixture
def pristine(tmp_path, monkeypatch):
    request = parse(request_fixture())
    credentials = dict.fromkeys(SECRETS, "fixture-only")
    record = {"calls": [], "closed": [], "mutation": None}
    config = request.config

    def observed(name, result):
        record["calls"].append(name)
        if record["mutation"] == name + "-error":
            raise OSError("DO_NOT_PRINT_PRIVATE_PROVIDER_RESPONSE")
        return result

    class Store:
        client = SimpleNamespace(close=lambda: record["closed"].append("r2"))
        def read(self):
            return observed("r2", (b"existing-control", '"' + "a" * 32 + '"') if record["mutation"] == "control" else None)
        def is_empty(self):
            return observed("bucket", record["mutation"] != "bucket-data")

    class Database:
        def __init__(self, *_): pass
        def close(self): record["closed"].append("database")
        def identity(self):
            return observed("database", {"uuid": "wrong" if record["mutation"] == "d1-id" else config.database_id,
                                         "name": "wrong" if record["mutation"] == "d1-name" else config.database_name})
        def query(self, sql):
            assert sql == repair.SCHEMA_QUERY, "the precondition must never execute DDL or DML"
            return [{"type": "table", "name": "private", "tbl_name": "private", "sql": "CREATE TABLE private (id)"}] if record["mutation"] == "d1-schema" else []

    class Worker:
        def __init__(self, *_): pass
        def close(self): record["closed"].append("worker")
        def settings_or_absent(self, name):
            assert name == config.worker_name
            return observed("worker", {} if record["mutation"] == "worker" else None)
        def account_subdomain(self):
            return "wrong" if record["mutation"] == "subdomain" else config.workers_subdomain

    class Pages:
        def __init__(self, *_): pass
        def close(self): record["closed"].append("pages")
        def project(self): return observed("pages", {} if record["mutation"] == "pages" else None)

    class Github:
        def __init__(self, *_): pass
        def close(self): record["closed"].append("github")
        def repository_info(self): return observed("github", {"id": 1})
        def environment_info(self): return {} if record["mutation"] == "environment" else None

    monkeypatch.setattr(repair, "r2_store", lambda *_: Store())
    for name, value in (("D1API", Database), ("WorkerAPI", Worker), ("PagesAPI", Pages), ("GitHubAPI", Github)):
        monkeypatch.setattr(repair, name, value)
    return request, credentials, record


def test_all_pristine_preconditions_are_read_only_and_every_client_is_closed(pristine):
    request, credentials, record = pristine
    repair.assert_pristine(request, credentials)
    assert record["calls"] == ["r2", "bucket", "database", "worker", "pages", "github"]
    assert record["closed"] == ["r2", "database", "worker", "pages", "github"]


@pytest.mark.parametrize("mutation", ["control", "bucket-data", "d1-id", "d1-name", "d1-schema", "worker",
                                    "subdomain", "pages", "environment", "r2-error", "database-error",
                                    "worker-error", "pages-error", "github-error"])
def test_existing_unknown_or_inaccessible_resources_refuse_repair_without_writes(pristine, mutation):
    request, credentials, record = pristine
    record["mutation"] = mutation
    with pytest.raises((repair.PristineError, OSError)):
        repair.assert_pristine(request, credentials)
    assert record["closed"]
    if mutation in ("control", "bucket-data", "r2-error"):
        assert record["closed"] == ["r2"]


def test_repair_keeps_platform_identity_but_creates_a_new_candidate_only_after_pristine_check(tmp_path, monkeypatch):
    request = parse(request_fixture())
    calls = []
    monkeypatch.setattr(application, "inspect_checkout", lambda *_: calls.append("source"))
    monkeypatch.setattr(application, "assert_pristine", lambda current, _: calls.append("pristine"))
    for name in ("prepare_linux_tools", "build_runtime", "build_image", "build_sealer", "build_aggregator"):
        monkeypatch.setattr(application, name, lambda *_args, _name=name, **_kwargs: calls.append(_name))

    def publish(current, *_args, **_kwargs):
        assert current == replace(request, action="deploy", previous=None, context=None)
        assert current.config is request.config and current.candidate == request.candidate
        calls.append("publish")
        return {"checks": {"suite-published": True, "service-ready": True}}

    monkeypatch.setattr(application, "deploy_suite", publish)
    result = application.execute(request, tmp_path, tmp_path.parent / (tmp_path.name + "-output"),
                                 environment=dict.fromkeys(SECRETS, "fixture-only"))
    assert result == {"checks": dict.fromkeys(("repair-completed", "data-preserved", "unchanged-resources-verified", "service-ready"), True)}
    assert calls == ["source", "pristine", "prepare_linux_tools", "build_runtime", "build_image", "build_sealer", "build_aggregator", "publish"]
    assert request.action == "repair" and request.previous == PARENT_RELEASE


def test_pristine_failure_is_fixed_diagnostic_and_stops_before_tools_or_publication(tmp_path, monkeypatch):
    monkeypatch.setattr(application, "inspect_checkout", lambda *_: [])
    def refused(*_): raise repair.PristineError("DO_NOT_PRINT_PRIVATE_RESPONSE")
    monkeypatch.setattr(application, "assert_pristine", refused)
    monkeypatch.setattr(application, "prepare_linux_tools", lambda *_: pytest.fail("unexpected build"))
    monkeypatch.setattr(application, "deploy_suite", lambda *_args, **_kwargs: pytest.fail("unexpected publication"))
    with pytest.raises(StageError, match="EASYPROXY_SBA_PRISTINE_UNKNOWN"):
        application.execute(parse(request_fixture()), tmp_path, tmp_path.parent / (tmp_path.name + "-output"),
                            environment=dict.fromkeys(SECRETS, "fixture-only"))


def test_manifest_repair_scope_and_fixed_parent_match_parser():
    manifest = json.loads((Path(__file__).resolve().parents[1] / "manifest.json").read_bytes())
    scope = next(row for row in manifest["repairs"] if row["id"] == REPAIR_ID)
    assert scope["id"] == REPAIR_ID and scope["fromErrorCodes"] == [PARENT_ERROR]
    assert set(scope["secretNames"]) == SECRETS and manifest["version"] == "1.0.9"
