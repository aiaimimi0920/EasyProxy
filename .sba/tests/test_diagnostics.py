"""固定阶段错误保持 UNKNOWN 和原请求身份，不打印内部异常或替换已有回执。"""
import json
from contextlib import nullcontext
from pathlib import Path
import sys

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import execute as entry
from diagnostics import STAGES, StageError, phase, safe_error_code
from test_pristine_repair import request_fixture
from test_request import parse


@pytest.mark.parametrize("stage", sorted(STAGES))
def test_each_stage_maps_to_a_fixed_safe_code(stage):
    with pytest.raises(StageError) as caught:
        with phase(stage):
            raise RuntimeError("DO_NOT_PRINT_SECRET_PROVIDER_BODY")
    assert safe_error_code(caught.value) == "EASYPROXY_SBA_" + stage + "_UNKNOWN"
    assert "DO_NOT_PRINT" not in str(caught.value)


def test_nested_stage_keeps_the_precise_inner_stage_and_arbitrary_errors_stay_generic():
    with pytest.raises(StageError) as caught:
        with phase("PUBLISH"):
            with phase("D1"):
                raise RuntimeError("private")
    assert safe_error_code(caught.value) == "EASYPROXY_SBA_D1_UNKNOWN"
    assert safe_error_code(RuntimeError("PRIVATE_TOKEN")) == "EASYPROXY_SBA_FAILED_OR_UNKNOWN"
    with pytest.raises(ValueError):
        with phase("UNTRUSTED_PRIVATE_STAGE"):
            pytest.fail("unreachable")


@pytest.mark.parametrize("error,expected", [(StageError("TOOLS"), "EASYPROXY_SBA_TOOLS_UNKNOWN"),
                                         (RuntimeError("DO_NOT_PRINT_SECRET"), "EASYPROXY_SBA_FAILED_OR_UNKNOWN")])
def test_entry_receipt_preserves_repair_identity_without_exception_body(tmp_path, monkeypatch, capsys, error, expected):
    request = parse(request_fixture())
    path, result = tmp_path / "request.json", tmp_path / "result.json"
    path.write_text(json.dumps(request_fixture()), encoding="utf-8")
    monkeypatch.setattr(entry, "_git", lambda *_: request.source_sha)
    monkeypatch.setattr(entry, "manifest_release", lambda *_: request.candidate)
    monkeypatch.setattr(entry, "action_deadline", lambda *_: nullcontext())
    def failed(*_args, **_kwargs): raise error
    monkeypatch.setattr(entry, "execute", failed)
    monkeypatch.setattr(sys, "argv", ["execute.py", "--request", str(path), "--result", str(result),
                                    "--output", str(tmp_path / "output"), "--deadline", "9999999999"])
    assert entry.main() == 1
    value = json.loads(result.read_bytes())
    assert value["status"] == "unknown" and value["errorCode"] == expected
    assert value["action"] == "repair" and value["taskId"] == request.task_id and value["sourceSha"] == request.source_sha
    assert "DO_NOT_PRINT" not in result.read_text(encoding="utf-8") + capsys.readouterr().out


def test_entry_never_replaces_an_existing_parent_or_child_receipt(tmp_path, monkeypatch):
    request = parse(request_fixture())
    path, result = tmp_path / "request.json", tmp_path / "result.json"
    path.write_text(json.dumps(request_fixture()), encoding="utf-8")
    original = b'{"status":"unknown","immutable":"parent-or-child-evidence"}\n'
    result.write_bytes(original)
    monkeypatch.setattr(entry, "_git", lambda *_: request.source_sha)
    monkeypatch.setattr(entry, "manifest_release", lambda *_: request.candidate)
    monkeypatch.setattr(entry, "action_deadline", lambda *_: nullcontext())
    def failed(*_args, **_kwargs): raise StageError("PRISTINE")
    monkeypatch.setattr(entry, "execute", failed)
    monkeypatch.setattr(sys, "argv", ["execute.py", "--request", str(path), "--result", str(result),
                                    "--output", str(tmp_path / "output"), "--deadline", "9999999999"])
    assert entry.main() == 1
    assert result.read_bytes() == original
