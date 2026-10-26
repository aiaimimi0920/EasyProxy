"""坏订阅只影响自身，不能清空好任务；补丁只存在于固定源的隔离副本。"""
import ast
import copy
import hashlib
import json
from pathlib import Path
import shutil
import subprocess
import sys
from types import SimpleNamespace

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import aggregator_build as build
import aggregator_runtime as runtime
from artifact_store import ArtifactError
from test_aggregator_runtime import approved_utils

ROOT = Path(__file__).resolve().parents[2]
PRIVATE = "CONTROLLED_PRIVATE_TASK_NAME_URL_OR_EXCEPTION"


def approved_workflow(workspace):
    raw = subprocess.check_output(["git", "-C", str(ROOT / "upstreams/aggregator"), "show",
        runtime.AGGREGATOR_SHA + ":subscribe/workflow.py"])
    path = workspace / "subscribe/workflow.py"
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(raw)
    return path


def patched_wrapper(tmp_path):
    path = approved_workflow(tmp_path)
    runtime.patch_source_tasks(tmp_path.resolve())
    node = next(node for node in ast.parse(path.read_bytes()).body if isinstance(node, ast.FunctionDef) and node.name == "executewrapper")
    messages = []
    namespace = {"TaskConfig": object, "logger": SimpleNamespace(error=messages.append)}
    exec(compile(ast.Module(body=[node], type_ignores=[]), str(path), "exec"), namespace)
    return namespace, messages


@pytest.mark.parametrize("error_type", [ValueError, RuntimeError, OSError])
def test_ordinary_task_failure_preserves_id_and_never_retries_or_logs_private_details(tmp_path, error_type):
    namespace, messages = patched_wrapper(tmp_path)
    calls = []
    def execute(*, task_conf):
        calls.append(task_conf.taskid)
        raise error_type(PRIVATE)
    namespace["execute"] = execute
    assert namespace["executewrapper"](SimpleNamespace(taskid=23, name=PRIVATE)) == (23, [])
    assert calls == [23] and messages == ["[SBA] subscription task failed"]
    assert PRIVATE not in json.dumps(messages)


def test_success_keeps_the_exact_original_list_and_id(tmp_path):
    namespace, messages = patched_wrapper(tmp_path)
    proxies = [{"name": PRIVATE}]
    namespace["execute"] = lambda **_: proxies
    result = namespace["executewrapper"](SimpleNamespace(taskid=17))
    assert result[0] == 17 and result[1] is proxies and messages == []


@pytest.mark.parametrize("task", [None, False, 0])
def test_original_falsy_task_semantics_are_preserved(tmp_path, task):
    namespace, _ = patched_wrapper(tmp_path)
    namespace["execute"] = lambda **_: pytest.fail("invalid task must not execute")
    assert namespace["executewrapper"](task) == (-1, [])


@pytest.mark.parametrize("error_type", [KeyboardInterrupt, SystemExit])
def test_cancellation_is_not_swallowed_by_the_direct_wrapper(tmp_path, error_type):
    namespace, messages = patched_wrapper(tmp_path)
    def execute(**_): raise error_type(PRIVATE)
    namespace["execute"] = execute
    with pytest.raises(error_type): namespace["executewrapper"](SimpleNamespace(taskid=1))
    assert messages == []


def test_invalid_task_structure_still_fails_outside_the_isolation_boundary(tmp_path):
    namespace, messages = patched_wrapper(tmp_path)
    namespace["execute"] = lambda **_: pytest.fail("invalid task must not execute")
    with pytest.raises(AttributeError): namespace["executewrapper"](SimpleNamespace(name=PRIVATE))
    assert messages == []


def test_exact_patch_hashes_and_unique_receipt_and_no_reapply(tmp_path):
    path = approved_workflow(tmp_path)
    before = path.read_bytes()
    receipt = runtime.patch_source_tasks(tmp_path.resolve())
    assert hashlib.sha256(before).hexdigest() == runtime.WORKFLOW_SHA256
    assert path.read_bytes() == before.replace(runtime.TASK_BEFORE, runtime.TASK_AFTER, 1)
    assert receipt == {"id": "subscription-task-isolation-v1", "path": "subscribe/workflow.py",
        "beforeSha256": runtime.WORKFLOW_SHA256, "afterSha256": runtime.TASK_PATCH_SHA256}
    with pytest.raises(ArtifactError, match="RUNTIME_PATCH"): runtime.patch_source_tasks(tmp_path.resolve())


def test_workflow_source_drift_is_rejected_without_writes(tmp_path):
    path = approved_workflow(tmp_path)
    raw = path.read_bytes() + b"\n# unexpected drift\n"
    path.write_bytes(raw)
    with pytest.raises(ArtifactError, match="RUNTIME_PATCH"): runtime.patch_source_tasks(tmp_path.resolve())
    assert path.read_bytes() == raw


