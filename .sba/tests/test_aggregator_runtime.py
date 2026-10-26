"""隔离副本补丁和固定工具拒绝路径；不运行上游生成或云操作。"""
import hashlib
from pathlib import Path
import shutil
import subprocess
import sys

import pytest

from test_sources import sources
import aggregator_runtime as runtime
from artifact_store import ArtifactError


ROOT = Path(__file__).resolve().parents[2]


def approved_utils(workspace):
    body = subprocess.run(["git", "-C", str(ROOT / "upstreams/aggregator"), "show",
        runtime.AGGREGATOR_SHA + ":subscribe/utils.py"], capture_output=True, check=True).stdout
    target = workspace / "subscribe/utils.py"
    target.parent.mkdir(parents=True)
    target.write_bytes(body)
    return target


def test_exact_gitlink_patch_only_restores_default_tls_and_is_not_reapplied(tmp_path):
    path = approved_utils(tmp_path)
    before = path.read_bytes()
    receipt = runtime.patch_source_tls(tmp_path.resolve())
    assert path.read_bytes() == before.replace(runtime.TLS_BEFORE, runtime.TLS_AFTER, 1)
    assert receipt["beforeSha256"] == runtime.UTILS_SHA256
    assert receipt["afterSha256"] == hashlib.sha256(path.read_bytes()).hexdigest()
    with pytest.raises(ArtifactError, match="RUNTIME_PATCH"):
        runtime.patch_source_tls(tmp_path.resolve())


def test_changed_gitlink_file_is_rejected_without_rewriting(tmp_path):
    path = approved_utils(tmp_path)
    body = path.read_bytes() + b"\n# unexpected drift\n"
    path.write_bytes(body)
    with pytest.raises(ArtifactError, match="RUNTIME_PATCH"):
        runtime.patch_source_tls(tmp_path.resolve())
    assert path.read_bytes() == body


def test_all_tools_must_match_before_any_source_is_patched(tmp_path, monkeypatch):
    monkeypatch.setattr(runtime.sys, "platform", "linux")
    monkeypatch.setattr(runtime.platform, "machine", lambda: "x86_64")
    source = approved_utils(tmp_path)
    for relative in runtime.BINARIES:
        path = tmp_path / relative
        path.parent.mkdir(parents=True)
        path.write_bytes(b"not-the-approved-executable")
    with pytest.raises(ArtifactError, match="RUNTIME_TOOL"):
        runtime.prepare_runtime(tmp_path.resolve(), runtime.AGGREGATOR_SHA)
    assert hashlib.sha256(source.read_bytes()).hexdigest() == runtime.UTILS_SHA256


@pytest.mark.parametrize("system,machine", [("win32", "AMD64"), ("linux", "aarch64")])
def test_unapproved_host_cannot_enable_execution(tmp_path, monkeypatch, system, machine):
    monkeypatch.setattr(runtime.sys, "platform", system)
    monkeypatch.setattr(runtime.platform, "machine", lambda: machine)
    with pytest.raises(ArtifactError, match="RUNTIME_HOST"):
        runtime.prepare_runtime(tmp_path.resolve(), runtime.AGGREGATOR_SHA)


def test_runtime_receipt_and_bytes_are_rechecked_after_generation(tmp_path, monkeypatch):
    monkeypatch.setattr(runtime.sys, "platform", "linux")
    monkeypatch.setattr(runtime.platform, "machine", lambda: "x86_64")
    approved_utils(tmp_path)
    raw = subprocess.check_output(["git", "-C", str(ROOT / "upstreams/aggregator"), "show",
        runtime.AGGREGATOR_SHA + ":subscribe/workflow.py"])
    (tmp_path / "subscribe/workflow.py").write_bytes(raw)
    pins = {}
    for relative in runtime.BINARIES:
        path = tmp_path / relative
        path.parent.mkdir(parents=True)
        body = ("fixture-" + relative).encode()
        path.write_bytes(body)
        pins[relative] = hashlib.sha256(body).hexdigest()
    monkeypatch.setattr(runtime, "BINARIES", pins)
    receipt = runtime.prepare_runtime(tmp_path.resolve(), runtime.AGGREGATOR_SHA)
    runtime.verify_runtime(tmp_path.resolve(), receipt)
    (tmp_path / next(iter(pins))).write_bytes(b"changed-during-generation")
    with pytest.raises(ArtifactError, match="RUNTIME_TOOL"):
        runtime.verify_runtime(tmp_path.resolve(), receipt)


@pytest.mark.skipif(sys.platform != "linux", reason="approved converter is Linux/x64")
def test_real_approved_converter_numeric_password_passes_isolated_clash_loader(tmp_path):
    import aggregator_publish as publish
    from test_aggregator_publish import BODIES
    work = tmp_path / "subconverter"
    shutil.copytree(ROOT / "upstreams/aggregator/subconverter", work)
    binary = work / "subconverter-linux-amd"
    assert hashlib.sha256(binary.read_bytes()).hexdigest() == runtime.BINARIES["subconverter/subconverter-linux-amd"]
    binary.chmod(0o700)
    (work / "fixture.yaml").write_text('proxies: [{name: controlled-fixture, type: ss, server: 192.0.2.1, '
        'port: 443, cipher: aes-128-gcm, password: "123456"}]\n', encoding="utf-8")
    (work / "generate.ini").write_text("[probe]\npath=probe.yaml\nurl=fixture.yaml\nexpand=false\n"
        "target=clash\nlist=true\nemoji=true\nadd_emoji=true\n", encoding="utf-8")
    completed = subprocess.run([str(binary), "-g", "--artifact", "probe"], cwd=work,
                               capture_output=True, timeout=30, check=False)
    assert completed.returncode == 0
    body = (work / "probe.yaml").read_bytes()
    assert b"password: !<str> 123456" in body
    with pytest.raises(publish.yaml.constructor.ConstructorError):
        publish.yaml.safe_load(body)
    bodies = {**BODIES, "subs/clash.yaml": body}
    publish._validate(bodies, publish.RAW)
    assert bodies["subs/clash.yaml"] == body
    assert publish.yaml.load(body, Loader=publish.ClashSafeLoader)["proxies"][0]["password"] == "123456"
