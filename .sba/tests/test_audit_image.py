"""固定镜像来源/平台/执行 ID；所有负例均禁止隐式 build/pull。"""
from dataclasses import replace
import hashlib
import json
from pathlib import Path
from types import SimpleNamespace

import pytest

from test_sources import sources
import audit_image as image
import aggregator_audit as audit
from artifact_store import ArtifactError
from test_aggregator_publish import BODIES, audit as audit_receipt, setup, state, OLD
from aggregator_publish import Candidate


FIXTURE = image.AuditImage("sha256:" + "f" * 64, "fixture/easyproxy", "a" * 40, "9" * 64)


def metadata(value=FIXTURE):
    return {"Id": value.image_id, "Os": "linux", "Architecture": "amd64", "Config": {"Labels": image.labels(value)}}


@pytest.mark.parametrize("mutation", [lambda row: row.update(Id="sha256:" + "b" * 64),
    lambda row: row.update(Os="windows"), lambda row: row.update(Architecture="arm64"),
    lambda row: row["Config"]["Labels"].update({"org.opencontainers.image.revision": "c" * 40}),
    lambda row: row["Config"]["Labels"].update({"easyproxy.audit.base": "python:latest"})])
def test_inspect_denies_id_platform_source_and_base_drift(monkeypatch, mutation):
    row = metadata()
    mutation(row)
    calls = []

    def docker(args, env):
        calls.append(args)
        return json.dumps([row]).encode()

    monkeypatch.setattr(image, "_docker", docker)
    with pytest.raises(image.AuditImageError):
        image.verify_image(FIXTURE, {})
    assert calls == [["image", "inspect", FIXTURE.image_id]]


def test_mutable_tag_is_rejected_before_docker(monkeypatch):
    monkeypatch.setattr(image, "_docker", lambda *_: pytest.fail("unexpected Docker operation"))
    with pytest.raises(image.AuditImageError):
        image.verify_image(replace(FIXTURE, image_id="easyproxy/audit:latest"), {})


def test_image_build_is_explicit_minimal_and_binds_actual_id_to_binary_and_source(tmp_path, monkeypatch):
    binary = tmp_path / "binary/easy-proxy"
    binary.parent.mkdir()
    binary.write_bytes(b"fixture-runtime")
    built = image.BuiltRuntime(binary.resolve(), FIXTURE.repository, FIXTURE.source_sha,
                               hashlib.sha256(binary.read_bytes()).hexdigest())
    output = (tmp_path / "image").resolve()
    calls = []

    def docker(args, env, timeout=60):
        calls.append(args)
        if args[0] == "build":
            (output / "image-id").write_text(FIXTURE.image_id, encoding="utf-8")
            return b""
        expected = replace(FIXTURE, binary_sha256=built.binary_sha256)
        return json.dumps([metadata(expected)]).encode()

    monkeypatch.setattr(image, "_docker", docker)
    result = image.build_image(built, output)
    assert result.image_id == FIXTURE.image_id and result.binary_sha256 == built.binary_sha256
    assert calls[0][:5] == ["build", "--network", "none", "--platform", "linux/amd64"]
    dockerfile = (output / "Dockerfile").read_text(encoding="utf-8")
    assert image.BASE_IMAGE in dockerfile and "RUN " not in dockerfile and "apt" not in dockerfile
    assert "COPY easy-proxy " in dockerfile and "frontend" not in dockerfile
    assert calls[1] == ["image", "inspect", result.image_id]
    assert json.loads((output / "image-provenance.json").read_text())["sourceSha"] == built.source_sha


def test_changed_runtime_is_denied_before_context_creation(tmp_path, monkeypatch):
    binary = tmp_path / "runtime"
    binary.write_bytes(b"changed")
    built = image.BuiltRuntime(binary.resolve(), FIXTURE.repository, FIXTURE.source_sha, "9" * 64)
    monkeypatch.setattr(image, "_docker", lambda *_: pytest.fail("unexpected Docker operation"))
    with pytest.raises(image.AuditImageError):
        image.build_image(built, tmp_path / "output")
    assert not (tmp_path / "output").exists()


