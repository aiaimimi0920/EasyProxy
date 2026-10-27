"""本地输出配置必须覆盖全部 R2 入口，不向上游传递部署秘密。"""
import copy
import json
import importlib.util
from pathlib import Path
import subprocess
import sys

import pytest

sys.path.insert(0, str(Path(__file__).parents[1]))
import aggregator_build as build
from artifact_store import ArtifactError
from sources import read_gitlinks, inspect_gitlink_checkout, _materialize_gitlink


ROOT = Path(__file__).resolve().parents[2]


def template():
    raw = (ROOT / "deploy/upstreams/aggregator/config/config.actions.r2.json").read_text(encoding="utf-8")
    return json.loads(raw.replace("__ISSUE91_SUB_URL_PLACEHOLDER__", "https://fixture.invalid/shared-sub"))


def test_local_config_cannot_leave_remote_storage_credentials_or_paths(tmp_path):
    source = template()
    source["storage"].update(access_key_id="fixture-private", token="fixture-private", base="https://private.invalid")
    source["storage"]["items"]["public-clash"]["local"] = "../outside"
    original = copy.deepcopy(source)
    rendered = build.local_config(source, tmp_path.resolve())
    assert source == original
    assert rendered["storage"]["engine"] == "local"
    assert set(rendered["storage"]) == {"engine", "items"}
    assert set(rendered["storage"]["items"]) == set(build.FILES)
    assert all(row == {"folder_id": str(tmp_path.resolve()), "file_id": name + ".artifact"}
               for name, row in rendered["storage"]["items"].items())
    assert rendered["update"] == {"enable": False}
    assert "fixture-private" not in json.dumps(rendered)


@pytest.mark.parametrize("mutation", [lambda c: c["crawl"].update(scripts=["arbitrary.py"]),
                                    lambda c: c["domains"][0].update(domain="https://register.invalid"),
                                    lambda c: c["storage"]["items"]["public-clash"].update(key="../private"),
                                    lambda c: c["storage"]["items"].update(unknown={"key":"unknown"}),
                                    lambda c: c["groups"]["public"]["regularize"].update(enable=True),
                                    lambda c: c["groups"]["public"].pop("regularize"),
                                    lambda c: c.update(groups={})])
def test_unreviewed_plugin_registration_or_storage_is_rejected(mutation, tmp_path):
    source = template()
    mutation(source)
    with pytest.raises(ArtifactError, match="CONFIG"):
        build.local_config(source, tmp_path.resolve())


def test_environment_has_no_ambient_cloud_credentials_proxy_python_injection_or_dotenv(tmp_path, monkeypatch):
    for key in ("CLOUDFLARE_API_TOKEN", "AWS_ACCESS_KEY_ID", "R2_SECRET_ACCESS_KEY", "GH_TOKEN", "GITHUB_TOKEN",
                "MISUB_ADMIN_PASSWORD", "ECH_TOKEN", "HTTP_PROXY", "HTTPS_PROXY", "PYTHONPATH", "PYTHONSTARTUP"):
        monkeypatch.setenv(key, "fixture-private")
    env = build._environment(tmp_path)
    assert "fixture-private" not in json.dumps(env)
    assert env["PYTHONDONTWRITEBYTECODE"] == "1" and env["PYTHONNOUSERSITE"] == "1"
    assert env["HOME"] == str(tmp_path / "home")
    assert env["TMPDIR"] == str(tmp_path)


