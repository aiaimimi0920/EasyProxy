"""配额来源生成、旧池排除和精确同步验收；全部使用离线夹具。"""
import hashlib
from pathlib import Path
import sys
from urllib.parse import quote

import pytest
import yaml

sys.path.insert(0, str(Path(__file__).parents[1]))
from subscription_sources import quota_exhausted, configure_registered, record_registered, seed_without_registered
from misub_sync import quota_filtered_expected, MiSubError
import subscription_sources
from test_subscription_sources import Response, Session, NODE


@pytest.mark.parametrize("header,expected", [
    ("upload=0;download=99;total=100", False), ("upload=1;download=99;total=100", True),
    ("upload=0;download=1;total=100", False),
    ("upload=0;download=99000000000000000001;total=100000000000000000000", True),
    (None, None), ("", None), ("upload=0;download=0", None),
    ("upload=0;download=0;total=0", None), ("upload=-1;download=0;total=100", None),
    ("upload=0;upload=1;download=0;total=100", None),
])
def test_strict_quota(header, expected):
    assert quota_exhausted(header) is expected


def test_generation_excludes_exhausted_and_unknown_but_rechecks_reset(tmp_path, monkeypatch):
    source = {"id": "quota-source", "url": "https://fixture.example/private", "user_agent": "clash.meta", "quota_enabled": True}
    responses = [Response(b"fixture") for _ in range(3)]
    responses[0].headers["subscription-userinfo"] = "upload=0;download=100;total=100"
    responses[2].headers["subscription-userinfo"] = "upload=0;download=1;total=100"
    session = Session(responses)
    monkeypatch.setattr(subscription_sources, "secure_session", lambda: session)
    for i in range(3):
        output = tmp_path / str(i)
        output.mkdir()
        config = {"domains": [], "groups": {}, "storage": {"items": {}}}
        records = configure_registered(config, [source], output)
        if i < 2:
            assert records == [] and config["domains"] == []
            assert list(output.iterdir()) == []
        else:
            assert len(records) == 1 and records[0]["quota_key"] == hashlib.sha256(source["id"].encode()).hexdigest()
    assert len(session.calls) == 3


def test_marker_is_added_after_conversion_and_seed_still_removes_node(tmp_path):
    private = tmp_path / "private.yaml"
    private.write_text(yaml.safe_dump({"proxies": [NODE]}), encoding="utf-8")
    public = dict(NODE, name="中文 + changed by converter")
    raw = record_registered(yaml.safe_dump({"proxies": [public]}).encode(), [
        {"id": "quota-source", "artifact": private, "downloadSha256": "b" * 64, "quota_key": "a" * 64}], tmp_path)
    assert yaml.safe_load(raw)["proxies"][0]["name"] == "[epq:" + "a" * 64 + "] 中文 + changed by converter"
    assert yaml.safe_load(seed_without_registered(raw))["proxies"] == []


def test_exact_manifest_verification_allows_only_marked_quota_exclusions():
    key = hashlib.sha256(b"quota-source").hexdigest()
    managed = {"id": "proxy_runtime_node_1", "kind": "proxy_uri", "options": {"managed_by": "easyproxy_runtime_sources"},
               "input": "ss://fixture#" + quote("[epq:" + key + "] name")}
    manual = {**managed, "id": "manual"}
    expected = {row["id"]: row for row in [managed, manual]}
    source = {"id": "quota-source", "kind": "subscription", "enabled": True, "options": {"trafficQuota": {"enabled": True}}}
    baseline = {"subscriptions": [source]}
    for status, allowed in [("available", True), ("exhausted", False), ("unknown", False), ("manual-disabled", False)]:
        result = quota_filtered_expected(expected, baseline, {"traffic_quota": [
            {"id": source["id"], "status": status, "effectiveEnabled": allowed}]})
        assert (managed["id"] in result) is allowed
        assert result["manual"] == manual
    with pytest.raises(MiSubError, match="QUOTA_UNKNOWN"):
        quota_filtered_expected(expected, baseline, {"traffic_quota": []})
    assert quota_filtered_expected(expected, {"subscriptions": [{**source, "options": {}}]}, {}) == expected
    assert quota_filtered_expected(expected, {"subscriptions": []}, {}) == {"manual": manual}
