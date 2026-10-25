"""正式入口/结果的局部证明；不提供虚假五动作成功，不运行云发布。"""
from contextlib import nullcontext
import json
from pathlib import Path
import sys
from types import SimpleNamespace

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import application
import bootstrap as entry
from result import ResultError, write_result
from test_request import fixture, parse


def test_result_is_utf8_bom_free_create_only_plain_file_with_exact_contract(tmp_path):
    request = parse(fixture())
    path = tmp_path / "result.json"
    write_result(path, request, status="succeeded", checks={"service-ready": True})
    value = json.loads(path.read_bytes())
    assert set(value) == {"schemaVersion", "taskId", "action", "sourceSha", "applicationVersion", "status", "checks"}
    assert value["taskId"] == request.task_id and value["checks"] == [{"id": "service-ready", "passed": True}]
    assert not path.read_bytes().startswith(b"\xef\xbb\xbf") and path.stat().st_nlink == 1
    with pytest.raises(ResultError):
        write_result(path, request, status="unknown", checks={})
    assert json.loads(path.read_bytes())["status"] == "succeeded"


@pytest.mark.parametrize("status,checks,error", [
    ("succeeded", {}, None), ("succeeded", {"service-ready": False}, None),
    ("succeeded", {"service-ready": 1}, None), ("unknown", {"UPPER": True}, None),
    ("unknown", {}, "private provider response"), ("invented", {}, None)])
def test_invalid_results_fail_before_publishing_a_file(tmp_path, status, checks, error):
    with pytest.raises(ResultError):
        write_result(tmp_path / "result.json", parse(fixture()), status=status, checks=checks, error_code=error)
    assert not list(tmp_path.iterdir())


def test_verify_does_not_read_environment_or_construct_writer_or_build_tools(tmp_path, monkeypatch):
    request = parse(fixture("verify"))
    monkeypatch.setattr(application, "inspect_checkout", lambda *_: [])
    monkeypatch.setattr(application, "prepare_linux_tools", lambda *_: pytest.fail("unexpected tools"))
    monkeypatch.setattr(application, "deploy_suite", lambda *_args, **_kwargs: pytest.fail("unexpected writer"))
    checks = {"release-versions-verified": True, "published-subscriptions-verified": True, "pages-frontend-verified": True}
    monkeypatch.setattr(application, "verify_public", lambda config, release, root: {"checks": checks, "private": "not a receipt field"})

    class NoReads:
        def get(self, *_):
            pytest.fail("verify read a secret")

    assert application.execute(request, tmp_path, tmp_path / "unused", environment=NoReads()) == {"checks": checks}
    assert not (tmp_path / "unused").exists()


@pytest.fixture
def bootstrap_input(tmp_path, monkeypatch):
    root = tmp_path / "checkout"
    (root / ".sba").mkdir(parents=True)
    task = tmp_path / "task"
    task.mkdir()
    request_path, result_path = task / "request.json", task / "application-result.json"
    request_path.write_text(json.dumps(fixture("verify")), encoding="utf-8")
    request = parse(fixture("verify"))
    monkeypatch.setattr(entry, "__file__", str(root / ".sba/bootstrap.py"))
    monkeypatch.setattr(entry, "_git", lambda *_: request.source_sha)
    monkeypatch.setattr(entry, "manifest_release", lambda *_: request.candidate)
    monkeypatch.setattr(entry, "inspect_checkout", lambda *_: [])
    monkeypatch.setattr(entry, "materialize_workspace", lambda output, *_args, **_kwargs: output.mkdir(parents=True))
    monkeypatch.setattr(entry, "prepare_python", lambda _lock, output: SimpleNamespace(python=output / "venv/bin/python"))
    monkeypatch.setattr(entry, "action_deadline", lambda *_: nullcontext())
    return root, task, request, request_path, result_path


def test_bootstrap_prepares_gitlinks_outside_source_and_forwards_no_verify_placeholders(bootstrap_input, monkeypatch):
    root, task, request, request_path, result_path = bootstrap_input
    calls = []

    def run(args, **kwargs):
        calls.append(args)
        script = Path(args[3])
        assert script.name == "execute.py" and root not in script.parents and task in script.parents
        assert "PYTHONPATH" not in kwargs["env"] and "GITHUB_TOKEN" not in kwargs["env"]
        assert not any(value == "verification-not-applicable" for value in kwargs["env"].values())
        assert "--request" in args and "--result" in args and "--deadline" in args
        write_result(result_path, request, status="succeeded", checks={"service-ready": True})
        return SimpleNamespace(returncode=0)

    monkeypatch.setattr(entry.subprocess, "run", run)
    assert entry.bootstrap(request_path, result_path, environment={"RUNNER_TEMP": str(task), "GITHUB_TOKEN": "must-not-inherit"}) == 0
    assert len(calls) == 1 and json.loads(result_path.read_bytes())["status"] == "succeeded"
    assert list(root.iterdir()) == [root / ".sba"]


def test_bootstrap_missing_child_receipt_is_unknown_not_success(bootstrap_input, monkeypatch):
    _, task, _, request_path, result_path = bootstrap_input
    monkeypatch.setattr(entry.subprocess, "run", lambda *_args, **_kwargs: SimpleNamespace(returncode=0))
    assert entry.bootstrap(request_path, result_path, environment={"RUNNER_TEMP": str(task)}) == 1
    value = json.loads(result_path.read_bytes())
    assert value["status"] == "unknown" and value["errorCode"] == "EASYPROXY_SBA_RESULT_MISSING"


def test_bootstrap_never_replaces_an_unknown_or_failed_child_receipt(bootstrap_input, monkeypatch):
    _, task, request, request_path, result_path = bootstrap_input

    def run(*_args, **_kwargs):
        write_result(result_path, request, status="unknown", checks={}, error_code="EASYPROXY_SBA_FAILED_OR_UNKNOWN")
        return SimpleNamespace(returncode=1)

    monkeypatch.setattr(entry.subprocess, "run", run)
    assert entry.bootstrap(request_path, result_path, environment={"RUNNER_TEMP": str(task)}) == 1
    assert json.loads(result_path.read_bytes())["errorCode"] == "EASYPROXY_SBA_FAILED_OR_UNKNOWN"
