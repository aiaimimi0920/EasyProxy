"""Pages Direct Upload 协议；账户 token 与项目资产 JWT 分开，不调用发布 CLI。"""
from __future__ import annotations

import base64
import json
import re

import requests

from cloudflare_http import api_result, decode_object, secure_session
from diagnostics import pages_phase
from origins import approved_origin
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
        with pages_phase("PAGES_PROJECT_READ") as diagnostic:
            value = api_result(self._session, self._token, "GET", self.path, require, missing_code=8000007, diagnostic=diagnostic)
            require(value is None or type(value) is dict, "RESPONSE_UNKNOWN")
            return value

    def create(self, config: dict) -> dict:
        with pages_phase("PAGES_PROJECT_CREATE") as diagnostic:
            value = api_result(self._session, self._token, "POST", self.path.rsplit("/", 1)[0], require, diagnostic=diagnostic,
                               json={"name": self.project_name, "production_branch": "main", "deployment_configs": config})
            require(type(value) is dict, "RESPONSE_UNKNOWN")
            return value

    def configure(self, config: dict):
        with pages_phase("PAGES_PROJECT_CONFIGURE") as diagnostic:
            value = api_result(self._session, self._token, "PATCH", self.path, require, diagnostic=diagnostic,
                               json={"deployment_configs": config})
            require(type(value) is dict, "RESPONSE_UNKNOWN")

    def upload_assets(self, assets: tuple, before_write):
        with pages_phase("PAGES_ASSET_TOKEN") as diagnostic:
            value = api_result(self._session, self._token, "GET", self.path + "/upload-token", require, diagnostic=diagnostic)
            require(type(value) is dict and isinstance(value.get("jwt"), str) and 0 < len(value["jwt"]) <= 16384
                    and all(33 <= ord(char) <= 126 for char in value["jwt"]), "UPLOAD_TOKEN")
            jwt = value["jwt"]
        with pages_phase("PAGES_ASSET_QUERY") as diagnostic:
            hash_set = {asset.hash for asset in assets}
            hashes = sorted(hash_set)
            before_write()
            missing = api_result(self._session, jwt, "POST", "/pages/assets/check-missing", require,
                                 pages_asset_response=True, diagnostic=diagnostic, json={"hashes": hashes})
            require(type(missing) is list and all(type(item) is str and item in hash_set for item in missing)
                    and len(set(missing)) == len(missing), "ASSETS_UNKNOWN")
        by_hash = {asset.hash: asset for asset in assets}
        # One bounded asset per request; no hidden retry, refresh-token replay or
        # parallel partial-success path. Cloudflare permits 50 MiB / 5000 per batch.
        for key in missing:
            with pages_phase("PAGES_ASSET_UPLOAD") as diagnostic:
                asset = by_hash[key]
                before_write()
                api_result(self._session, jwt, "POST", "/pages/assets/upload", require,
                           pages_asset_response=True, diagnostic=diagnostic,
                           json=[{"key": key, "value": base64.b64encode(asset.body).decode("ascii"),
                                  "metadata": {"contentType": asset.content_type}, "base64": True}])
        # Match the pinned Wrangler Pages protocol: upload ACKs precede recent-hash
        # registration. check-missing is not an immediate post-upload read barrier.
        # Deployment/project/public-version verification remains in publish_pages.
        with pages_phase("PAGES_ASSET_REGISTER") as diagnostic:
            before_write()
            api_result(self._session, jwt, "POST", "/pages/assets/upsert-hashes", require,
                       pages_asset_response=True, diagnostic=diagnostic, json={"hashes": hashes})

    def deploy(self, artifact, release: dict) -> dict:
        with pages_phase("PAGES_DEPLOYMENT_SUBMIT") as diagnostic:
            value = api_result(self._session, self._token, "POST", self.path + "/deployments", require, diagnostic=diagnostic, files={
                "manifest": (None, json.dumps({asset.path: asset.hash for asset in artifact.assets}, separators=(",", ":"))),
                "branch": (None, "main"), "commit_hash": (None, release["sourceSha"]),
                "commit_message": (None, "EasyProxy SBA " + release["applicationVersion"]), "commit_dirty": (None, "false"),
                "_worker.bundle": ("_worker.bundle", artifact.worker_bundle, "application/octet-stream"),
                "_routes.json": ("_routes.json", artifact.routes, "application/json")})
            require(type(value) is dict, "RESPONSE_UNKNOWN")
            return value

    def deployment(self, deployment_id: str) -> dict:
        with pages_phase("PAGES_DEPLOYMENT_READ") as diagnostic:
            value = api_result(self._session, self._token, "GET", self.path + "/deployments/" + identifier(deployment_id), require,
                               diagnostic=diagnostic)
            require(type(value) is dict, "RESPONSE_UNKNOWN")
            return value

    def deployments(self) -> list:
        with pages_phase("PAGES_DEPLOYMENT_LIST") as diagnostic:
            value = api_result(self._session, self._token, "GET", self.path + "/deployments", require, diagnostic=diagnostic)
            require(type(value) is list and all(type(row) is dict for row in value), "RESPONSE_UNKNOWN")
            return value


def public_release(project: str, *, origin: str | None = None) -> dict:
    provider = f"https://{name(project)}.pages.dev"
    origin = provider if origin is None else origin
    require(approved_origin(origin, provider), "IDENTITY")
    session = secure_session()
    try:
        with session.get(origin + "/.well-known/easyproxy-release.json",
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