def fixture_runtime(tmp_path, monkeypatch):
    monkeypatch.setattr(runtime.sys, "platform", "linux")
    monkeypatch.setattr(runtime.platform, "machine", lambda: "x86_64")
    approved_utils(tmp_path)
    approved_workflow(tmp_path)
    pins = {}
    for relative in runtime.BINARIES:
        path = tmp_path / relative
        path.parent.mkdir(parents=True)
        body = ("fixture-" + relative).encode()
        path.write_bytes(body)
        pins[relative] = hashlib.sha256(body).hexdigest()
    monkeypatch.setattr(runtime, "BINARIES", pins)


@pytest.mark.parametrize("mutation", ["missing", "extra", "reorder", "id", "path", "before", "after", "workflow-bytes", "utils-bytes"])
def test_post_generation_verification_requires_both_exact_patches(tmp_path, monkeypatch, mutation):
    fixture_runtime(tmp_path, monkeypatch)
    receipt = runtime.prepare_runtime(tmp_path.resolve(), runtime.AGGREGATOR_SHA)
    assert len(receipt["patches"]) == 2
    runtime.verify_runtime(tmp_path.resolve(), receipt)
    changed = copy.deepcopy(receipt)
    if mutation == "missing": changed["patches"].pop()
    elif mutation == "extra": changed["patches"].append(copy.deepcopy(changed["patches"][0]))
    elif mutation == "reorder": changed["patches"].reverse()
    elif mutation in ("id", "path", "before", "after"):
        key = {"before": "beforeSha256", "after": "afterSha256"}.get(mutation, mutation)
        changed["patches"][1][key] = PRIVATE
    else:
        target = tmp_path / ("subscribe/workflow.py" if mutation == "workflow-bytes" else "subscribe/utils.py")
        target.write_bytes(target.read_bytes() + b"\n# drift\n")
    with pytest.raises(ArtifactError, match="RUNTIME_PATCH"): runtime.verify_runtime(tmp_path.resolve(), changed)


def test_all_source_hashes_are_checked_before_either_patch_is_written(tmp_path, monkeypatch):
    fixture_runtime(tmp_path, monkeypatch)
    path = tmp_path / "subscribe/workflow.py"
    path.write_bytes(path.read_bytes() + b"\n# drift\n")
    with pytest.raises(ArtifactError, match="RUNTIME_PATCH"): runtime.prepare_runtime(tmp_path.resolve(), runtime.AGGREGATOR_SHA)
    assert hashlib.sha256((tmp_path / "subscribe/utils.py").read_bytes()).hexdigest() == runtime.UTILS_SHA256


@pytest.mark.skipif(sys.platform != "linux", reason="real approved process-pool proof runs on Linux")
@pytest.mark.parametrize("bad_ids", [[], [1], [2], [1, 2]])
def test_real_approved_pool_preserves_result_order_and_healthy_tasks_without_network(tmp_path, bad_ids):
    checkout = tmp_path / "copy"
    shutil.copytree(ROOT / "upstreams/aggregator/subscribe", checkout / "subscribe")
    original = (ROOT / "upstreams/aggregator/subscribe/workflow.py").read_bytes()
    runtime.patch_source_tasks(checkout.resolve())
    script = '''import json,sys
sys.path.insert(0,sys.argv[1])
import workflow,utils
bad_ids=json.loads(sys.argv[2])
def controlled_execute(*,task_conf):
    if task_conf.taskid in bad_ids: raise ValueError("CONTROLLED_PRIVATE_TASK_NAME_URL_OR_EXCEPTION")
    return [{"name":"healthy-"+str(task_conf.taskid)}]
workflow.execute=controlled_execute
tasks=[workflow.TaskConfig(name="fixture",bin_name="fixture",taskid=i) for i in (1,2)]
rows=utils.multi_process_run(workflow.executewrapper,tasks)
print(json.dumps(rows))
'''
    result = subprocess.run([sys.executable, "-B", "-c", script, str(checkout / "subscribe"), json.dumps(bad_ids)],
        capture_output=True, cwd=tmp_path, timeout=30)
    assert result.returncode == 0
    rows = json.loads(result.stdout.splitlines()[-1])
    assert rows == [[i, [] if i in bad_ids else [{"name": "healthy-" + str(i)}]] for i in (1, 2)]
    assert PRIVATE.encode() not in result.stdout + result.stderr
    assert (ROOT / "upstreams/aggregator/subscribe/workflow.py").read_bytes() == original
    if bad_ids == [1, 2]:
        artifacts = tmp_path / "artifacts"
        artifacts.mkdir()
        for name in ("public-clash", "public-singbox", "public-mixed"):
            with pytest.raises(build.GenerationOutputError) as caught: build._read_output(artifacts, name)
            assert caught.value.reason == "missing"
        assert list(artifacts.iterdir()) == []
