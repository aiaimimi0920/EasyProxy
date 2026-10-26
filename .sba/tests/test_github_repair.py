"""定向恢复只接受准确父任务和部分状态；不重放、不解锁、条件失败即停止。"""
import copy
from dataclasses import replace
import hashlib
import io
import json
from pathlib import Path
import sys
from types import SimpleNamespace

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import application
import github_environment
import github_repair as repair
from coordination import Coordinator, CoordinationError, Permit, StoreUnknown, _encode, STATE_KEY
from coordination import StoreConflict
from github_api import GitHubError
from repair_context import GITHUB_CONTEXT, GITHUB_PARENT_RELEASE
from request import RequestError, SECRETS
from test_coordination import MemoryStore, state
from test_pristine_repair import request_fixture
from test_request import parse


def recovery_request():
    value = request_fixture()
    value.update(previous=dict(GITHUB_PARENT_RELEASE), context=dict(GITHUB_CONTEXT))
    return value


@pytest.mark.parametrize("field", list(GITHUB_CONTEXT))
def test_recovery_context_rejects_any_different_parent_receipt_field(field):
    value = recovery_request()
    value["context"][field] = 42 if field == "parentRunId" else "different"
    with pytest.raises(RequestError):
        parse(value)


@pytest.fixture
def partial(tmp_path, monkeypatch):
    request = parse(recovery_request())
    store = MemoryStore()
    store.bucket = request.config.bucket
    owner = Coordinator(store, request.config.identity("production"), request.config.inventory_digest)
    old = owner.begin("deploy", GITHUB_PARENT_RELEASE)
    for stage in ("database-initialization", "github-environment"):
        owner.claim_stage(old, stage)
    monkeypatch.setattr(repair, "CONTROL_SHA256", hashlib.sha256(store.body).hexdigest())
    record = {"mutation": None, "queries": [], "closed": [], "parent": 0}
    schema = [{"type": "table", "name": "fixture", "tbl_name": "fixture", "sql": "CREATE TABLE fixture(id)"}]
    monkeypatch.setattr(repair, "reviewed_plan", lambda _: ([], schema))
    monkeypatch.setattr(github_environment, "reviewed_workflow", lambda _: b"approved-workflow")

    def parent(_token):
        record["parent"] += 1
        if record["mutation"] == "parent": raise repair.GitHubRepairError("fixture")
    monkeypatch.setattr(repair, "parent_completed", parent)

    class Client:
        def list_objects_v2(self, **_kwargs):
            keys = [STATE_KEY] + [f"_sba/operations/{op}/{stage}.json" for op, stage in store.stages]
            if record["mutation"] == "extra-object": keys.append("private-data")
            return {"IsTruncated": record["mutation"] == "truncated", "Contents": [{"Key": key} for key in keys]}
        def get_object(self, Key, **_kwargs):
            op, stage = Key.split("/")[-2:]
            body = store.stages[(op, stage.removesuffix(".json"))]
            return {"Body": io.BytesIO(b"wrong" if record["mutation"] == "stage" else body)}
    store.client = Client()

    class Database:
        def __init__(self, *_): pass
        def close(self): record["closed"].append("database")
        def identity(self):
            return {"uuid": request.config.database_id, "name": "wrong" if record["mutation"] == "d1-id" else request.config.database_name}
        def query(self, sql):
            record["queries"].append(sql)
            if sql == repair.SCHEMA_QUERY: return [] if record["mutation"] == "schema" else schema
            if sql == repair.HISTORY_QUERY:
                return [] if record["mutation"] == "history" else [{"migration_id": 1, "name": "0001_initial"}, {"migration_id": 2, "name": "0002_cron_executions"}]
            assert sql == repair.COUNT_QUERY, "recovery may only issue reviewed SELECT queries"
            return [{"subscriptions": int(record["mutation"] == "data"), "profiles": 0, "settings": 0, "cron_executions": 0}]

    class Worker:
        def __init__(self, *_): pass
        def close(self): record["closed"].append("worker")
        def settings_or_absent(self, _): return {} if record["mutation"] == "worker" else None
        def account_subdomain(self): return request.config.workers_subdomain

    class Pages:
        def __init__(self, *_): pass
        def close(self): record["closed"].append("pages")
        def project(self): return {} if record["mutation"] == "pages" else None

    expected = github_environment.variables(owner, request.config, 123, repair.ENVIRONMENT_ID)
    class Github:
        def __init__(self, *_): pass
        def close(self): record["closed"].append("github")
        def repository_info(self): return {"id": 123, "defaultBranch": "main"}
        def workflow_at(self, _): return b"wrong" if record["mutation"] == "workflow" else b"approved-workflow"
        def active_workflow(self): pass
        def environment_info(self):
            return {"id": 0 if record["mutation"] == "environment" else repair.ENVIRONMENT_ID,
                    "deployment_branch_policy": {"protected_branches": False, "custom_branch_policies": True},
                    "protection_rules": [{"type": "branch_policy"}]}
        def policies(self): return [{"id": 1, "name": "wrong" if record["mutation"] == "policy" else "main", "type": "branch"}]
        def variables(self):
            return expected if record["mutation"] == "variables" else {github_environment.CONFIG_VARIABLE: expected[github_environment.CONFIG_VARIABLE]}
        def secret_names(self): return {"FOREIGN"} if record["mutation"] == "secrets" else set()
        def repository_variable(self, _): return "foreign" if record["mutation"] == "registry" else None
    for name, cls in (("D1API", Database), ("WorkerAPI", Worker), ("PagesAPI", Pages), ("GitHubAPI", Github)):
        monkeypatch.setattr(repair, name, cls)
    return request, owner, old, store, record, tmp_path


