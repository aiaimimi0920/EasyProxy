"""Pages Direct Upload 协议；账户 token 与项目资产 JWT 分开，不调用发布 CLI。"""
from __future__ import annotations

import base64
import json
import re

import requests

from cloudflare_http import api_result, decode_object, secure_session
from worker_api import credential


class PagesError(RuntimeError):
    """不附加秘密、响应正文、构建日志或 provider 原异常。"""


def require(condition: bool, code: str):
    if not condition:
        raise PagesError("EASYPROXY_SBA_PAGES_" + code)


def name(value: str) -> str:
    require(isinstance(value, str) and re.fullmatch(r"[a-z][a-z0-9-]{1,61}[a-z0-9]", value) is not None, "IDENTITY")
    return value


def identifier(value: str) -> str:
    require(isinstance(value, str) and re.fullmatch(r"[a-f0-9]{8}(?:-[a-f0-9]{4}){3}-[a-f0-9]{12}", value) is not None,
            "IDENTITY")
    return value


class PagesAPI:
    def __init__(self, account: str, project: str, token: str):
        require(isinstance(account, str) and re.fullmatch(r"[a-f0-9]{32}", account) is not None
                and credential(token), "CREDENTIALS")
        self.path = f"/accounts/{account}/pages/projects/{name(project)}"
        self.project_name = project
        self._token = token
        self._session = secure_session()

    def close(self):
        self._session.close()

    def project(self) -> dict | None:
        value = api_result(self._session, self._token, "GET", self.path, require, missing_code=8000007)
        require(value is None or type(value) is dict, "RESPONSE_UNKNOWN")
        return value

    def create(self, config: dict) -> dict:
        value = api_result(self._session, self._token, "POST", self.path.rsplit("/", 1)[0], require,
                           json={"name": self.project_name, "production_branch": "main", "deployment_configs": config})
        require(type(value) is dict, "RESPONSE_UNKNOWN")
        return value

    def configure(self, config: dict):
        value = api_result(self._session, self._token, "PATCH", self.path, require, json={"deployment_configs": config})
        require(type(value) is dict, "RESPONSE_UNKNOWN")

    def upload_assets(self, assets: tuple, before_write):
        value = api_result(self._session, self._token, "GET", self.path + "/upload-token", require)
        require(type(value) is dict and isinstance(value.get("jwt"), str) and 0 < len(value["jwt"]) <= 16384
                and all(33 <= ord(char) <= 126 for char in value["jwt"]), "UPLOAD_TOKEN")
        jwt = value["jwt"]
        hash_set = {asset.hash for asset in assets}
        hashes = sorted(hash_set)
        before_write()
        missing = api_result(self._session, jwt, "POST", "/pages/assets/check-missing", require, json={"hashes": hashes})
        require(type(missing) is list and all(type(item) is str and item in hash_set for item in missing)
                and len(set(missing)) == len(missing), "ASSETS_UNKNOWN")
        by_hash = {asset.hash: asset for asset in assets}
        # One bounded asset per request; no hidden retry, refresh-token replay or
        # parallel partial-success path. Cloudflare permits 50 MiB / 5000 per batch.
        for key in missing:
            asset = by_hash[key]
            before_write()
            api_result(self._session, jwt, "POST", "/pages/assets/upload", require,
                       json=[{"key": key, "value": base64.b64encode(asset.body).decode("ascii"),
                              "metadata": {"contentType": asset.content_type}, "base64": True}])
        # A second read-only check confirms every required hash is present.
        before_write()
        verified = api_result(self._session, jwt, "POST", "/pages/assets/check-missing", require, json={"hashes": hashes})
        require(verified == [], "ASSETS_UNKNOWN")
        before_write()
        api_result(self._session, jwt, "POST", "/pages/assets/upsert-hashes", require, json={"hashes": hashes})

    def deploy(self, artifact, release: dict) -> dict:
        value = api_result(self._session, self._token, "POST", self.path + "/deployments", require, files={
            "manifest": (None, json.dumps({asset.path: asset.hash for asset in artifact.assets}, separators=(",", ":"))),
            "branch": (None, "main"), "commit_hash": (None, release["sourceSha"]),
            "commit_message": (None, "EasyProxy SBA " + release["applicationVersion"]), "commit_dirty": (None, "false"),
            "_worker.bundle": ("_worker.bundle", artifact.worker_bundle, "application/octet-stream"),
            "_routes.json": ("_routes.json", artifact.routes, "application/json")})
        require(type(value) is dict, "RESPONSE_UNKNOWN")
        return value

    def deployment(self, deployment_id: str) -> dict:
        value = api_result(self._session, self._token, "GET", self.path + "/deployments/" + identifier(deployment_id), require)
        require(type(value) is dict, "RESPONSE_UNKNOWN")
        return value

    def deployments(self) -> list:
        value = api_result(self._session, self._token, "GET", self.path + "/deployments", require)
        require(type(value) is list and all(type(row) is dict for row in value), "RESPONSE_UNKNOWN")
        return value


def public_release(project: str) -> dict:
    session = secure_session()
    try:
        with session.get(f"https://{name(project)}.pages.dev/.well-known/easyproxy-release.json",
                         headers={"Cache-Control": "no-cache"}, timeout=(10, 30), allow_redirects=False, stream=True) as response:
            require(response.status_code == 200, "PUBLIC_VERSION")
            raw = bytearray()
            for chunk in response.iter_content(4096):
                raw.extend(chunk)
                require(len(raw) <= 16384, "PUBLIC_VERSION")
            return decode_object(bytes(raw), require)
    except (requests.RequestException, OSError):
        raise PagesError("EASYPROXY_SBA_PAGES_PUBLIC_UNKNOWN") from None
    finally:
        session.close()
