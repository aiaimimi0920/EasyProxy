"""独立、无部署凭据的公开业务读回；不创建 writer、登录会话或云资源。"""
from __future__ import annotations

import hashlib
import re

import requests

from aggregator_publish import ARTIFACTS, _validate
from artifact_store import MAX_BYTES
from cloudflare_http import decode_object, secure_session
from coordination import SHA, VERSION
from sources import inspect_checkout
from suite_config import SuiteConfig


class VerificationError(RuntimeError):
    pass


def require(value, code):
    if not value:
        raise VerificationError("EASYPROXY_SBA_VERIFY_" + code)


class PublicReader:
    def __init__(self, config: SuiteConfig):
        require(type(config) is SuiteConfig, "IDENTITY")
        self.config = config
        self.session = secure_session()

    def close(self):
        self.session.close()

    def get(self, origin: str, path: str, limit: int) -> bytes:
        require(origin in (self.config.pages_origin, self.config.worker_origin)
                and path.startswith("/") and not path.startswith("//")
                and not any(char in path for char in ("?", "#", "\\", "\r", "\n"))
                and all(part not in (".", "..") for part in path.split("/")), "IDENTITY")
        try:
            with self.session.get(origin + path, headers={"Cache-Control": "no-cache"},
                    allow_redirects=False, timeout=(10, 45), stream=True) as response:
                require(response.status_code == 200, "READ_UNKNOWN")
                # A public response must never cause authentication to be retained.
                require("Set-Cookie" not in response.headers, "AUTH_SIDE_EFFECT")
                body = bytearray()
                for chunk in response.iter_content(65536):
                    body.extend(chunk)
                    require(len(body) <= limit, "SIZE")
                require(body, "EMPTY")
                return bytes(body)
        except (requests.RequestException, OSError):
            raise VerificationError("EASYPROXY_SBA_VERIFY_READ_UNKNOWN") from None
        finally:
            self.session.cookies.clear()


def _manifest(raw: bytes, release: dict, aggregator_sha: str) -> dict:
    result = decode_object(raw, require)
    require(set(result) == {"schema_version", "run_id", "generated_at", "root_commit", "application_version",
                            "aggregator_commit", "previous_run_id", "counts", "artifacts"}, "MANIFEST")
    require(type(result["schema_version"]) is int and result["schema_version"] == 1
            and type(result["run_id"]) is str and re.fullmatch(r"[a-f0-9]{32}", result["run_id"]) is not None
            and result["root_commit"] == release["sourceSha"]
            and result["application_version"] == release["applicationVersion"]
            and result["aggregator_commit"] == aggregator_sha, "VERSION")
    rows = result["artifacts"]
    require(type(rows) is list and len(rows) == len(ARTIFACTS)
            and all(type(row) is dict and set(row) == {"name", "stable_key", "release_key", "sha256", "size"}
                    and type(row["name"]) is str for row in rows)
            and {row["name"] for row in rows} == set(ARTIFACTS), "MANIFEST")
    for row in rows:
        key = ARTIFACTS[row["name"]]
        require(row["stable_key"] == key and row["release_key"] == f"releases/{result['run_id']}/{key}"
                and type(row["size"]) is int and 0 < row["size"] <= MAX_BYTES
                and type(row["sha256"]) is str and re.fullmatch(r"[a-f0-9]{64}", row["sha256"]) is not None, "MANIFEST")
    counts = result["counts"]
    require(type(counts) is dict and set(counts) == {"nodes", "sources"}
            and all(type(count) is int and 0 < count <= 100000 for count in counts.values()), "COUNTS")
    return result


