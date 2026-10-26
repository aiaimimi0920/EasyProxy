"""只读 hosted 预检不重放部署；失败只输出固定阶段，绝不输出异常或产物正文。"""
import json
from pathlib import Path
import subprocess
import sys
from types import SimpleNamespace

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import aggregator_build
import audit_image
import github_environment
import preflight
import python_runtime
import sealed_box
import sources
import toolchain
from artifact_store import ArtifactError
from diagnostics import StageError


SHA = "a" * 40
ORDER = ["inspection", "tools", "runtime", "image", "sealer", "generation", "workflow", "inspection"]


@pytest.mark.parametrize("failure", [None, *ORDER[:-1]])
def test_read_only_stages_never_call_publish_and_only_report_fixed_diagnostics(tmp_path, monkeypatch, capsys, failure):
    calls = []
    root, output, result = tmp_path / "root", tmp_path / "output", tmp_path / "result.json"
    root.mkdir()
    output.mkdir()

    def stage(name):
        def run(*_args, **kwargs):
            calls.append(name)
            if name == "generation":
                assert kwargs["seed_secrets"] == dict.fromkeys(aggregator_build.SEED_SECRETS, aggregator_build.DISABLED_SEED)
            if name == failure:
                raise RuntimeError("DO_NOT_PRINT_PRIVATE_RESPONSE_OR_SECRET")
            return SimpleNamespace()
        return run

    monkeypatch.setattr(sources, "inspect_checkout", stage("inspection"))
    monkeypatch.setattr(toolchain, "prepare_linux_tools", stage("tools"))
    monkeypatch.setattr(audit_image, "build_runtime", stage("runtime"))
    monkeypatch.setattr(audit_image, "build_image", stage("image"))
    monkeypatch.setattr(audit_image, "image_identity", lambda *_: {})
    monkeypatch.setattr(sealed_box, "build_sealer", stage("sealer"))
    monkeypatch.setattr(aggregator_build, "build_aggregator", stage("generation"))
    monkeypatch.setattr(github_environment, "reviewed_workflow", stage("workflow"))
    code = preflight.inspect_prepared(root, output, SHA, result)
    value = json.loads(result.read_bytes())
    assert code == int(failure is not None) and value["passed"] is (failure is None)
    assert value["sourceSha"] == SHA and value["cloudWrites"] is False
    expected = ORDER if failure is None else ORDER[:ORDER.index(failure) + 1]
    assert calls == expected
    assert "DO_NOT_PRINT" not in result.read_text(encoding="utf-8") + capsys.readouterr().out
    assert not result.read_bytes().startswith(b"\xef\xbb\xbf")
    if failure:
        assert value["stage"] == failure and value["errorCode"] == "EASYPROXY_PREFLIGHT_" + failure.upper()


def test_parent_uses_exact_target_lock_private_python_and_scrubbed_environment(tmp_path, monkeypatch):
    root, output, result = tmp_path / "root", tmp_path / "operation", tmp_path / "result.json"
    root.mkdir()
    monkeypatch.setenv("EASYPROXY_CLOUDFLARE_API_TOKEN", "must-not-be-forwarded")
    monkeypatch.setenv("GITHUB_TOKEN", "must-not-be-forwarded")

    def materialize(target, repository, sha, *, cache_root):
        assert (target, repository, sha, cache_root) == (output / "source", preflight.REPOSITORY, SHA, root)

    def python(lock, destination):
        assert lock == output / "source/.sba/requirements-linux.lock" and destination == output / "python"
        return SimpleNamespace(python=output / "private-python")

    def run(args, **kwargs):
        assert args[0] == str(output / "private-python") and args[1:3] == ["-B", "-s"]
        assert "--prepared" in args and args[args.index("--source-sha") + 1] == SHA
        assert not any("TOKEN" in key or "SECRET" in key or key == "PYTHONPATH" for key in kwargs["env"])
        assert kwargs["cwd"] == output
        preflight.report(result, SHA, "workflow", passed=True)
        return SimpleNamespace(returncode=0)

    monkeypatch.setattr(sources, "materialize_workspace", materialize)
    monkeypatch.setattr(python_runtime, "prepare_python", python)
    monkeypatch.setattr(preflight.subprocess, "run", run)
    assert preflight.prepare(root, output, SHA, result) == 0


def test_existing_report_is_never_replaced(tmp_path):
    result = tmp_path / "result.json"
    result.write_text("original", encoding="utf-8")
    with pytest.raises(FileExistsError):
        preflight.report(result, SHA, "tools", passed=False)
    assert result.read_text(encoding="utf-8") == "original"


