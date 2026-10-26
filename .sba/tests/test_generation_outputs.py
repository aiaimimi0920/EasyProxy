"""生成输出读取诊断限定固定名称和原因，不读取越界文件或重试生成。"""
import json
import os
from pathlib import Path
import stat
import sys
from types import SimpleNamespace

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import aggregator_build as build
from artifact_store import ArtifactError
from diagnostics import StageError, phase, safe_error_code
import preflight
from test_generation_diagnostics import fixture

NAMES = ("public-clash", "public-singbox", "public-mixed", "crawledsubs")
REASONS = ("missing", "not-regular", "symlink", "path-alias", "stat-error", "path-error", "permission", "read-error")
PRIVATE = "DO_NOT_PRINT_PRIVATE_PATH_TOKEN_OR_BODY"
CODE = "EASYPROXY_SBA_GENERATION_OUTPUTS_UNKNOWN"


@pytest.mark.parametrize("name", NAMES)
@pytest.mark.parametrize("reason", REASONS)
def test_each_fixed_output_read_failure_is_classified_without_body_or_retry(tmp_path, monkeypatch, capsys, name, reason):
    path = tmp_path / (name + ".artifact")
    path.write_bytes(PRIVATE.encode())
    original_stat, original_resolve, original_open = Path.lstat, Path.resolve, Path.open
    reads = []

    def lstat(value, *args, **kwargs):
        if value == path:
            if reason == "missing": raise FileNotFoundError(PRIVATE)
            if reason == "stat-error": raise OSError(PRIVATE)
            if reason in ("not-regular", "symlink"):
                return SimpleNamespace(st_mode=stat.S_IFIFO if reason == "not-regular" else stat.S_IFLNK)
        return original_stat(value, *args, **kwargs)

    def resolve(value, *args, **kwargs):
        if value == path:
            if reason == "path-error": raise RuntimeError(PRIVATE)
            if reason == "path-alias": return tmp_path / PRIVATE
        return original_resolve(value, *args, **kwargs)

    def open_file(value, *args, **kwargs):
        if value == path:
            reads.append(value)
            if reason == "permission": raise PermissionError(PRIVATE)
            if reason == "read-error": raise OSError(PRIVATE)
        return original_open(value, *args, **kwargs)

    monkeypatch.setattr(Path, "lstat", lstat)
    monkeypatch.setattr(Path, "resolve", resolve)
    monkeypatch.setattr(Path, "open", open_file)
    with pytest.raises(StageError) as caught:
        with phase("GENERATION_OUTPUTS"):
            build._read_output(tmp_path, name)
    assert safe_error_code(caught.value) == CODE
    value = preflight.generation_output_diagnostic(caught.value)
    assert value == {"artifact": name + ".artifact", "reason": reason}
    assert len(reads) == int(reason in ("permission", "read-error"))
    captured = capsys.readouterr()
    assert PRIVATE not in str(caught.value) + json.dumps(value) + captured.out + captured.err


@pytest.mark.parametrize("name", NAMES)
@pytest.mark.parametrize("body", [b"", b"unchanged-body\x00\xff"])
def test_success_reads_each_body_once_without_normalization(tmp_path, monkeypatch, name, body):
    path = tmp_path / (name + ".artifact")
    path.write_bytes(body)
    original = Path.open
    calls = []
    def open_file(value, *args, **kwargs):
        calls.append(value)
        return original(value, *args, **kwargs)
    monkeypatch.setattr(Path, "open", open_file)
    assert build._read_output(tmp_path, name) == body
    assert calls == [path]


def test_read_limit_is_unchanged_and_validation_still_owns_empty_or_oversized_bodies(tmp_path, monkeypatch):
    monkeypatch.setattr(build, "MAX_BYTES", 4)
    (tmp_path / "public-mixed.artifact").write_bytes(b"123456789")
    assert build._read_output(tmp_path, "public-mixed") == b"12345"


