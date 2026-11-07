"""动态订阅每轮重取、不可静默漏源、旧用户节点不沿用；不访问生产。"""
import copy
import json
from pathlib import Path
import sys
from types import SimpleNamespace

import pytest
import requests
import yaml

sys.path.insert(0, str(Path(__file__).parents[1]))
import aggregator_build as build
import subscription_sources as sources
from artifact_store import ArtifactError
from test_misub_sync import wire  # noqa: F401 - real authenticated export fixture


NODE = {"name": "fixture", "type": "ss", "server": "192.0.2.1", "port": 443,
        "cipher": "aes-128-gcm", "password": "fixture-only"}


def test_registry_reads_export_only_and_selects_all_enabled_subscriptions_even_failed_probes(wire):
    wire["data"]["subscriptions"].extend([
        {"id": "live", "kind": "subscription", "enabled": True, "input": "https://fixture.invalid/live", "customUserAgent": "clash.meta"},
        {"id": "retry", "kind": "subscription", "enabled": True, "url": "https://fixture.invalid/retry", "probe_status": "failed"},
        {"id": "manual", "kind": "proxy_uri", "enabled": True, "input": "ss://not-a-feed"},
    ])
    assert sources.load_registered_subscriptions(SimpleNamespace(pages_name="easyproxy-fixture-misub"), {
        "EASYPROXY_MISUB_ADMIN_PASSWORD": "fixture-admin", "EASYPROXY_MANIFEST_TOKEN": "fixture-manifest",
    }) == [{"id": identity, "url": "https://fixture.invalid/" + identity, "user_agent": "clash.meta"} for identity in ("live", "retry")]
    assert [path for _, path, _ in wire["requests"]] == ["/api/login", "/api/backup/export"]


@pytest.mark.parametrize("url", ["http://fixture.invalid/sub", "file:///private", "https://user:private@fixture.invalid/sub",
    "https://127.0.0.1/sub", "https://169.254.169.254/", "https://localhost/", "https://x.local/sub",
    "https://fixture.invalid/sub#fragment", "https://fixture.invalid:8080/sub", "https://fixture.invalid/a\nb"])
def test_invalid_registry_url_never_leaks_input(url):
    with pytest.raises(ArtifactError, match="SUBSCRIPTION_URL") as error:
        sources.subscription_url(url)
    assert url not in str(error.value)


class Response:
    def __init__(self, body=b"fixture", status=200, location=None):
        self.body, self.status_code = body, status
        self.headers = {"Location": location} if location is not None else {}
    def __enter__(self): return self
    def __exit__(self, *_): pass
    def iter_content(self, _size): yield self.body


class Session:
    def __init__(self, responses): self.responses, self.calls = list(responses), []
    def __enter__(self): return self
    def __exit__(self, *_): pass
    def get(self, url, **kwargs):
        self.calls.append((url, kwargs))
        response = self.responses.pop(0)
        if isinstance(response, Exception): raise response
        return response


def test_each_generation_downloads_rotated_body_without_inheriting_credentials_or_raw_urls(tmp_path, monkeypatch):
    session = Session([Response(b"day-one"), Response(b"day-two")])
    monkeypatch.setattr(sources, "secure_session", lambda: session)
    source = {"id": "user-feed", "url": "https://fixture.invalid/private-token", "user_agent": "fixture-agent"}
    paths = []
    for day in (1, 2):
        output = tmp_path / str(day); output.mkdir()
        config = {"domains": [], "groups": {}, "storage": {"items": {}}}
        records = sources.configure_registered(config, [source], output)
        paths.append(config["domains"][0]["sub"][0])
        assert source["url"] not in json.dumps(config)
        assert config["domains"][0]["push_to"] == ["public", "registered-1"]
        assert records[0]["id"] == source["id"]
        assert (output / "registered-1.private.input").read_bytes() == (b"day-one" if day == 1 else b"day-two")
    assert paths[0] != paths[1] and len(session.calls) == 2
    for _, kwargs in session.calls:
        assert kwargs["headers"] == {"User-Agent": "fixture-agent", "Cache-Control": "no-cache"}
        assert kwargs["allow_redirects"] is False and kwargs["timeout"] == (10, 60)


@pytest.mark.parametrize("response,code", [(Response(status=401), "FETCH"), (Response(b""), "EMPTY"),
    (Response(b"x" * (sources.MAX_BYTES + 1)), "SIZE"), (requests.Timeout("private-url"), "FETCH"),
    (Response(status=302, location="http://fixture.invalid/insecure"), "URL")])
