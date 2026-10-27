"""共享固定 HTTPS 传输；不重放写入，不继承代理、netrc 或环境凭据。"""
from __future__ import annotations

import json
import re

import requests
from requests.adapters import HTTPAdapter
from urllib3.util.retry import Retry

from response_diagnostics import ResponseDiagnostic, RESPONSE_REASONS


def _mark(diagnostic, reason):
    if diagnostic is not None:
        diagnostic.reason = reason


def _status_reason(status):
    if status in (400, 401, 403, 404, 409, 413, 429):
        return "HTTP_" + str(status)
    return "HTTP_3XX" if 300 <= status < 400 else "HTTP_4XX" if 400 <= status < 500 else "HTTP_5XX" if 500 <= status < 600 else "HTTP_OTHER"


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


def api_result(session: requests.Session, token: str, method: str, path: str, require, *, missing_code=None,
               pages_asset_response=False, worker_domain_response=False, pages_domain_response=False, diagnostic=None, **kwargs):
    require(diagnostic is None or type(diagnostic) is ResponseDiagnostic, "IDENTITY")
    _mark(diagnostic, None)
    # Only typed provider clients construct paths. There is no configurable host.
    require(path.startswith("/") and not any(char in path for char in ("?", "#", "\\", "\r", "\n")), "IDENTITY")
    # Pages' JWT asset API omits errors on success; its two write ACKs also
    # omit result. Scope these wire exceptions to exact asset POST endpoints.
    require(type(pages_asset_response) is bool and (not pages_asset_response or method == "POST"
            and path in ("/pages/assets/check-missing", "/pages/assets/upload", "/pages/assets/upsert-hashes")), "IDENTITY")
    # Workers Domains GET uses nullable errors; its documented PUT response can
    # be a direct domain object. Neither exception applies to Worker uploads.
    require(type(worker_domain_response) is bool and (not worker_domain_response or
            method in ("GET", "PUT") and re.fullmatch(r"/accounts/[a-f0-9]{32}/workers/domains", path) is not None), "IDENTITY")
    pages_domain_path = re.fullmatch(r"/accounts/[a-f0-9]{32}/pages/projects/[a-z0-9-]+/domains(/[^/]+)?", path)
    require(type(pages_domain_response) is bool and (not pages_domain_response or pages_domain_path is not None
            and ((method == "POST" and pages_domain_path[1] is None) or
                 (method == "GET" and pages_domain_path[1] is not None))), "IDENTITY")
    try:
        with session.request(method, "https://api.cloudflare.com/client/v4" + path,
                             headers={"Authorization": "Bearer " + token}, timeout=(10, 60),
                             allow_redirects=False, stream=True, **kwargs) as response:
            if not (200 <= response.status_code < 300 or
                    method == "GET" and missing_code is not None and response.status_code == 404):
                _mark(diagnostic, _status_reason(response.status_code))
            require(200 <= response.status_code < 300 or
                    method == "GET" and missing_code is not None and response.status_code == 404, "REQUEST_UNKNOWN")
            raw = bytearray()
            for chunk in response.iter_content(65536):
                raw.extend(chunk)
                if len(raw) > 1024 * 1024:
                    _mark(diagnostic, "SIZE")
                require(len(raw) <= 1024 * 1024, "RESPONSE_UNKNOWN")
            _mark(diagnostic, "DECODE")
            payload = decode_object(bytes(raw), require)
            _mark(diagnostic, "ENVELOPE")
            if method == "GET" and missing_code is not None and response.status_code == 404:
                errors = payload.get("errors")
                require(payload.get("success") is False and type(errors) is list and len(errors) == 1
                        and type(errors[0]) is dict and errors[0].get("code") == missing_code, "RESPONSE_UNKNOWN")
                _mark(diagnostic, None)
                return None
            require(200 <= response.status_code < 300, "REQUEST_UNKNOWN")
            if (worker_domain_response and method == "PUT" or pages_domain_response) and "success" not in payload:
                fields = ({"id", "hostname", "service", "zone_id", "zone_name", "environment", "enabled", "previews_enabled"}
                          if worker_domain_response else {"id", "domain_id", "name", "zone_tag", "status"})
                require(not {"errors", "messages", "result"}.intersection(payload) and
                        fields.issubset(payload), "RESPONSE_UNKNOWN")
                _mark(diagnostic, None)
                return payload  # DomainAPI must still validate the exact owning target.
            errors = payload.get("errors", [] if pages_asset_response else None)
            require(payload.get("success") is True and (errors == [] or worker_domain_response and errors is None)
                    and ("result" in payload or pages_asset_response and path in
                         ("/pages/assets/upload", "/pages/assets/upsert-hashes")),
                    "RESPONSE_UNKNOWN")
            _mark(diagnostic, None)
            return payload.get("result")
    except requests.Timeout:
        _mark(diagnostic, "TIMEOUT")
        require(False, "REQUEST_UNKNOWN")
    except requests.ConnectionError:
        _mark(diagnostic, "NETWORK")
        require(False, "REQUEST_UNKNOWN")
    except requests.RequestException:
        _mark(diagnostic, "REQUEST")
        require(False, "REQUEST_UNKNOWN")
    except OSError:
        _mark(diagnostic, "NETWORK")
        require(False, "REQUEST_UNKNOWN")
