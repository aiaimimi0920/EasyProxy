"""来源缓存只来自完整验证的旧稳定版；严格只读，不复用健康数据。"""
import json
from pathlib import Path
import sys
from types import SimpleNamespace

import pytest

sys.path.insert(0, str(Path(__file__).parents[1]))
import generation_sources as sources
from artifact_store import ArtifactError, Object
from test_aggregator_publish import deployed, OLD


@pytest.mark.parametrize("body", [b'[]', b'{}', b'not-json', b'{"file:///private":{}}',
    b'{"https://fixture.invalid/sub":[]}', b'{"https://user:secret@fixture.invalid/sub":{}}'])
def test_untrusted_cache_is_rejected_without_exposing_its_content(body):
    with pytest.raises(ArtifactError, match="SOURCE_CACHE") as caught:
        sources.validate_sources(body)
    assert "secret" not in str(caught.value)


@pytest.mark.parametrize("drift", [None, "version", "artifact", "control"])
def test_load_requires_owned_stable_integrity_and_unchanged_control_and_never_writes(monkeypatch, drift):
    import aggregator_publish
    monkeypatch.setattr(aggregator_publish, "inspect_checkout", lambda *_: [{"path": "upstreams/aggregator", "sourceSha": "e" * 40}])
    owner, objects, public, _, _ = deployed()
    closed = []
    owner.store.client = SimpleNamespace(close=lambda: closed.append("store"))
    public.close = lambda: closed.append("public")
    config = SimpleNamespace(account_id=owner.store.account_id, bucket=owner.store.bucket,
        inventory_digest=owner.inventory_digest, worker_origin=public.origin.removesuffix("/aggregator"),
        custom_worker_origin=None, identity=lambda _: owner.identity)
    monkeypatch.setattr(sources, "r2_store", lambda *_: owner.store)
    monkeypatch.setattr(sources, "ArtifactStore", lambda _: objects)
    monkeypatch.setattr(sources, "PublicArtifacts", lambda *_: public)
    if drift == "artifact":
        objects.objects["internal/crawledsubs.json"] = Object(b'{}', '"' + '0' * 32 + '"')
    elif drift == "control":
        verify = public.verify
        def change(key, body):
            verify(key, body)
            owner.store.etag = '"' + 'f' * 32 + '"'
        public.verify = change
    expected = {**OLD, "sourceSha": "0" * 40} if drift == "version" else OLD
    before = (list(owner.store.writes), list(objects.writes), dict(owner.store.stages))
    credentials = dict.fromkeys(("EASYPROXY_R2_ACCESS_KEY_ID", "EASYPROXY_R2_SECRET_ACCESS_KEY"), "fixture")
    if drift:
        with pytest.raises(ArtifactError):
            sources.load_previous_sources(config, expected, credentials)
    else:
        assert sources.load_previous_sources(config, expected, credentials) == objects.get("internal/crawledsubs.json").body
    assert before == (owner.store.writes, objects.writes, owner.store.stages)
    assert "store" in closed and (drift == "version" or "public" in closed)