def test_explicitly_disabled_optional_seed_has_no_relay_dependency_and_keeps_public_crawl(tmp_path, monkeypatch):
    values = dict.fromkeys(build.SEED_SECRETS, build.DISABLED_SEED)
    assert build.configured_seed_secrets(values) == {}
    assert values == dict.fromkeys(build.SEED_SECRETS, build.DISABLED_SEED)
    spec = importlib.util.spec_from_file_location("disabled_seed_materializer", ROOT / "scripts/materialize-aggregator-config.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    for name in build.SEED_SECRETS:
        monkeypatch.delenv(name, raising=False)
    output = tmp_path / "materialized.json"
    module.materialize(ROOT / "deploy/upstreams/aggregator/config/config.actions.r2.json", output)
    rendered = build.local_config(json.loads(output.read_bytes()), tmp_path)
    private = next(row for row in rendered["domains"] if row["name"].startswith("seed-sub-issue91-shared"))
    assert private["enable"] is False and private["sub"] == []
    assert any(row["enable"] and row["sub"] for row in rendered["domains"])
    assert rendered["crawl"]["enable"] is True
    assert build.DISABLED_SEED not in json.dumps(rendered) and "seed-relay" not in json.dumps(rendered)


def test_configured_private_seed_is_preserved_and_unknown_secret_names_are_rejected():
    value = {"EASYPROXY_AGGREGATOR_ISSUE91_SUB_URL_B64": "fixture-encoded-value"}
    assert build.configured_seed_secrets(value) == value
    with pytest.raises(ArtifactError, match="SECRETS"):
        build.configured_seed_secrets({"CLOUDFLARE_API_TOKEN": "fixture"})


def test_candidate_generation_defers_liveness_to_required_real_easyproxy_audit(tmp_path, monkeypatch):
    from test_generation_diagnostics import fixture
    root, output, calls = fixture(tmp_path, monkeypatch)
    original_run, observed = build._run, []

    def run(args, cwd, env, label, timeout):
        if label == "generation":
            observed.append(list(args))
            assert not any(name in env for name in ("CLOUDFLARE_API_TOKEN", "GITHUB_TOKEN", "SKIP_ALIVE_CHECK"))
        return original_run(args, cwd, env, label, timeout)

    monkeypatch.setattr(build, "_run", run)
    bodies = build.build_aggregator(root, "owner/repo", "a" * 40, output,
        seed_secrets=dict.fromkeys(build.SEED_SECRETS, build.DISABLED_SEED))
    assert set(bodies) == build.RAW and calls == ["config", "generation"]
    args, = observed
    assert args.count("--skip-alive-check") == 1
    assert all(args.count(flag) == 1 for flag in ("--overwrite", "--skip-remark", "--invisible", "--server", "--environment"))
    assert "--no-skip-alive-check" not in args


def test_approved_legacy_liveness_can_remove_all_uri_exportable_candidates_without_network(tmp_path):
    # HTTP 节点可保留但旧 mixed 转换器不导出；候选集合不能先被旧内核的瞬时连通性裁剪。
    sha = subprocess.run(["git", "-C", str(ROOT), "rev-parse", "HEAD"], capture_output=True, check=True).stdout.decode().strip()
    row = next(row for row in read_gitlinks(ROOT, sha) if row["path"] == "upstreams/aggregator")
    approved = tmp_path / "approved"
    checkout = approved / row["path"]
    checkout.mkdir(parents=True)
    _materialize_gitlink(row, checkout, ROOT.resolve())
    inspect_gitlink_checkout(approved.resolve(), row)
    script = '''import copy, pathlib, socket, sys
def forbidden(*a, **k):
    raise RuntimeError("NETWORK_FORBIDDEN")
socket.socket.connect = forbidden
socket.create_connection = forbidden
sys.path.insert(0, sys.argv[1])
import pipeline
pipeline.time.sleep = lambda *_: None
pipeline.utils.chmod = lambda *_: None
class Process:
    def __init__(self, *_a, **_k): pass
    def terminate(self): pass
pipeline.subprocess.Popen = Process
pipeline.utils.multi_thread_run = lambda **kwargs: [False for _ in kwargs["tasks"]]
nodes = [
    {"name":"synthetic-http", "type":"http", "server":"127.0.0.1", "port":9, "liveness":False},
    {"name":"synthetic-ss", "type":"ss", "server":"127.0.0.1", "port":10,
     "cipher":"aes-128-gcm", "password":"synthetic-test-only", "liveness":True},
]
root = pathlib.Path(sys.argv[2])
legacy = pipeline.check_alive_proxies(copy.deepcopy(nodes), "unused", str(root / "legacy"), display=False)
assert [node["type"] for node in legacy] == ["http"]
candidates = pipeline.check_alive_proxies(copy.deepcopy(nodes), "unused", str(root / "candidates"), skip=True, display=False)
assert sorted(node["type"] for node in candidates) == ["http", "ss"]
print("APPROVED_CANDIDATES_PRESERVED_WITHOUT_NETWORK")
'''
    env_root = tmp_path / "environment"
    env_root.mkdir()
    result = subprocess.run([sys.executable, "-c", script, str(checkout / "subscribe"), str(tmp_path)],
        cwd=tmp_path, env=build._environment(env_root), capture_output=True, timeout=30)
    assert result.returncode == 0, result.stderr.decode("utf-8", errors="replace")
    assert b"APPROVED_CANDIDATES_PRESERVED_WITHOUT_NETWORK" in result.stdout


def test_actual_approved_upstream_local_backend_writes_and_reads_without_network(tmp_path):
    # Worktree root may contain unrelated edits; only the accurate gitlink is the source of this backend test.
    sha = subprocess.run(["git", "-C", str(ROOT), "rev-parse", "HEAD"], capture_output=True, check=True).stdout.decode().strip()
    row = next(row for row in read_gitlinks(ROOT, sha) if row["path"] == "upstreams/aggregator")
    approved = tmp_path / "approved"
    checkout = approved / row["path"]
    checkout.mkdir(parents=True)
    _materialize_gitlink(row, checkout, ROOT.resolve())
    inspect_gitlink_checkout(approved.resolve(), row)
    source = tmp_path / "upstream"
    build._copy(checkout, source)
    output = tmp_path / "outputs"
    output.mkdir()
    env_root = tmp_path / "environment"
    env_root.mkdir()
    env = build._environment(env_root)
    config = build.local_config(template(), output.resolve())
    (tmp_path / "config.json").write_text(json.dumps(config), encoding="utf-8")
    script = '''import json, socket, sys
def forbidden(*a, **k):
    raise RuntimeError("NETWORK_FORBIDDEN")
socket.socket.connect = forbidden
socket.create_connection = forbidden
sys.path.insert(0, sys.argv[1])
from config.models import ProcessConfig
import push
config = ProcessConfig.parse(json.load(open(sys.argv[2], encoding="utf-8")))
backend = push.get_instance(config.storage)
assert type(backend) is push.PushToLocal
for name, item in config.storage.items.items():
    assert backend.push_to("local-fixture-" + name, item, retry=99)
    from urllib.request import url2pathname
    from urllib.parse import urlsplit
    assert open(url2pathname(urlsplit(backend.raw_url(item)).path), encoding="utf-8").read() == "local-fixture-" + name
print("APPROVED_LOCAL_BACKEND_OK")
'''
    result = subprocess.run([sys.executable, "-c", script, str(source / "subscribe"), str(tmp_path / "config.json")],
                            cwd=source, env=env, capture_output=True, timeout=30)
    assert result.returncode == 0, result.stderr.decode("utf-8", errors="replace")
    assert b"APPROVED_LOCAL_BACKEND_OK" in result.stdout
    assert len(list(output.iterdir())) == len(build.FILES)
