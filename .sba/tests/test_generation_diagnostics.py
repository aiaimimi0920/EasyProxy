"""生成诊断只允许固定阶段，不输出私有异常或改变单次执行语义。"""
import base64
import json
from pathlib import Path
import sys

import pytest

sys.path.insert(0, str(Path(__file__).parents[1]))
import aggregator_build as build
from diagnostics import StageError, safe_error_code
from test_aggregator_build import template


PRIVATE = "DO_NOT_PRINT_PRIVATE_SUBSCRIPTION_OR_SECRET"
CASES = {
    "source": "GENERATION_SOURCE",
    "runtime": "GENERATION_RUNTIME",
    "config": "GENERATION_CONFIG",
    "collection": "GENERATION_COLLECT",
    "readback": "GENERATION_READBACK",
    "outputs": "GENERATION_OUTPUTS",
    "conversion": "GENERATION_CONVERSION",
    "validation": "GENERATION_VALIDATION",
}


def fixture(tmp_path, monkeypatch, failure=None):
    root = tmp_path / "root"
    root.mkdir()
    scripts = root / "scripts"
    scripts.mkdir()
    payload = base64.b64encode(b"ss://fixture@fixture.invalid:443\n")
    converter = "def build_payload(body):\n    return body, {}\n"
    if failure == "conversion":
        converter = "def build_payload(body):\n    raise RuntimeError(" + repr(PRIVATE) + ")\n"
    (scripts / "publish-aggregator-v2ray.py").write_text(converter, encoding="utf-8")
    calls = []

    def fail():
        raise RuntimeError(PRIVATE)

    def inspect(*_args):
        if failure == "source":
            fail()
        return [{"path": "upstreams/aggregator", "sourceSha": "b" * 40}]

    def copy(_source, target):
        if failure == "runtime":
            fail()
        target.mkdir()

    def run(args, _cwd, _env, label, _timeout):
        calls.append(label)
        if (failure, label) in (("config", "config"), ("collection", "generation")):
            fail()
        if label == "config":
            Path(args[-1]).write_text(json.dumps(template()), encoding="utf-8")
        else:
            bodies = {
                "public-clash": b"proxies: [{name: fixture}]\n",
                "public-singbox": b'{"outbounds":[{"type":"direct"}]}',
                "public-mixed": payload,
                "crawledsubs": b'{"fixture":[]}',
            }
            for name, body in bodies.items():
                if failure != "outputs" or name != "public-clash":
                    (output / "artifacts" / (name + ".artifact")).write_bytes(body)

    monkeypatch.setattr(build, "inspect_checkout", inspect)
    monkeypatch.setattr(build, "inspect_gitlink_checkout", lambda *_: None)
    monkeypatch.setattr(build, "_copy", copy)
    monkeypatch.setattr(build, "prepare_runtime", lambda *_: {})
    monkeypatch.setattr(build, "verify_runtime", lambda *_: fail() if failure == "readback" else None)
    monkeypatch.setattr(build, "_run", run)
    if failure == "validation":
        monkeypatch.setattr(build, "_validate", lambda *_: fail())
    output = tmp_path / "output"
    return root, output, calls


@pytest.mark.parametrize("failure,stage", CASES.items())
def test_generation_failure_has_precise_fixed_stage_without_private_body(tmp_path, monkeypatch, capsys, failure, stage):
    root, output, calls = fixture(tmp_path, monkeypatch, failure)
    with pytest.raises(StageError) as caught:
        build.build_aggregator(root, "owner/repo", "a" * 40, output,
                               seed_secrets=dict.fromkeys(build.SEED_SECRETS, build.DISABLED_SEED))
    assert safe_error_code(caught.value) == "EASYPROXY_SBA_" + stage + "_UNKNOWN"
    captured = capsys.readouterr()
    assert PRIVATE not in str(caught.value) + captured.out + captured.err
    assert calls.count("config") <= 1 and calls.count("generation") <= 1


def test_success_keeps_original_bodies_and_single_generation_call(tmp_path, monkeypatch):
    root, output, calls = fixture(tmp_path, monkeypatch)
    bodies = build.build_aggregator(root, "owner/repo", "a" * 40, output,
                                   seed_secrets=dict.fromkeys(build.SEED_SECRETS, build.DISABLED_SEED))
    assert set(bodies) == build.RAW
    assert bodies["subs/v2ray.txt"] == bodies["subs/mixed.txt"]
    assert calls == ["config", "generation"]
