"""真实 stdin helper 与 Python 适配；秘密不出现在 argv/env/日志或失败消息。"""
import base64
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from sealed_box import SealError, Sealer, decode_base64, secret_value


@pytest.mark.parametrize("value", [None, "", "verification-not-applicable", "\ud800", "x" * 513, "bad\nvalue", "bad\rvalue", "\x00"])
def test_unusable_secret_values_are_rejected(value):
    assert secret_value(value) is False


def test_unicode_secret_limit_is_in_utf8_bytes_not_characters():
    assert secret_value("中" * 170) is True
    assert secret_value("中" * 171) is False


@pytest.fixture
def helper():
    configured = os.environ.get("EASYPROXY_TEST_SEALER")
    if not configured:
        pytest.skip("Explicit real sba-seal executable required")
    executable = Path(configured).resolve(strict=True)
    return Sealer(executable, "a" * 40, hashlib.sha256(executable.read_bytes()).hexdigest())


def test_real_helper_uses_stdin_and_sanitized_environment_without_plaintext_output(helper, monkeypatch):
    original = subprocess.run
    calls = []
    plaintext = "fixture-secret-不进入命令行"
    key = base64.b64encode(bytes(range(32))).decode()
    monkeypatch.setenv("EASYPROXY_CLOUDFLARE_API_TOKEN", "must-not-be-inherited")

    def run(args, **kwargs):
        assert args == [str(helper.executable)] and plaintext not in json.dumps(args)
        assert "EASYPROXY_CLOUDFLARE_API_TOKEN" not in kwargs["env"]
        assert json.loads(kwargs["input"]) == {"publicKey": key, "plaintext": plaintext}
        calls.append(args)
        return original(args, **kwargs)

    monkeypatch.setattr(subprocess, "run", run)
    first, second = helper.encrypt(key, plaintext), helper.encrypt(key, plaintext)
    assert first != second and plaintext not in first
    assert len(calls) == 2
    assert len(decode_base64(first, len(plaintext.encode()) + 48)) > 48


def test_modified_executable_is_rejected_before_execution(helper, tmp_path):
    path = tmp_path / "fake"
    path.write_bytes(b"not executable")
    altered = Sealer(path, helper.source_sha, helper.sha256)
    with pytest.raises(SealError):
        altered.encrypt(base64.b64encode(bytes(range(32))).decode(), "fixture-secret")