@pytest.mark.parametrize("bad_summary", [False, True])
def test_candidate_audit_never_builds_and_requires_actual_image_receipt(tmp_path, monkeypatch, bad_summary):
    owner, objects, public = setup()
    permit = owner.begin("deploy", OLD)
    candidate = Candidate(permit.operation_id, BODIES)
    root = tmp_path / "root"
    root.mkdir()
    monkeypatch.setattr(audit, "inspect_checkout", lambda *_: [{"path": "upstreams/aggregator", "sourceSha": "e" * 40}])
    monkeypatch.setattr(audit, "inspect_gitlink_checkout", lambda *_: None)
    inspected = []
    monkeypatch.setattr(audit, "verify_image", lambda value, env: inspected.append(value))
    for key, body in BODIES.items():
        objects.put(f"candidate/{permit.operation_id}/{key}", body, None)
    calls = []

    def run(args, cwd, env, **kwargs):
        calls.append(args)
        body = json.loads(audit_receipt(permit, public).body)
        if bad_summary:
            body["image_identity"]["imageId"] = "sha256:" + "0" * 64
        (cwd / "audit.private.json").write_text(json.dumps(body), encoding="utf-8")

    monkeypatch.setattr(audit, "run_audit", run)
    monkeypatch.setattr(audit, "container_owner", lambda *_: None)
    output = (tmp_path / "audit").resolve()
    if bad_summary:
        with pytest.raises(ArtifactError, match="AUDIT_IMAGE"):
            audit.audit_candidate(owner, permit, candidate, public, root.resolve(), output, image=FIXTURE)
    else:
        result = audit.audit_candidate(owner, permit, candidate, public, root.resolve(), output, image=FIXTURE)
        assert result.image == FIXTURE and len(inspected) == 2
    assert len(calls) == 1 and "--build-if-missing" not in calls[0]
    assert calls[0][calls[0].index("--image") + 1] == FIXTURE.image_id
    assert calls[0][calls[0].index("--expected-source-sha") + 1] == FIXTURE.source_sha
    assert state(owner.store)["writer"] is not None


def test_audit_image_credentials_and_daemon_overrides_are_not_inherited(tmp_path, monkeypatch):
    for name in ("DOCKER_HOST", "DOCKER_CONFIG", "GITHUB_TOKEN", "AWS_SECRET_ACCESS_KEY", "CLOUDFLARE_API_TOKEN"):
        monkeypatch.setenv(name, "must-not-inherit")
    assert "must-not-inherit" not in " ".join(image.environment(tmp_path).values())


def test_cli_strict_image_and_container_identity_never_build_or_pull(tmp_path, monkeypatch):
    monkeypatch.syspath_prepend(str(Path(__file__).resolve().parents[2]))
    from scripts import easyproxy_audit_image as cli
    calls = []

    def run(args, **kwargs):
        calls.append(args)
        return SimpleNamespace(returncode=0, stdout=json.dumps([metadata()]) if args[1] == "image" else FIXTURE.image_id + "\n")

    monkeypatch.setattr(cli.subprocess, "run", run)
    assert cli.inspect_image(FIXTURE.image_id, FIXTURE.source_sha)["imageId"] == FIXTURE.image_id
    cli.verify_container("fixture-container", FIXTURE.image_id)
    assert calls == [["docker", "image", "inspect", FIXTURE.image_id],
                     ["docker", "inspect", "fixture-container", "--format", "{{.Image}}"]]
    with pytest.raises(ValueError, match="IDENTITY"):
        cli.inspect_image("mutable:tag", FIXTURE.source_sha)
    with pytest.raises(RuntimeError, match="MISMATCH"):
        cli.verify_container("fixture-container", "sha256:" + "0" * 64)