def test_fresh_child_imports_only_the_target_checkout_modules(tmp_path):
    output, result = tmp_path / "operation", tmp_path / "result.json"
    target = output / "source/.sba"
    target.mkdir(parents=True)
    modules = {
        "aggregator_build": 'DISABLED_SEED="disabled"\nSEED_SECRETS={"seed"}\ndef build_aggregator(*a,**k): pass\n',
        "audit_image": 'def build_image(*a): pass\ndef build_runtime(*a): pass\ndef image_identity(*a): pass\n',
        "github_environment": 'def reviewed_workflow(*a): return b"target fixture"\n',
        "sealed_box": 'def build_sealer(*a): pass\n',
        "sources": 'def inspect_checkout(*a): pass\n',
        "toolchain": 'def prepare_linux_tools(*a): pass\n',
    }
    for name, body in modules.items():
        (target / (name + ".py")).write_text(body, encoding="utf-8")
    completed = subprocess.run([sys.executable, "-B", str(Path(preflight.__file__).resolve()),
        "--prepared", str(output / "source"), "--source-sha", SHA, "--output", str(output),
        "--report", str(result)], capture_output=True, timeout=30, check=False)
    assert completed.returncode == 0, completed.stderr.decode("utf-8", errors="replace")
    assert json.loads(result.read_bytes())["passed"] is True


def test_parent_source_failure_is_sanitized_and_does_not_launch_child(tmp_path, monkeypatch, capsys):
    def failed(*_args, **_kwargs):
        raise RuntimeError("DO_NOT_PRINT_PRIVATE_GIT_RESPONSE")

    monkeypatch.setattr(sources, "materialize_workspace", failed)
    monkeypatch.setattr(preflight.subprocess, "run", lambda *_args, **_kwargs: pytest.fail("unexpected child"))
    result = tmp_path / "result.json"
    assert preflight.prepare(tmp_path, tmp_path / "output", SHA, result) == 1
    assert json.loads(result.read_bytes())["errorCode"] == "EASYPROXY_PREFLIGHT_SOURCE"
    assert "DO_NOT_PRINT" not in capsys.readouterr().out


@pytest.mark.parametrize("sha,stage", [("not-a-sha", "tools"), (SHA, "private-provider-error")])
def test_untrusted_report_fields_are_rejected_before_write(tmp_path, sha, stage):
    result = tmp_path / "result.json"
    with pytest.raises(ValueError, match="EASYPROXY_PREFLIGHT_INPUT"):
        preflight.report(result, sha, stage, passed=False)
    assert not result.exists()


def test_workflow_has_no_deployment_credentials_or_arbitrary_artifact_upload():
    body = (Path(__file__).resolve().parents[2] / ".github/workflows/sba-preflight.yml").read_text(encoding="utf-8")
    assert "workflow_dispatch:" in body and "persist-credentials: false" in body and "contents: read" in body
    assert "secrets." not in body and "id-token:" not in body and "environment:" not in body
    assert "path: ${{ runner.temp }}/easyproxy-preflight.safe.json" in body


@pytest.mark.parametrize("error,expected", [
    (ArtifactError("EASYPROXY_SBA_ARTIFACT_OUTPUT"), "EASYPROXY_SBA_ARTIFACT_OUTPUT"),
    (StageError("GENERATION_COLLECT"), "EASYPROXY_SBA_GENERATION_COLLECT_UNKNOWN"),
    (ArtifactError("DO_NOT_PRINT_PRIVATE_BODY"), None),
    (RuntimeError("EASYPROXY_SBA_ARTIFACT_OUTPUT"), None),
    (StageError("D1"), None),
])
def test_generation_diagnostic_is_a_target_type_and_finite_constant_only(tmp_path, monkeypatch, capsys, error, expected):
    monkeypatch.setattr(sources, "inspect_checkout", lambda *_: None)
    monkeypatch.setattr(toolchain, "prepare_linux_tools", lambda *_: None)
    monkeypatch.setattr(audit_image, "build_runtime", lambda *_: None)
    monkeypatch.setattr(audit_image, "build_image", lambda *_: None)
    monkeypatch.setattr(audit_image, "image_identity", lambda *_: None)
    monkeypatch.setattr(sealed_box, "build_sealer", lambda *_: None)
    def failed(*_args, **_kwargs):
        raise error
    monkeypatch.setattr(aggregator_build, "build_aggregator", failed)
    result = tmp_path / "result.json"
    assert preflight.inspect_prepared(tmp_path, tmp_path, SHA, result) == 1
    value = json.loads(result.read_bytes())
    assert value.get("diagnosticCode") == expected
    assert value["errorCode"] == "EASYPROXY_PREFLIGHT_GENERATION"
    captured = capsys.readouterr()
    assert "DO_NOT_PRINT" not in result.read_text(encoding="utf-8") + captured.out + captured.err


@pytest.mark.parametrize("stage,passed,diagnostic", [
    ("generation", False, "PRIVATE_DIAGNOSTIC"),
    ("tools", False, "EASYPROXY_SBA_ARTIFACT_OUTPUT"),
    ("generation", True, "EASYPROXY_SBA_ARTIFACT_OUTPUT"),
])
def test_unknown_or_out_of_scope_diagnostic_is_rejected_before_report_write(tmp_path, stage, passed, diagnostic):
    path = tmp_path / "result.json"
    with pytest.raises(ValueError, match="EASYPROXY_PREFLIGHT_INPUT"):
        preflight.report(path, SHA, stage, passed=passed, diagnostic_code=diagnostic)
    assert not path.exists()