@pytest.mark.parametrize("name", ["../" + PRIVATE, "/" + PRIVATE, "unknown", "public-clash\n", 1])
def test_unknown_output_name_is_rejected_before_any_file_observation(tmp_path, monkeypatch, name):
    monkeypatch.setattr(Path, "lstat", lambda *_: pytest.fail("unexpected file observation"))
    with pytest.raises(ArtifactError):
        build._read_output(tmp_path, name)


@pytest.mark.parametrize("parent_link", [False, True])
def test_real_symlink_never_reads_the_external_target(tmp_path, monkeypatch, parent_link):
    external = tmp_path / "private"
    external.mkdir()
    target = external / "public-clash.artifact"
    target.write_text(PRIVATE, encoding="utf-8")
    output = tmp_path / "outputs"
    try:
        if parent_link:
            output.symlink_to(external, target_is_directory=True)
        else:
            output.mkdir()
            (output / target.name).symlink_to(target)
    except OSError:
        if os.name == "nt": pytest.skip("Windows symlink privilege unavailable; native Linux gate is required")
        raise
    monkeypatch.setattr(Path, "open", lambda *_args, **_kwargs: pytest.fail("external body must not be read"))
    with pytest.raises(StageError) as caught:
        with phase("GENERATION_OUTPUTS"):
            build._read_output(output, "public-clash")
    assert preflight.generation_output_diagnostic(caught.value) == {
        "artifact": target.name, "reason": "path-alias" if parent_link else "symlink"}


def test_actual_generation_failure_preserves_single_run_and_stops_before_conversion(tmp_path, monkeypatch):
    root, output, calls = fixture(tmp_path, monkeypatch, "outputs")
    with pytest.raises(StageError) as caught:
        build.build_aggregator(root, "owner/repo", "a" * 40, output,
            seed_secrets=dict.fromkeys(build.SEED_SECRETS, build.DISABLED_SEED))
    assert safe_error_code(caught.value) == CODE
    assert preflight.generation_output_diagnostic(caught.value) == {"artifact": "public-clash.artifact", "reason": "missing"}
    assert calls == ["config", "generation"]


@pytest.mark.parametrize("reason", REASONS)
def test_preflight_report_accepts_only_fixed_output_diagnostic_on_output_failure(tmp_path, reason):
    result = tmp_path / "result.json"
    value = {"artifact": "public-clash.artifact", "reason": reason}
    preflight.report(result, "a" * 40, "generation", passed=False, diagnostic_code=CODE, output_diagnostic=value)
    assert json.loads(result.read_bytes())["outputDiagnostic"] == value


@pytest.mark.parametrize("mutation", ["artifact", "reason", "extra", "wrong-code", "wrong-stage", "success", "both"])
def test_output_report_rejects_unknown_or_out_of_scope_fields_before_write(tmp_path, mutation):
    value = {"artifact": "public-clash.artifact", "reason": "missing"}
    kwargs = {"diagnostic_code": CODE, "output_diagnostic": value}
    stage, passed = "generation", False
    if mutation in ("artifact", "reason"): value[mutation] = PRIVATE
    if mutation == "extra": value["private"] = PRIVATE
    if mutation == "wrong-code": kwargs["diagnostic_code"] = "EASYPROXY_SBA_GENERATION_COLLECT_UNKNOWN"
    if mutation == "wrong-stage": stage = "tools"
    if mutation == "success": passed = True
    if mutation == "both": kwargs["artifact_diagnostic"] = {"artifact": "subs/mixed.txt", "reason": "base64"}
    result = tmp_path / "result.json"
    with pytest.raises(ValueError):
        preflight.report(result, "a" * 40, stage, passed=passed, **kwargs)
    assert not result.exists()