def test_failed_fetch_is_not_empty_success_and_has_no_automatic_retry(response, code):
    session = Session([response])
    with pytest.raises(ArtifactError, match="SUBSCRIPTION_" + code) as error:
        sources._download(session, {"url": "https://fixture.invalid/private", "user_agent": "clash.meta"})
    assert "private" not in str(error.value) and len(session.calls) == 1


def test_redirect_is_bounded_and_revalidates_each_target():
    session = Session([Response(status=302, location="/next"), Response(b"current")])
    assert sources._download(session, {"url": "https://fixture.invalid/sub", "user_agent": "clash.meta"}) == b"current"
    assert session.calls[1][0] == "https://fixture.invalid/next"
    session = Session([Response(status=302, location="/again") for _ in range(4)])
    with pytest.raises(ArtifactError, match="FETCH"):
        sources._download(session, {"url": "https://fixture.invalid/sub", "user_agent": "clash.meta"})
    assert len(session.calls) == 4


def test_registered_nodes_are_proven_in_combined_candidate_and_not_retained_after_rotation_or_disable(tmp_path):
    user = dict(NODE, name="user-name")
    agg = dict(NODE, name="agg-name", server="192.0.2.2")
    private = tmp_path / "source.yaml"
    private.write_text(yaml.safe_dump({"proxies": [dict(user, name="renamed-by-converter")]}), encoding="utf-8")
    original = yaml.safe_dump({"proxies": [agg, user]}).encode()
    combined = sources.record_registered(original, [{"id": "user-feed", "artifact": private, "downloadSha256": "a" * 64}], tmp_path)
    value = yaml.safe_load(combined)
    assert value[sources.REGISTERED_HASHES] == [sources.node_hash(user)]
    seeded = yaml.safe_load(sources.seed_without_registered(combined))
    assert seeded == {"proxies": [agg]}
    assert sources.seed_without_registered(original) == original
    assert "https://" not in combined.decode()
    report = json.loads((tmp_path / "registered-source-report.safe.json").read_bytes())
    assert report == [{"id": "user-feed", "downloadSha256": "a" * 64, "parsedCandidates": 1, "includedCandidates": 1}]


@pytest.mark.parametrize("failure", ["missing", "empty", "not-in-pool"])
def test_selected_source_cannot_silently_disappear_in_converter_output(tmp_path, failure):
    private = tmp_path / "source.yaml"
    if failure != "missing":
        private.write_text(yaml.safe_dump({"proxies": [] if failure == "empty" else [dict(NODE, server="192.0.2.99")]}), encoding="utf-8")
    with pytest.raises(ArtifactError, match="SUBSCRIPTION_OUTPUT"):
        sources.record_registered(yaml.safe_dump({"proxies": [NODE]}).encode(),
            [{"id": "user-feed", "artifact": private, "downloadSha256": "a" * 64}], tmp_path)


def test_real_build_hooks_merge_and_track_current_source_before_required_audit(tmp_path, monkeypatch):
    from test_generation_diagnostics import fixture
    root, output, calls = fixture(tmp_path, monkeypatch)
    original_run = build._run
    monkeypatch.setattr(sources, "secure_session", lambda: Session([Response(b"current-provider-config")]))
    def run(args, cwd, env, label, timeout):
        original_run(args, cwd, env, label, timeout)
        if label == "generation":
            config = json.loads((output / "runtime.private.json").read_bytes())
            assert config["domains"][-1]["push_to"] == ["public", "registered-1"]
            body = yaml.safe_dump({"proxies": [NODE]}).encode()
            (output / "artifacts/public-clash.artifact").write_bytes(body)
            (output / "registered-1.private.yaml").write_bytes(body)
    monkeypatch.setattr(build, "_run", run)
    bodies = build.build_aggregator(root, "owner/repo", "a" * 40, output,
        seed_secrets=dict.fromkeys(build.SEED_SECRETS, build.DISABLED_SEED),
        registered_subscriptions=[{"id": "user-feed", "url": "https://fixture.invalid/sub", "user_agent": "clash.meta"}])
    assert sources.REGISTERED_HASHES in yaml.safe_load(bodies["subs/clash.yaml"])
    assert calls == ["config", "generation"] and set(bodies) == build.RAW
