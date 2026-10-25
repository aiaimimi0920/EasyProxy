"""共享固定 HTTPS 传输；不重放写入，不继承代理、netrc 或环境凭据。"""
from __future__ import annotations

import json

import requests
from requests.adapters import HTTPAdapter
from urllib3.util.retry import Retry


def secure_session() -> requests.Session:
    session = requests.Session()
    session.trust_env = False
    session.mount("https://", HTTPAdapter(max_retries=Retry(total=0, connect=0, read=0,
                                                         redirect=0, status=0, other=0)))
    return session


def decode_object(raw: bytes, require) -> dict:
    def unique(pairs):
        value = {}
        for key, item in pairs:
            require(key not in value, "RESPONSE_UNKNOWN")
            value[key] = item
        return value
    try:
        value = json.loads(raw.decode("utf-8"), object_pairs_hook=unique)
    except (ValueError, UnicodeError, RecursionError):
        require(False, "RESPONSE_UNKNOWN")
    require(type(value) is dict, "RESPONSE_UNKNOWN")
    return value


def api_result(session: requests.Session, token: str, method: str, path: str, require, *, missing_code=None, **kwargs):
    # Only typed provider clients construct paths. There is no configurable host.
    require(path.startswith("/") and not any(char in path for char in ("?", "#", "\\", "\r", "\n")), "IDENTITY")
    try:
        with session.request(method, "https://api.cloudflare.com/client/v4" + path,
                             headers={"Authorization": "Bearer " + token}, timeout=(10, 60),
                             allow_redirects=False, stream=True, **kwargs) as response:
            require(200 <= response.status_code < 300 or
                    method == "GET" and missing_code is not None and response.status_code == 404, "REQUEST_UNKNOWN")
            raw = bytearray()
            for chunk in response.iter_content(65536):
                raw.extend(chunk)
                require(len(raw) <= 1024 * 1024, "RESPONSE_UNKNOWN")
            payload = decode_object(bytes(raw), require)
            if method == "GET" and missing_code is not None and response.status_code == 404:
                errors = payload.get("errors")
                require(payload.get("success") is False and type(errors) is list and len(errors) == 1
                        and type(errors[0]) is dict and errors[0].get("code") == missing_code, "RESPONSE_UNKNOWN")
                return None
            require(200 <= response.status_code < 300, "REQUEST_UNKNOWN")
            require(payload.get("success") is True and payload.get("errors") == [] and "result" in payload,
                    "RESPONSE_UNKNOWN")
            return payload["result"]
    except (requests.RequestException, OSError):
        require(False, "REQUEST_UNKNOWN")