def test_legacy_target_failure_does_not_import_a_new_diagnostics_dependency(tmp_path):
    output, result = tmp_path / "operation", tmp_path / "result.json"
    target = output / "source/.sba"
    target.mkdir(parents=True)
    modules = {
        "aggregator_build": 'from artifact_store import ArtifactError\nDISABLED_SEED="disabled"\nSEED_SECRETS={"seed"}\n'
            'def build_aggregator(*a,**k): raise ArtifactError("EASYPROXY_SBA_ARTIFACT_OUTPUT")\n',
        "artifact_store": 'class ArtifactError(RuntimeError): pass\n',
        "diagnostics": 'raise RuntimeError("DIAGNOSTICS_IMPORT_FORBIDDEN_FOR_LEGACY_TARGET")\n',
        "audit_image": 'def build_image(*a): pass\ndef build_runtime(*a): pass\ndef image_identity(*a): pass\n',
        "github_environment": 'def reviewed_workflow(*a): pass\n',
        "sealed_box": 'def build_sealer(*a): pass\n',
        "sources": 'def inspect_checkout(*a): pass\n',
        "toolchain": 'def prepare_linux_tools(*a): pass\n',
    }
    for name, body in modules.items():
        (target / (name + ".py")).write_text(body, encoding="utf-8")
    completed = subprocess.run([sys.executable, "-B", str(Path(preflight.__file__).resolve()),
        "--prepared", str(output / "source"), "--source-sha", SHA, "--output", str(output),
        "--report", str(result)], capture_output=True, timeout=30, check=False)
    assert completed.returncode == 1
    value = json.loads(result.read_bytes())
    assert value["errorCode"] == "EASYPROXY_PREFLIGHT_GENERATION"
    assert value["diagnosticCode"] == "EASYPROXY_SBA_ARTIFACT_OUTPUT"
    assert b"DIAGNOSTICS_IMPORT_FORBIDDEN" not in completed.stdout + completed.stderr


@pytest.mark.parametrize("artifact,body,reason", [
    ("subs/mixed.txt", b"not-base64!", "base64"),
    ("subs/mixed.txt", b"c3M6Ly9wcml2YXRlIHNlY3JldAo=", "uri-line-format"),
    ("subs/singbox.json", b"PRIVATE_INVALID_JSON", "json"),
    ("subs/singbox.json", b'{"outbounds":[]}', "singbox-shape"),
    ("internal/crawledsubs.json", b"{}", "json-empty"),
    ("subs/clash.yaml", b"PRIVATE_YAML", "clash-shape"),
    ("subs/clash.yaml", b"proxies: []", "clash-empty"),
    ("subs/clash.yaml", b'proxies: ["PRIVATE\\q"]', "yaml-unknown-escape"),
    ("subs/clash.yaml", b"proxies:\n\tPRIVATE", "yaml-forbidden-token"),
    ("subs/clash.yaml", b"proxies: !PRIVATE []", "yaml-constructor"),
])
def test_legacy_validator_failure_only_exports_fixed_artifact_and_reason(tmp_path, artifact, body, reason):
    namespace = {"ArtifactError": ArtifactError}
    exec(compile("def _validate(key, body):\n    raise ArtifactError('EASYPROXY_SBA_ARTIFACT_FORMAT')\n",
                 str(tmp_path / ".sba/aggregator_publish.py"), "exec"), namespace)
    try:
        namespace["_validate"](artifact, body)
    except ArtifactError as error:
        value = preflight.generation_artifact_diagnostic(error, tmp_path)
    assert value == {"artifact": artifact, "reason": reason}
    assert "PRIVATE" not in json.dumps(value)


@pytest.mark.parametrize("field", ["artifact", "reason"])
def test_private_artifact_diagnostic_fields_are_rejected_before_write(tmp_path, field):
    value = {"artifact": "subs/mixed.txt", "reason": "base64"}
    value[field] = "DO_NOT_PRINT_PRIVATE"
    path = tmp_path / "result.json"
    with pytest.raises(ValueError, match="EASYPROXY_PREFLIGHT_INPUT"):
        preflight.report(path, SHA, "generation", passed=False, artifact_diagnostic=value)
    assert not path.exists()


def test_artifact_diagnostic_never_reads_another_source_or_unknown_key(tmp_path):
    for filename, key in ((tmp_path / "other/aggregator_publish.py", "subs/mixed.txt"),
                          (tmp_path / ".sba/aggregator_publish.py", "PRIVATE_KEY")):
        namespace = {"ArtifactError": ArtifactError}
        exec(compile("def _validate(key, body):\n    raise ArtifactError('PRIVATE_BODY')\n", str(filename), "exec"), namespace)
        try:
            namespace["_validate"](key, b"PRIVATE_BODY")
        except ArtifactError as error:
            assert preflight.generation_artifact_diagnostic(error, tmp_path) is None