def run(partial):
    request, owner, _, _, _, root = partial
    return repair.begin_recovery(owner, request, root, dict.fromkeys(SECRETS, "fixture-token"))


def test_transfer_is_one_cas_keeps_old_markers_never_unlocks_and_invalidates_old_permit(partial):
    request, owner, old, store, record, _ = partial
    previous = dict(store.stages)
    permit, resume = run(partial)
    assert len(store.writes) == 2 and state(store)["active"] is None
    assert state(store)["writer"] == {"operationId": permit.operation_id, "action": "deploy", "candidate": request.candidate}
    assert all(store.stages[key] == body for key, body in previous.items()) and len(store.stages) == 3
    resume.assert_permit(owner, permit)
    with pytest.raises(CoordinationError, match="OWNERSHIP"): owner.assert_owned(old)
    assert record["queries"] == [repair.SCHEMA_QUERY, repair.HISTORY_QUERY, repair.COUNT_QUERY]
    with pytest.raises(repair.GitHubRepairError): run(partial)
    assert len(store.writes) == 2 and len(store.stages) == 3


@pytest.mark.parametrize("mutation", ["parent", "extra-object", "truncated", "stage", "d1-id", "schema", "history", "data",
                                      "worker", "pages", "workflow", "environment", "policy", "variables", "secrets", "registry"])
def test_any_partial_state_drift_refuses_all_writes_and_preserves_original_writer(partial, mutation):
    _, owner, old, store, record, _ = partial
    record["mutation"] = mutation
    with pytest.raises((repair.GitHubRepairError, GitHubError)): run(partial)
    owner.assert_owned(old)
    assert len(store.writes) == 1 and len(store.stages) == 2


@pytest.mark.parametrize("failure", ["marker-unknown", "cas-unknown", "cas-response-lost"])
def test_unknown_transition_is_not_replayed_or_unlocked(partial, monkeypatch, failure):
    _, _, _, store, _, _ = partial
    if failure == "marker-unknown":
        original = store.create_stage
        def ambiguous(*args):
            original(*args)
            raise StoreUnknown()
        monkeypatch.setattr(store, "create_stage", ambiguous)
    elif failure == "cas-unknown": store.ambiguous = True
    else: store.fail_after_commit = True
    with pytest.raises((StoreUnknown, CoordinationError)): run(partial)
    assert state(store)["writer"] is not None and state(store)["active"] is None
    before = (len(store.writes), len(store.stages))
    with pytest.raises(repair.GitHubRepairError): run(partial)
    assert before == (len(store.writes), len(store.stages))


def test_noncanonical_control_bytes_are_drift_even_when_json_is_equivalent(partial):
    store = partial[3]
    store.body = b" " + store.body
    with pytest.raises(repair.GitHubRepairError): run(partial)
    assert len(store.writes) == 1 and len(store.stages) == 2


