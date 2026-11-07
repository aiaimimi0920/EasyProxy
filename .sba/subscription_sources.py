"""MiSub 动态订阅进入云端候选池；原始地址和下载内容仅留在私有生成目录。"""
from __future__ import annotations

import hashlib
import ipaddress
import json
from pathlib import Path
import re
from urllib.parse import urljoin, urlsplit

import requests
import yaml

from aggregator_publish import ClashSafeLoader
from artifact_store import ArtifactError, MAX_BYTES, require
from cloudflare_http import secure_session


REGISTERED_HASHES = "x-easyproxy-registered-node-hashes"


def subscription_url(value: str) -> str:
    require(isinstance(value, str) and 0 < len(value) <= 8192 and not any(char.isspace() for char in value), "SUBSCRIPTION_URL")
    try:
        parsed = urlsplit(value)
        host = parsed.hostname
        require(parsed.scheme == "https" and host and not parsed.username and not parsed.password and not parsed.fragment
                and parsed.port in (None, 443) and host != "localhost" and not host.endswith((".local", ".localhost", ".internal")), "SUBSCRIPTION_URL")
        try:
            address = ipaddress.ip_address(host)
        except ValueError:
            require("." in host, "SUBSCRIPTION_URL")
        else:
            require(address.is_global, "SUBSCRIPTION_URL")
    except ValueError:
        raise ArtifactError("EASYPROXY_SBA_ARTIFACT_SUBSCRIPTION_URL") from None
    return value


def load_registered_subscriptions(config, credentials: dict) -> list[dict]:
    from misub_sync import MiSubAPI
    api = MiSubAPI(config.pages_name, credentials["EASYPROXY_MISUB_ADMIN_PASSWORD"], credentials["EASYPROXY_MANIFEST_TOKEN"])
    try:
        rows = api.snapshot()["subscriptions"]
    finally:
        api.close()
    result = []
    for row in rows:
        # A failed format probe must not prevent a later scheduled recovery.
        if row.get("kind") != "subscription" or row.get("enabled") is not True:
            continue
        agent = row.get("customUserAgent") or "clash.meta"
        require(isinstance(agent, str) and 0 < len(agent) <= 512 and all(32 <= ord(char) < 127 for char in agent), "SUBSCRIPTION_AGENT")
        result.append({"id": row["id"], "url": subscription_url(row.get("input") or row.get("url")), "user_agent": agent})
    return result


def _download(session, source: dict) -> bytes:
    url = subscription_url(source["url"])
    try:
        for redirect in range(4):
            with session.get(url, headers={"User-Agent": source["user_agent"], "Cache-Control": "no-cache"},
                             timeout=(10, 60), stream=True, allow_redirects=False) as response:
                if response.status_code in (301, 302, 303, 307, 308):
                    require(redirect < 3 and response.headers.get("Location"), "SUBSCRIPTION_FETCH")
                    url = subscription_url(urljoin(url, response.headers["Location"]))
                    continue
                require(response.status_code == 200, "SUBSCRIPTION_FETCH")
                body = bytearray()
                for chunk in response.iter_content(65536):
                    body.extend(chunk)
                    require(len(body) <= MAX_BYTES, "SUBSCRIPTION_SIZE")
                require(body.strip(), "SUBSCRIPTION_EMPTY")
                return bytes(body)
    except (requests.RequestException, OSError):
        raise ArtifactError("EASYPROXY_SBA_ARTIFACT_SUBSCRIPTION_FETCH") from None
    raise ArtifactError("EASYPROXY_SBA_ARTIFACT_SUBSCRIPTION_FETCH")


def configure_registered(config: dict, sources: list[dict], output: Path) -> list[dict]:
    """复用批准的解析/转换器；每个来源另留一个仅本地的输出，拒绝静默漏源。"""
    records = []
    with secure_session() as session:
        for index, source in enumerate(sources, 1):
            group = f"registered-{index}"
            downloaded = output / (group + ".private.input")
            body = _download(session, source)
            downloaded.write_bytes(body)
            artifact = output / (group + ".private.yaml")
            config["domains"].append({"name": group, "domain": "", "sub": [downloaded.as_uri()], "enable": True,
                "push_to": ["public", group], "ignorede": True, "liveness": True, "secure": False})
            config["groups"][group] = {"emoji": True, "list": True, "targets": {"clash": group}, "regularize": {"enable": False}}
            config["storage"]["items"][group] = {"folder_id": str(output), "file_id": artifact.name}
            records.append({"id": source["id"], "artifact": artifact, "downloadSha256": hashlib.sha256(body).hexdigest()})
    return records


def node_hash(node: dict) -> str:
    return hashlib.sha256(json.dumps({key: value for key, value in node.items() if key != "name"},
                                    sort_keys=True, ensure_ascii=False, separators=(",", ":")).encode("utf-8")).hexdigest()


def _clash(raw: bytes) -> dict:
    value = yaml.load(raw, Loader=ClashSafeLoader)
    require(type(value) is dict and type(value.get("proxies")) is list
            and all(type(node) is dict for node in value["proxies"]), "SUBSCRIPTION_OUTPUT")
    return value


def seed_without_registered(raw: bytes) -> bytes:
    """上一轮用户订阅节点不作 remains 缓存；换日、禁用和删除后必须以本轮响应为准。"""
    value = _clash(raw)
    hashes = value.pop(REGISTERED_HASHES, [])
    require(type(hashes) is list and len(hashes) <= 100000
            and all(isinstance(item, str) and re.fullmatch(r"[a-f0-9]{64}", item) for item in hashes), "SUBSCRIPTION_OUTPUT")
    if not hashes:
        return raw
    excluded = set(hashes)
    value["proxies"] = [node for node in value["proxies"] if node_hash(node) not in excluded]
    return yaml.safe_dump(value, allow_unicode=True, sort_keys=False).encode("utf-8")


def record_registered(raw: bytes, records: list[dict], output: Path) -> bytes:
    if not records:
        return raw
    value = _clash(raw)
    public = {node_hash(node) for node in value["proxies"]}
    registered, report = set(), []
    for row in records:
        path = row["artifact"]
        require(path.is_file() and not path.is_symlink() and path.stat().st_size <= MAX_BYTES, "SUBSCRIPTION_OUTPUT")
        nodes = _clash(path.read_bytes())["proxies"]
        included = {node_hash(node) for node in nodes} & public
        require(nodes and included, "SUBSCRIPTION_OUTPUT")
        registered.update(included)
        report.append({"id": row["id"], "downloadSha256": row["downloadSha256"], "parsedCandidates": len(nodes), "includedCandidates": len(included)})
    value[REGISTERED_HASHES] = sorted(registered)
    (output / "registered-source-report.safe.json").write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
    return yaml.safe_dump(value, allow_unicode=True, sort_keys=False).encode("utf-8")
