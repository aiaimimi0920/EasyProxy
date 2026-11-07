"""只读带入已提交稳定版的来源缓存；不复用旧节点健康结论或向上游传写凭据。"""
import json
from urllib.parse import urlsplit

from aggregator_publish import PublicArtifacts, _previous
from artifact_store import ArtifactStore, MAX_BYTES, require
from coordination import Coordinator, r2_store


def validate_sources(raw: bytes) -> dict:
    require(type(raw) is bytes and 0 < len(raw) <= MAX_BYTES, "SOURCE_CACHE")
    try:
        rows = json.loads(raw)
        require(type(rows) is dict and 0 < len(rows) <= 100000, "SOURCE_CACHE")
        for url, metadata in rows.items():
            parsed = urlsplit(url)
            require(parsed.scheme == "https" and parsed.hostname and not parsed.username and not parsed.password
                    and not parsed.fragment and type(metadata) is dict, "SOURCE_CACHE")
        return rows
    except (ValueError, TypeError, UnicodeError, RecursionError):
        require(False, "SOURCE_CACHE")


def load_previous_sources(config, expected_release: dict, credentials: dict) -> bytes:
    store = r2_store(config.account_id, config.bucket, credentials["EASYPROXY_R2_ACCESS_KEY_ID"],
                     credentials["EASYPROXY_R2_SECRET_ACCESS_KEY"])
    public = None
    try:
        owner = Coordinator(store, config.identity("production"), config.inventory_digest)
        before = owner._read()
        require(before is not None and before[0]["active"] == expected_release, "SOURCE_CACHE_IDENTITY")
        public = PublicArtifacts(config.worker_origin + "/aggregator",
            **({"inventory": config.inventory} if config.custom_worker_origin is not None else {}))
        _, _, artifacts = _previous(ArtifactStore(store), public, expected_release)
        raw = artifacts["internal/crawledsubs.json"].body
        validate_sources(raw)
        require(owner._read() == before, "SOURCE_CACHE_DRIFT")
        return raw
    finally:
        if public is not None:
            public.close()
        store.client.close()
