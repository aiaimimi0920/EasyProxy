"""清理失败不能签发成功回执；不能删除同名外部容器或重发 UNKNOWN 清理。"""
from contextlib import ExitStack
import json
import os
from pathlib import Path
import subprocess
import sys
from types import SimpleNamespace
from unittest.mock import Mock, patch

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))
sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from scripts.easyproxy_audit_lifecycle import AuditContainer
from scripts import easyproxy_source_audit as cli
import audit_runner
import aggregator_publish as publish
from artifact_store import ArtifactError
from test_aggregator_publish import BODIES, OLD, audit, setup, state
from aggregator_publish import Candidate
from coordination import _encode
from dataclasses import replace


IMAGE, SOURCE, ID = "sha256:" + "f" * 64, "a" * 40, "b" * 64


def owner(tmp_path):
    return AuditContainer("easyproxy-source-audit-fixture", IMAGE, SOURCE, "fixture", tmp_path / "cleanup.json")


def row(value):
    return {"Id": ID, "Name": "/" + value.name, "Image": IMAGE,
            "Config": {"Labels": {"easyproxy.audit.source": SOURCE, "easyproxy.audit.operation": "fixture"}}}


def mock_docker(monkeypatch, value, *, drift=None, rm_ok=True, absent=False):
    calls = []
    metadata = row(value)
    if drift:
        drift(metadata)

    def run(args):
        calls.append(args)
        if args[:2] == ["container", "ls"]:
            return SimpleNamespace(returncode=0, stdout="" if absent else ID + "\n")
        if args[:2] == ["container", "inspect"]:
            assert args[2] == ID
            return SimpleNamespace(returncode=0, stdout=json.dumps([metadata]))
        assert args == ["rm", "--force", ID]
        return SimpleNamespace(returncode=0 if rm_ok else 1, stdout=ID + "\n" if rm_ok else "")

    monkeypatch.setattr(value, "_run", run)
    return calls


def test_cleanup_uses_exact_verified_id_and_cannot_be_repeated(tmp_path, monkeypatch):
    value = owner(tmp_path)
    calls = mock_docker(monkeypatch, value)
    value.cleanup()
    assert calls[-1] == ["rm", "--force", ID]
    with pytest.raises(RuntimeError, match="ALREADY_ATTEMPTED"):
        value.cleanup()
    assert sum(args[0] == "rm" for args in calls) == 1
    if os.name == "posix":
        assert value.journal.stat().st_mode & 0o777 == 0o600


@pytest.mark.parametrize("drift", [lambda r: r.update(Id="c" * 64), lambda r: r.update(Name="/other"),
    lambda r: r.update(Image="sha256:" + "0" * 64),
    lambda r: r["Config"]["Labels"].update({"easyproxy.audit.source": "0" * 40}),
    lambda r: r["Config"]["Labels"].update({"easyproxy.audit.operation": "other"})])
def test_foreign_container_never_removed(tmp_path, monkeypatch, drift):
    value = owner(tmp_path)
    calls = mock_docker(monkeypatch, value, drift=drift)
    with pytest.raises(RuntimeError, match="OWNER_MISMATCH"):
        value.cleanup()
    assert not any(args[0] == "rm" for args in calls)


def test_rm_failure_is_unknown_and_is_never_retried(tmp_path, monkeypatch):
    value = owner(tmp_path)
    calls = mock_docker(monkeypatch, value, rm_ok=False)
    with pytest.raises(RuntimeError, match="UNCONFIRMED"):
        value.cleanup()
    with pytest.raises(RuntimeError, match="ALREADY_ATTEMPTED"):
        value.cleanup()
    assert sum(args[0] == "rm" for args in calls) == 1


def test_daemon_failure_is_not_absence(tmp_path, monkeypatch):
    value = owner(tmp_path)
    monkeypatch.setattr(value, "_run", lambda *_: SimpleNamespace(returncode=1, stdout=""))
    with pytest.raises(RuntimeError, match="LOOKUP_UNKNOWN"):
        value.assert_absent()
    with pytest.raises(RuntimeError, match="LOOKUP_UNKNOWN"):
        value.cleanup()


def test_preexisting_name_blocks_start_without_removal(tmp_path, monkeypatch):
    value = owner(tmp_path)
    calls = mock_docker(monkeypatch, value)
    with pytest.raises(RuntimeError, match="ALREADY_EXISTS"):
        value.assert_absent()
    assert not value.journal.exists() and not any(args[0] == "rm" for args in calls)


def test_start_failure_with_no_container_is_confirmed_without_rm(tmp_path, monkeypatch):
    value = owner(tmp_path)
    calls = mock_docker(monkeypatch, value, absent=True)
    value.cleanup()
    assert not any(args[0] == "rm" for args in calls)


def test_old_attempt_blocks_a_new_start_before_docker(tmp_path, monkeypatch):
    value = owner(tmp_path)
    value.journal.write_text("UNKNOWN", encoding="utf-8")
    monkeypatch.setattr(value, "_run", lambda *_: pytest.fail("must not start/reconcile old attempt"))
    with pytest.raises(RuntimeError, match="ALREADY_ATTEMPTED"):
        value.assert_startable()


