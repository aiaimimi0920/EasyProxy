"""Python 安装的输入、秘密隔离与准确 pip 参数；原生安装单独实际验证。"""
from pathlib import Path
import hashlib

import pytest

from test_sources import sources
import python_runtime as runtime


ROOT = Path(__file__).resolve().parents[2]


def test_tracked_lock_has_canonical_lf_bytes_and_matches_runtime_digest():
    body = (ROOT / ".sba/requirements-linux.lock").read_bytes()
    assert b"\r" not in body
    assert hashlib.sha256(body).hexdigest() == runtime.LOCK_SHA256
    assert ".sba/requirements-linux.lock text eol=lf" in (ROOT / ".gitattributes").read_text(encoding="utf-8")


def host(monkeypatch):
    monkeypatch.setattr(runtime.sys, "platform", "linux")
    monkeypatch.setattr(runtime.platform, "machine", lambda: "x86_64")
    monkeypatch.setattr(runtime.sys, "version_info", (3, 12, 15))


def test_environment_never_inherits_cloud_python_pip_or_proxy_injection(monkeypatch, tmp_path):
    for name in ("PYTHONPATH", "PYTHONSTARTUP", "PIP_INDEX_URL", "PIP_CONFIG_FILE", "HTTP_PROXY",
                 "HTTPS_PROXY", "GITHUB_TOKEN", "AWS_SECRET_ACCESS_KEY", "CLOUDFLARE_API_TOKEN"):
        monkeypatch.setenv(name, "must-not-inherit")
    env = runtime.environment(tmp_path)
    assert "must-not-inherit" not in " ".join(env.values())
    assert env["PIP_CONFIG_FILE"] == runtime.os.devnull


def test_unknown_lock_is_denied_before_directory_or_process(tmp_path, monkeypatch):
    host(monkeypatch)
    lock = tmp_path / "requirements.lock"
    lock.write_bytes(b"requests\n")
    monkeypatch.setattr(runtime, "_run", lambda *_: pytest.fail("unexpected process"))
    with pytest.raises(runtime.PythonRuntimeError, match="LOCK"):
        runtime.prepare_python(lock.resolve(), tmp_path / "output")
    assert not (tmp_path / "output").exists()


@pytest.mark.parametrize("system,machine,version", [("win32", "AMD64", (3, 12)), ("linux", "aarch64", (3, 12)),
                                                   ("linux", "x86_64", (3, 13))])
def test_unsupported_runtime_is_rejected_before_io(monkeypatch, tmp_path, system, machine, version):
    monkeypatch.setattr(runtime.sys, "platform", system)
    monkeypatch.setattr(runtime.platform, "machine", lambda: machine)
    monkeypatch.setattr(runtime.sys, "version_info", version)
    with pytest.raises(runtime.PythonRuntimeError, match="HOST"):
        runtime.prepare_python(tmp_path / "missing.lock", tmp_path / "output")
    assert not (tmp_path / "output").exists()


def test_install_uses_fresh_venv_hashes_binary_only_no_index_and_dependency_check(tmp_path, monkeypatch):
    host(monkeypatch)
    calls = []

    def run(args, output, env, label, timeout):
        calls.append((args, env))
        if label == "venv":
            python = output / "venv/bin/python"
            python.parent.mkdir(parents=True)
            python.write_bytes(b"fixture-copied-interpreter")

    monkeypatch.setattr(runtime, "_run", run)
    output = tmp_path / "output"
    result = runtime.prepare_python((ROOT / ".sba/requirements-linux.lock").resolve(), output.resolve())
    assert result.python == output / "venv/bin/python"
    assert len(calls) == 3
    assert calls[0][0][1:5] == ["-I", "-m", "venv", "--copies"]
    for flag in ("--isolated", "--no-index", "--require-hashes", "--only-binary=:all:", "--no-cache-dir"):
        assert flag in calls[1][0]
    assert calls[2][0][-1] == "check"
    with pytest.raises(runtime.PythonRuntimeError, match="PATH"):
        runtime.prepare_python((ROOT / ".sba/requirements-linux.lock").resolve(), output.resolve())
    assert len(calls) == 3