def verify_public(config: SuiteConfig, release: dict, root) -> dict:
    require(type(config) is SuiteConfig and type(release) is dict and set(release) == {"sourceSha", "applicationVersion"}
            and type(release["sourceSha"]) is str and SHA.fullmatch(release["sourceSha"]) is not None
            and type(release["applicationVersion"]) is str and VERSION.fullmatch(release["applicationVersion"]) is not None,
            "IDENTITY")
    links = inspect_checkout(root, config.repository, release["sourceSha"])
    aggregator_sha = next(row["sourceSha"] for row in links if row["path"] == "upstreams/aggregator")
    reader = PublicReader(config)
    try:
        expected = {"schemaVersion": 1, "applicationId": "easyproxy-cloud", **release}
        for origin in (config.worker_origin, config.pages_origin):
            metadata = decode_object(reader.get(origin, "/.well-known/easyproxy-release.json", 16384), require)
            require(type(metadata.get("schemaVersion")) is int and metadata == expected, "VERSION")
        raw = reader.get(config.worker_origin, "/aggregator/manifests/stable.json", 65536)
        manifest = _manifest(raw, release, aggregator_sha)
        require(reader.get(config.worker_origin, f"/aggregator/releases/{manifest['run_id']}/manifest.json", 65536) == raw,
                "IMMUTABLE_MANIFEST")
        bodies = {}
        for row in manifest["artifacts"]:
            body = reader.get(config.worker_origin, "/aggregator/" + row["stable_key"], row["size"])
            require(len(body) == row["size"] and hashlib.sha256(body).hexdigest() == row["sha256"], "ARTIFACT_DRIFT")
            require(reader.get(config.worker_origin, "/aggregator/" + row["release_key"], row["size"]) == body,
                    "IMMUTABLE_ARTIFACT")
            bodies[row["stable_key"]] = body
        _validate(bodies, set(ARTIFACTS.values()))
        effective = decode_object(bodies["subs/effective.json"], require)
        require(set(effective) == {"count", "stable_available_uris"} and type(effective["count"]) is int
                and effective["count"] == manifest["counts"]["nodes"]
                and type(effective["stable_available_uris"]) is list, "EFFECTIVE")
        uris = effective["stable_available_uris"]
        require(len(uris) == effective["count"] and all(type(uri) is str
                and re.fullmatch(r"[a-z][a-z0-9+.-]*://[^\s]+", uri) is not None for uri in uris)
                and len(set(uris)) == len(uris)
                and bodies["subs/effective.txt"] == ("\n".join(uris) + "\n").encode("utf-8"), "EFFECTIVE")
        sources = decode_object(bodies["internal/crawledsubs.json"], require) if bodies["internal/crawledsubs.json"].lstrip().startswith(b"{") else None
        if sources is None:
            # This approved generator also supports a JSON array of source rows.
            import json
            try:
                sources = json.loads(bodies["internal/crawledsubs.json"].decode("utf-8"))
            except (ValueError, UnicodeError, RecursionError):
                require(False, "COUNTS")
        require(type(sources) in (dict, list) and len(sources) == manifest["counts"]["sources"], "COUNTS")
        html = reader.get(config.pages_origin, "/login", 1024 * 1024).decode("utf-8", errors="strict")
        scripts = re.findall(r'<script\b[^>]*\bsrc=["\'](/assets/js/[A-Za-z0-9_.-]+\.js)["\']', html)
        require("<html" in html.lower() and len(scripts) == 1, "FRONTEND")
        javascript = reader.get(config.pages_origin, scripts[0], 8 * 1024 * 1024)
        require(b"<html" not in javascript[:100].lower() and len(javascript) > 100, "FRONTEND")
        # Read stable again: a concurrent refresh must not create a mixed-version success.
        require(reader.get(config.worker_origin, "/aggregator/manifests/stable.json", 65536) == raw, "CONCURRENT_REFRESH")
        inspect_checkout(root, config.repository, release["sourceSha"])
        return {"checks": {"release-versions-verified": True, "published-subscriptions-verified": True,
                           "pages-frontend-verified": True}, "runId": manifest["run_id"],
                "manifestSha256": hashlib.sha256(raw).hexdigest(), "counts": manifest["counts"]}
    except (UnicodeError, ValueError, RecursionError):
        raise VerificationError("EASYPROXY_SBA_VERIFY_FORMAT") from None
    finally:
        reader.close()