def configure_cli(stack, tmp_path, monkeypatch, lifecycle):
    monkeypatch.setattr(sys, "argv", ["audit", "--audit-id", "fixture", "--image", IMAGE,
        "--expected-image-id", IMAGE, "--expected-source-sha", SOURCE, "--proxy-uri", "http://fixture.invalid:80",
        "--artifact-dir", str(tmp_path), "--docker-network-name", "", "--scenario-timeout-seconds", "1"])
    for name, value in {"ensure_docker": None, "load_policy": {}, "inspect_image": {"imageId": IMAGE},
        "get_free_port": 12345, "get_free_port_range_start": 34000, "build_config": {},
        "wait_management_ready": {}, "wait_scenario_state": {}, "collect_container_networks": [],
        "fetch_nodes_and_source_sync": ({"total_nodes": 1, "nodes": []}, {}),
        "collect_direct_probe_candidates": [], "discover_directly_usable_nodes": (["http://fixture.invalid:80"], [{"tag": "fixture"}]),
        "probe_http_proxy": {"ok": True, "attempts": []},
        "checkout_proxy_lease": {"id": "fixture", "proxyUrl": "http://127.0.0.1:22323"},
        "report_proxy_lease": None, "release_proxy_lease": None}.items():
        stack.enter_context(patch.object(cli, name, return_value=value))
    stack.enter_context(patch.object(cli, "AuditContainer", return_value=lifecycle))
    stack.enter_context(patch.object(cli.requests, "get", return_value=Mock(json=lambda: {})))
    stack.enter_context(patch.object(cli, "ensure_image", side_effect=AssertionError("implicit build")))
    stack.enter_context(patch.object(cli, "stop_container", side_effect=AssertionError("legacy cleanup")))


@pytest.mark.parametrize("cleanup_error", [False, True])
def test_success_receipt_is_written_only_after_single_confirmed_cleanup(tmp_path, monkeypatch, cleanup_error):
    lifecycle = Mock(spec=AuditContainer)
    lifecycle.label_args.return_value = ["--label", "easyproxy.audit.operation=fixture"]
    if cleanup_error:
        lifecycle.cleanup.side_effect = RuntimeError("EASYPROXY_AUDIT_CLEANUP_UNCONFIRMED")
    with ExitStack() as stack:
        configure_cli(stack, tmp_path, monkeypatch, lifecycle)
        stack.enter_context(patch.object(cli, "run", return_value=SimpleNamespace(returncode=0, stdout="", stderr="")))
        if cleanup_error:
            with pytest.raises(RuntimeError, match="UNCONFIRMED"):
                cli.main()
        else:
            assert cli.main() == 0
    body = json.loads((tmp_path / "summary.json").read_text())
    assert body["container_cleanup_confirmed"] is (not cleanup_error)
    lifecycle.cleanup.assert_called_once()
    assert not (tmp_path / "config.yaml").exists()


def test_created_but_failed_docker_start_is_cleaned_once(tmp_path, monkeypatch):
    lifecycle = Mock(spec=AuditContainer)
    lifecycle.label_args.return_value = []
    with ExitStack() as stack:
        configure_cli(stack, tmp_path, monkeypatch, lifecycle)
        stack.enter_context(patch.object(cli, "run", side_effect=RuntimeError("start failed")))
        with pytest.raises(RuntimeError, match="start failed"):
            cli.main()
    lifecycle.cleanup.assert_called_once()
    assert not (tmp_path / "summary.json").exists() and not (tmp_path / "config.yaml").exists()


def test_promotion_rejects_old_receipt_without_cleanup_proof(monkeypatch):
    monkeypatch.setattr(publish, "inspect_checkout", lambda *_: [{"path": "upstreams/aggregator", "sourceSha": "e" * 40}])
    manager, objects, public = setup()
    permit = manager.begin("deploy", OLD)
    receipt = audit(permit, public)
    body = json.loads(receipt.body)
    body.pop("container_cleanup_confirmed")
    with pytest.raises(ArtifactError, match="AUDIT_CLEANUP"):
        publish.validated_audit(manager, permit, Candidate(permit.operation_id, BODIES),
                                replace(receipt, body=_encode(body)), Path("fixture"), public)
    assert state(manager.store)["writer"] is not None


@pytest.mark.skipif(os.name != "posix", reason="requires actual Linux process-group semantics")
@pytest.mark.parametrize("claimed", [False, True])
@pytest.mark.parametrize("termination", ["timeout", "nonzero", "signal"])
def test_real_failure_kills_process_group_and_does_not_repeat_claimed_cleanup(tmp_path, claimed, termination):
    lifecycle = Mock(journal=tmp_path / "attempt.json")
    if claimed:
        lifecycle.journal.write_text("attempted", encoding="utf-8")
    child_pid = tmp_path / "descendant.pid"
    script = ("import subprocess,time,pathlib,sys; "
              "p=subprocess.Popen([sys.executable,'-c','import time;time.sleep(120)']); "
              "pathlib.Path(sys.argv[1]).write_text(str(p.pid)); ")
    script += {"timeout": "time.sleep(120)", "nonzero": "sys.exit(1)",
               "signal": "import os,signal;os.kill(os.getpid(),signal.SIGTERM)"}[termination]
    with pytest.raises(ArtifactError, match="AUDIT_UNKNOWN"):
        audit_runner.run_audit([sys.executable, "-c", script, str(child_pid)], tmp_path, dict(os.environ),
                               lifecycle=lifecycle, timeout=1)
    assert lifecycle.cleanup.call_count == (0 if claimed else 1)
    pid = int(child_pid.read_text())
    process = Path(f"/proc/{pid}/stat")
    assert not process.exists() or process.read_text().split()[2] == "Z"