def test_single_cas_conflict_preserves_original_writer_and_durable_transition_evidence(partial, monkeypatch):
    _, owner, old, store, _, _ = partial
    calls = []
    def conflict(*_):
        calls.append("CAS")
        raise StoreConflict()
    monkeypatch.setattr(store, "compare_and_set", conflict)
    with pytest.raises(CoordinationError, match="CONFLICT"): run(partial)
    owner.assert_owned(old)
    assert calls == ["CAS"] and len(store.stages) == 3 and len(store.writes) == 1
    with pytest.raises(repair.GitHubRepairError): run(partial)
    assert calls == ["CAS"]


def test_state_change_during_preflight_stops_before_any_transition_write(partial, monkeypatch):
    store = partial[3]
    original = repair.checked_github
    def changed(*args):
        original(*args)
        store.etag = '"' + "f" * 32 + '"'
    monkeypatch.setattr(repair, "checked_github", changed)
    with pytest.raises(repair.GitHubRepairError): run(partial)
    assert len(store.writes) == 1 and len(store.stages) == 2


def test_manifest_has_separate_error_scope_with_only_existing_secret_names():
    manifest = json.loads((Path(__file__).resolve().parents[1] / "manifest.json").read_bytes())
    scope = next(row for row in manifest["repairs"] if row["id"] == GITHUB_CONTEXT["repairId"])
    assert scope["fromErrorCodes"] == [GITHUB_CONTEXT["errorCode"]]
    assert set(scope["secretNames"]) == SECRETS and manifest["version"] == "1.0.9"


def test_application_passes_exact_repair_context_only_to_new_recovery_path(tmp_path, monkeypatch):
    request = parse(recovery_request())
    monkeypatch.setattr(application, "inspect_checkout", lambda *_: [])
    monkeypatch.setattr(application, "assert_pristine", lambda *_: pytest.fail("not a pristine repair"))
    for name in ("prepare_linux_tools", "build_runtime", "build_image", "build_sealer", "build_aggregator"):
        monkeypatch.setattr(application, name, lambda *_args, **_kwargs: None)
    def deploy(current, *_args, **kwargs):
        assert current == replace(request, action="deploy", previous=None, context=None)
        assert kwargs["recovery_request"] is request
        return {"checks": {"suite-published": True, "service-ready": True}}
    monkeypatch.setattr(application, "deploy_suite", deploy)
    assert application.execute(request, tmp_path, tmp_path.parent / (tmp_path.name + "-output"),
                               environment=dict.fromkeys(SECRETS, "fixture-token"))["checks"]["repair-completed"] is True


@pytest.mark.parametrize("mutation", [None, "status", "conclusion", "run_attempt", "head_sha", "path", "display_title", "event", "http", "json"])
def test_original_run_must_be_terminal_first_attempt_and_exact_executor_without_redirect_or_retry(monkeypatch, mutation):
    row = {"id": GITHUB_CONTEXT["parentRunId"], "run_attempt": 1, "status": "completed", "conclusion": "success",
           "head_sha": repair.EXECUTOR_SHA, "event": "workflow_dispatch", "path": ".github/workflows/sba-execute-linux.yml",
           "display_title": "sba:" + GITHUB_CONTEXT["parentTaskId"] + ":" + GITHUB_CONTEXT["requestDigest"]}
    if mutation in row: row[mutation] = True if mutation == "run_attempt" else "wrong"
    calls = []
    class Response:
        status_code = 307 if mutation == "http" else 200
        def __enter__(self): return self
        def __exit__(self, *_): pass
        def iter_content(self, _): yield b"invalid" if mutation == "json" else json.dumps(row).encode()
    class Session:
        def close(self): calls.append("closed")
        def get(self, url, **kwargs):
            assert url == "https://api.github.com/repos/aiaimimi0920/SpringBok/actions/runs/40272310671"
            assert kwargs["allow_redirects"] is False and kwargs["stream"] is True
            calls.append("GET")
            return Response()
    monkeypatch.setattr(repair, "secure_session", Session)
    if mutation is None: repair.parent_completed("fixture-only")
    else:
        with pytest.raises(repair.GitHubRepairError): repair.parent_completed("fixture-only")
    assert calls == ["GET", "closed"]
