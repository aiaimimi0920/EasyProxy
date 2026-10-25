"""固定 Cloudflare Workers API；写入单次发送，禁止重定向、凭据链和自动重放。"""
from __future__ import annotations

import json
import re

import requests

from cloudflare_http import api_result, decode_object, secure_session


class WorkerError(RuntimeError):
    """只返回安全错误码，不附加响应正文、密码或 provider 原异常。"""


def require(condition: bool, code: str) -> None:
    if not condition:
        raise WorkerError("EASYPROXY_SBA_WORKER_" + code)


def credential(value: str) -> bool:
    return (isinstance(value, str) and 0 < len(value) <= 512 and value != "verification-not-applicable"
            and all(33 <= ord(character) <= 126 for character in value))


def decode(raw: bytes) -> dict:
    return decode_object(raw, require)


class WorkerAPI:
    def __init__(self, account_id: str, token: str):
        require(isinstance(account_id, str) and re.fullmatch(r"[a-f0-9]{32}", account_id) is not None
                and credential(token), "CREDENTIALS")
        self.account_id = account_id
        self._token = token
        self._session = secure_session()

    def close(self):
        self._session.close()

    def _request(self, method: str, suffix: str, **kwargs):
        # All suffixes are made by the methods below, never by a URL/config input.
        return api_result(self._session, self._token, method,
                          f"/accounts/{self.account_id}/workers/{suffix}", require, **kwargs)

    @staticmethod
    def _name(name: str) -> str:
        require(isinstance(name, str) and re.fullmatch(r"[a-z][a-z0-9-]{1,61}[a-z0-9]", name) is not None, "IDENTITY")
        return name

    def list_workers(self) -> list:
        value = self._request("GET", "scripts")
        require(type(value) is list and all(type(item) is dict and isinstance(item.get("id"), str) for item in value),
                "RESPONSE_UNKNOWN")
        require(len({item["id"] for item in value}) == len(value), "RESPONSE_UNKNOWN")
        return value

    def account_subdomain(self) -> str:
        value = self._request("GET", "subdomain")
        require(type(value) is dict and isinstance(value.get("subdomain"), str)
                and re.fullmatch(r"[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?", value["subdomain"]) is not None,
                "SUBDOMAIN")
        return value["subdomain"]

    def settings(self, name: str) -> dict:
        value = self._request("GET", f"scripts/{self._name(name)}/settings")
        require(type(value) is dict, "RESPONSE_UNKNOWN")
        return value

    def upload(self, name: str, metadata: dict, bundle: bytes) -> dict:
        require(type(metadata) is dict and type(bundle) is bytes and 0 < len(bundle) <= 3 * 1024 * 1024, "BUNDLE")
        value = self._request("PUT", f"scripts/{self._name(name)}", files={
            "metadata": (None, json.dumps(metadata, separators=(",", ":")), "application/json"),
            "edge.mjs": ("edge.mjs", bundle, "application/javascript+module")})
        require(type(value) is dict and value.get("id") == name and isinstance(value.get("etag"), str)
                and 0 < len(value["etag"]) <= 256, "RESPONSE_UNKNOWN")
        return value

    def enable_subdomain(self, name: str):
        value = self._request("POST", f"scripts/{self._name(name)}/subdomain",
                              json={"enabled": True, "previews_enabled": False})
        require(type(value) is dict and value.get("enabled") is True and value.get("previews_enabled") is False,
                "RESPONSE_UNKNOWN")

    def script_subdomain(self, name: str):
        value = self._request("GET", f"scripts/{self._name(name)}/subdomain")
        require(type(value) is dict and value.get("enabled") is True and value.get("previews_enabled") is False,
                "RESPONSE_UNKNOWN")


def public_release(origin: str) -> dict:
    """公开版本读回不携带 Cloudflare API token，也不继承 netrc/proxy 凭据。"""
    require(isinstance(origin, str) and re.fullmatch(r"https://[a-z0-9-]+\.[a-z0-9-]+\.workers\.dev", origin) is not None,
            "IDENTITY")
    session = secure_session()
    try:
        with session.get(origin + "/.well-known/easyproxy-release.json", timeout=(10, 30),
                         allow_redirects=False, stream=True, headers={"Cache-Control": "no-cache"}) as response:
            require(response.status_code == 200, "PUBLIC_VERSION")
            raw = bytearray()
            for chunk in response.iter_content(4096):
                raw.extend(chunk)
                require(len(raw) <= 16384, "PUBLIC_VERSION")
            return decode(bytes(raw))
    except (requests.RequestException, OSError):
        raise WorkerError("EASYPROXY_SBA_WORKER_PUBLIC_UNKNOWN") from None
    finally:
        session.close()
