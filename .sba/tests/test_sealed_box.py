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
from sealed_box import SealError, Sealer, decode_base64, secret_value, single_line_secret


@pytest.mark.parametrize("value", [
    None, "", "verification-not-applicable", "repair-not-applicable", "\ud800",
    "x" * 16385, "\x00", "bad\x0bvalue", "bad\x0cvalue", "bad\x1bvalue", "bad\x7fvalue",
])
def test_unusable_secret_values_are_rejected(value):
    assert secret_value(value) is False


def test_secret_value_supports_16kib_utf8_and_multiline_while_single_line_tokens_stay_bounded():
    assert secret_value("line1\nline2\r\n\tvalue") is True
    assert secret_value("中" * 5461) is True
    assert secret_value("中" * 5462) is False
    assert single_line_secret("中" * 170) is True
    assert single_line_secret("中" * 171) is False
    assert single_line_secret("line\nbreak") is False
    assert single_line_secret("tab\tvalue") is False


@pytest.fixture
def helper():
    configured = os.environ.get("EASYPROXY_TEST_SEALER")
    if not configured:
        pytest.skip("Explicit real sba-seal executable required")
    executable = Path(configured).resolve(strict=True)
    return Sealer(executable, "a" * 40, hashlib.sha256(executable.read_bytes()).hexdigest())


@pytest.mark.parametrize("plaintext", [
    "fixture-secret-不进入命令行",
    "multiline-secret\nline2\r\n\tvalue",
    "x" * 16384,
])
def test_real_helper_uses_stdin_and_sanitized_environment_without_plaintext_output(helper, monkeypatch, plaintext):
    original = subprocess.run
    calls = []
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
    assert len(decode_base64(first, len(plaintext.encode("utf-8")) + 48)) > 48


def test_modified_executable_is_rejected_before_execution(helper, tmp_path):
    path = tmp_path / "fake"
    path.write_bytes(b"not executable")
    altered = Sealer(path, helper.source_sha, helper.sha256)
    with pytest.raises(SealError):
        altered.encrypt(base64.b64encode(bytes(range(32))).decode(), "fixture-secret")