def test_output_diagnostic_rejects_lookalikes_subclasses_mutated_fields_and_legacy_targets(monkeypatch):
    error = build.GenerationOutputError("public-clash", "missing")
    class Lookalike(RuntimeError): pass
    fake = Lookalike(CODE)
    fake.artifact, fake.reason = error.artifact, error.reason
    assert preflight.generation_output_diagnostic(fake) is None
    class Child(build.GenerationOutputError): pass
    assert preflight.generation_output_diagnostic(Child("public-clash", "missing")) is None
    error.artifact = PRIVATE
    assert preflight.generation_output_diagnostic(error) is None
    monkeypatch.setitem(sys.modules, "aggregator_build", SimpleNamespace())
    assert preflight.generation_output_diagnostic(error) is None


@pytest.mark.parametrize("name,reason", [(PRIVATE, "missing"), ("public-clash", PRIVATE)])
def test_typed_error_rejects_private_names_or_reasons(name, reason):
    with pytest.raises(ArtifactError) as caught:
        build.GenerationOutputError(name, reason)
    assert PRIVATE not in str(caught.value)


def test_diagnostic_extraction_has_no_second_file_observation(tmp_path, monkeypatch):
    with pytest.raises(StageError) as caught:
        with phase("GENERATION_OUTPUTS"):
            build._read_output(tmp_path, "public-singbox")
    monkeypatch.setattr(Path, "lstat", lambda *_: pytest.fail("unexpected second observation"))
    monkeypatch.setattr(Path, "open", lambda *_args, **_kwargs: pytest.fail("unexpected body read"))
    assert preflight.generation_output_diagnostic(caught.value) == {"artifact": "public-singbox.artifact", "reason": "missing"}


def test_actual_stream_read_error_is_not_retried(tmp_path, monkeypatch):
    (tmp_path / "public-mixed.artifact").write_bytes(b"fixture")
    calls = []
    class Stream:
        def __enter__(self): return self
        def __exit__(self, *_): pass
        def read(self, limit):
            calls.append(limit)
            raise OSError(PRIVATE)
    monkeypatch.setattr(Path, "open", lambda *_args, **_kwargs: Stream())
    with pytest.raises(StageError) as caught:
        with phase("GENERATION_OUTPUTS"):
            build._read_output(tmp_path, "public-mixed")
    assert calls == [build.MAX_BYTES + 1]
    assert preflight.generation_output_diagnostic(caught.value) == {"artifact": "public-mixed.artifact", "reason": "read-error"}


def test_inspect_prepared_exports_the_output_diagnostic_and_stops_before_workflow(tmp_path, monkeypatch, capsys):
    import sources, toolchain, audit_image, sealed_box, github_environment
    for module, names in ((sources, ("inspect_checkout",)), (toolchain, ("prepare_linux_tools",)),
            (audit_image, ("build_runtime", "build_image", "image_identity")), (sealed_box, ("build_sealer",))):
        for name in names: monkeypatch.setattr(module, name, lambda *_: None)
    calls = []
    def generate(*_args, **kwargs):
        calls.append("generation")
        assert kwargs["seed_secrets"] == dict.fromkeys(build.SEED_SECRETS, build.DISABLED_SEED)
        with phase("GENERATION"):
            with phase("GENERATION_OUTPUTS"):
                build._read_output(tmp_path, "crawledsubs")
    monkeypatch.setattr(build, "build_aggregator", generate)
    monkeypatch.setattr(github_environment, "reviewed_workflow", lambda *_: pytest.fail("workflow after failure"))
    result = tmp_path / "result.json"
    assert preflight.inspect_prepared(tmp_path, tmp_path, "a" * 40, result) == 1
    value = json.loads(result.read_bytes())
    assert value["diagnosticCode"] == CODE and value["cloudWrites"] is False
    assert value["outputDiagnostic"] == {"artifact": "crawledsubs.artifact", "reason": "missing"}
    assert calls == ["generation"]
    assert PRIVATE not in result.read_text(encoding="utf-8") + capsys.readouterr().out
