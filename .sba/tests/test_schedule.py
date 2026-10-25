"""调度 bootstrap 的路径、凭据边界和 Linux 总截止；无云写入。"""
from contextlib import nullcontext
import json
from pathlib import Path
import signal
import sys
import time
from types import SimpleNamespace

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import schedule
from deadline import ExecutionDeadline, MAX_SECONDS, action_deadline, remaining
from refresh_context import CONFIG_VARIABLE, OWNER_VARIABLE, REFRESH_SECRETS, RefreshError
from suite_config import parse_configuration
from test_refresh import environment
from test_suite_config import PUBLIC


def values():
    return environment(parse_configuration(PUBLIC, repository="fixture/EasyProxy", environment="testing"))


def test_schedule_passes_only_refresh_secrets_and_public_context_to_children(tmp_path):
    source = values()
    source.update(GITHUB_TOKEN="must-not-inherit", EASYPROXY_CLOUDFLARE_API_TOKEN="must-not-inherit",
                  EASYPROXY_GITHUB_TOKEN="must-not-inherit", EASYPROXY_MISUB_COOKIE_SECRET="must-not-inherit",
                  PYTHONPATH="must-not-inherit", HTTP_PROXY="must-not-inherit")
    actual = schedule.child_environment(tmp_path, source)
    assert not any("must-not-inherit" in value for value in actual.values())
    assert set(REFRESH_SECRETS) <= actual.keys()
    assert actual["GITHUB_REPOSITORY"] == "fixture/easyproxy" and actual[OWNER_VARIABLE] == source[OWNER_VARIABLE]


def test_bootstrap_selects_then_uses_prepared_approved_source_not_main(tmp_path, monkeypatch):
    root = tmp_path / "default-branch"
    (root / ".sba").mkdir(parents=True)
    temporary = tmp_path / "temporary"
    temporary.mkdir()
    monkeypatch.setattr(schedule, "__file__", str(root / ".sba/schedule.py"))
    monkeypatch.setattr(schedule, "action_deadline", lambda *_: nullcontext())
    installs, processes = [], []

    def prepare(lock, output):
        installs.append((lock, output))
        return SimpleNamespace(python=output / "venv/bin/python")

    def run(args, **kwargs):
        processes.append((args, kwargs))
        assert not any(secret in str(args) for secret in ("fixture-value", "must-not-inherit"))
        assert not any("must-not-inherit" in value for value in kwargs["env"].values())
        if len(processes) == 1:
            assert args[3] == str(root / ".sba/refresh_select.py")
            selected = Path(args[5])
            (selected / "source/.sba").mkdir(parents=True)
        else:
            assert Path(args[3]).as_posix().endswith("selected/source/.sba/run_refresh.py")
            assert Path(args[0]).parent.parent.parent.name == "approved-python"
            assert "--selection" in args and "--deadline" in args and kwargs["timeout"] <= MAX_SECONDS + 120
        return SimpleNamespace(returncode=0)

    monkeypatch.setattr(schedule, "prepare_python", prepare)
    monkeypatch.setattr(schedule.subprocess, "run", run)
    source = {**values(), "RUNNER_TEMP": str(temporary), "GITHUB_TOKEN": "must-not-inherit"}
    schedule.run_schedule(environment=source)
    assert len(processes) == len(installs) == 2
    assert installs[0][0] == root / ".sba/requirements-linux.lock"
    assert installs[1][0].as_posix().endswith("selected/source/.sba/requirements-linux.lock")


def test_schedule_failure_stops_before_approved_runtime_or_publication(tmp_path, monkeypatch):
    monkeypatch.setattr(schedule, "action_deadline", lambda *_: nullcontext())
    installs = []
    monkeypatch.setattr(schedule, "prepare_python", lambda lock, output: installs.append(lock) or SimpleNamespace(python=Path("/fake/python")))
    monkeypatch.setattr(schedule.subprocess, "run", lambda *_args, **_kwargs: SimpleNamespace(returncode=1))
    with pytest.raises(RefreshError):
        schedule.run_schedule(environment={**values(), "RUNNER_TEMP": str(tmp_path)})
    assert len(installs) == 1


@pytest.mark.parametrize("offset", [-1, 0, MAX_SECONDS + 1, float("inf"), float("nan")])
def test_invalid_or_expired_total_budget_is_rejected(offset):
    with pytest.raises(ExecutionDeadline):
        remaining(time.monotonic() + offset)


@pytest.mark.skipif(not hasattr(signal, "setitimer"), reason="Native Linux signal timer required")
def test_linux_deadline_interrupts_work_and_restores_timer_and_handler():
    previous = signal.getsignal(signal.SIGALRM)
    with pytest.raises(ExecutionDeadline):
        with action_deadline(time.monotonic() + .02):
            time.sleep(1)
    assert signal.getitimer(signal.ITIMER_REAL) == (0.0, 0.0)
    assert signal.getsignal(signal.SIGALRM) == previous


def test_workflow_is_secret_scoped_read_only_and_has_no_user_supplied_sha():
    path = Path(__file__).resolve().parents[2] / ".github/workflows/sba-aggregator-refresh.yml"
    body = path.read_text(encoding="utf-8")
    assert "contents: read" in body and "persist-credentials: false" in body
    assert "ubuntu-24.04" in body and "cancel-in-progress: false" in body
    assert "workflow_dispatch:" in body and "inputs:" not in body
    assert "CLOUDFLARE_API_TOKEN" not in body and "GITHUB_TOKEN" not in body and "COOKIE_SECRET" not in body
    assert "python -B -s .sba/schedule.py" in body
