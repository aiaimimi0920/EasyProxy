"""固定 GitHub environment API；每次写只发送一次，UNKNOWN 不重放。"""
from __future__ import annotations

import base64
import hashlib
import re

import requests

from cloudflare_http import decode_object, secure_session
from sealed_box import decode_base64
from suite_config import WORKFLOW


class GitHubError(RuntimeError):
    pass


def require(value, code):
    if not value:
        raise GitHubError("EASYPROXY_SBA_GITHUB_" + code)


class GitHubAPI:
    def __init__(self, repository: str, environment: str, token: str):
        require(type(repository) is str and re.fullmatch(
            r"[a-z0-9][a-z0-9_.-]{0,99}/[a-z0-9][a-z0-9_.-]{0,99}", repository) is not None
            and type(environment) is str and re.fullmatch(r"[a-z][a-z0-9-]{1,62}", environment) is not None,
            "IDENTITY")
        require(type(token) is str and 0 < len(token) <= 512 and token != "verification-not-applicable"
                and all(33 <= ord(char) <= 126 for char in token), "CREDENTIALS")
        self.repository = repository
        self.environment = environment
        self._root = "/repos/" + repository
        self._env = self._root + "/environments/" + environment
        self._token = token
        self._session = secure_session()

    def close(self):
        self._session.close()

    def _request(self, method: str, path: str, *, status=200, missing=False, empty=False, **kwargs):
        require(path == self._root or path.startswith(self._root + "/"), "IDENTITY")
        require(not any(char in path for char in ("?", "#", "\\", "\r", "\n")), "IDENTITY")
        headers = {"Authorization": "Bearer " + self._token, "Accept": "application/vnd.github+json",
                   "X-GitHub-Api-Version": "2022-11-28", "User-Agent": "easyproxy-sba"}
        try:
            with self._session.request(method, "https://api.github.com" + path, headers=headers,
                    timeout=(10, 60), stream=True, allow_redirects=False, **kwargs) as response:
                absent = missing and method == "GET" and response.status_code == 404
                require(response.status_code == status or absent, "REQUEST_UNKNOWN")
                raw = bytearray()
                for chunk in response.iter_content(65536):
                    raw.extend(chunk)
                    require(len(raw) <= 1024 * 1024, "RESPONSE_UNKNOWN")
                if empty:
                    require(not raw and not absent, "RESPONSE_UNKNOWN")
                    return None
                require(response.headers.get("Content-Type", "").split(";")[0].lower()
                        in ("application/json", "application/vnd.github+json"), "RESPONSE_UNKNOWN")
                result = decode_object(bytes(raw), require)
                if absent:
                    require(result.get("message") == "Not Found", "RESPONSE_UNKNOWN")
                    return None
                return result
        except (requests.RequestException, OSError):
            raise GitHubError("EASYPROXY_SBA_GITHUB_REQUEST_UNKNOWN") from None

    def repository_info(self) -> dict:
        row = self._request("GET", self._root)
        require(type(row.get("id")) is int and row["id"] > 0
                and type(row.get("full_name")) is str and row["full_name"].lower() == self.repository
                and row.get("archived") is False and row.get("disabled") is False
                and type(row.get("permissions")) is dict and row["permissions"].get("admin") is True,
                "REPOSITORY")
        # Literal branch policy, never a caller-supplied glob or arbitrary ref.
        branch = row.get("default_branch")
        require(type(branch) is str and re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._/-]{0,99}", branch) is not None
                and ".." not in branch and "//" not in branch and not branch.endswith(("/", ".", ".lock")), "BRANCH")
        return {"id": row["id"], "defaultBranch": branch}

    def environment_info(self) -> dict | None:
        row = self._request("GET", self._env, missing=True)
        if row is not None:
            self._environment_identity(row)
        return row

    def _environment_identity(self, row):
        require(type(row.get("id")) is int and row["id"] > 0 and row.get("name") == self.environment
                and type(row.get("url")) is str
                and row["url"].lower() == "https://api.github.com" + self._env, "ENVIRONMENT")

    def create_environment(self) -> dict:
        # GitHub only offers upsert, not atomic create-only. Never explicitly
        # reset reviewers/timers. Concurrent out-of-band administration is not
        # fenced by the suite Permit and must remain excluded during deployment.
        row = self._request("PUT", self._env, json={
            "deployment_branch_policy": {"protected_branches": False, "custom_branch_policies": True}})
        self._environment_identity(row)
        return row

    def policies(self) -> list[dict]:
        row = self._request("GET", self._env + "/deployment-branch-policies", params={"per_page": 100, "page": 1})
        return self._list(row, "branch_policies")

    def create_branch_policy(self, branch: str) -> dict:
        return self._request("POST", self._env + "/deployment-branch-policies", json={"name": branch, "type": "branch"})

    @staticmethod
    def _list(row, field: str) -> list[dict]:
        require(type(row.get("total_count")) is int and type(row.get(field)) is list
                and len(row[field]) <= 100 and row["total_count"] == len(row[field])
                and all(type(item) is dict for item in row[field]), "RESPONSE_UNKNOWN")
        return row[field]

    def variables(self) -> dict[str, str]:
        items = self._list(self._request("GET", self._env + "/variables", params={"per_page": 100, "page": 1}), "variables")
        require(all(type(row.get("name")) is str and type(row.get("value")) is str for row in items)
                and len({row["name"] for row in items}) == len(items), "RESPONSE_UNKNOWN")
        return {row["name"]: row["value"] for row in items}

    def create_variable(self, name: str, value: str):
        self._request("POST", self._env + "/variables", status=201, empty=True, json={"name": name, "value": value})

    def repository_variable(self, name: str) -> str | None:
        require(re.fullmatch(r"[A-Z][A-Z0-9_]{1,99}", name) is not None, "IDENTITY")
        row = self._request("GET", self._root + "/actions/variables/" + name, missing=True)
        if row is None:
            return None
        require(row.get("name") == name and type(row.get("value")) is str, "RESPONSE_UNKNOWN")
        return row["value"]

    def put_repository_variable(self, name: str, value: str, *, create: bool):
        require(re.fullmatch(r"[A-Z][A-Z0-9_]{1,99}", name) is not None and type(value) is str, "IDENTITY")
        self._request("POST" if create else "PATCH", self._root + "/actions/variables" + ("" if create else "/" + name),
                      status=201 if create else 204, empty=True, json={"name": name, "value": value})

    def secret_names(self) -> set[str]:
        items = self._list(self._request("GET", self._env + "/secrets", params={"per_page": 100, "page": 1}), "secrets")
        require(all(type(row.get("name")) is str for row in items)
                and len({row["name"] for row in items}) == len(items), "RESPONSE_UNKNOWN")
        return {row["name"] for row in items}

    def public_key(self) -> dict:
        row = self._request("GET", self._env + "/secrets/public-key")
        require(type(row.get("key_id")) is str and re.fullmatch(r"[0-9]{1,64}", row["key_id"]) is not None
                and type(row.get("key")) is str, "PUBLIC_KEY")
        decode_base64(row["key"], 32)
        return row

    def put_secret(self, name: str, ciphertext: str, key_id: str, *, create: bool):
        require(re.fullmatch(r"[A-Z][A-Z0-9_]{1,99}", name) is not None, "IDENTITY")
        self._request("PUT", self._env + "/secrets/" + name, status=201 if create else 204,
                      empty=True, json={"encrypted_value": ciphertext, "key_id": key_id})

    def workflow_at(self, reference: str) -> bytes:
        require(type(reference) is str and re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._/-]{0,99}", reference) is not None,
                "IDENTITY")
        row = self._request("GET", self._root + "/contents/" + WORKFLOW, params={"ref": reference})
        require(row.get("type") == "file" and row.get("path") == WORKFLOW and row.get("encoding") == "base64"
                and type(row.get("size")) is int and 0 < row["size"] <= 65536
                and type(row.get("content")) is str and type(row.get("sha")) is str, "WORKFLOW")
        try:
            body = base64.b64decode("".join(row["content"].splitlines()), validate=True)
        except (ValueError, UnicodeError):
            raise GitHubError("EASYPROXY_SBA_GITHUB_WORKFLOW") from None
        require(len(body) == row["size"] and hashlib.sha1(b"blob " + str(len(body)).encode() + b"\0" + body).hexdigest() == row["sha"],
                "WORKFLOW")
        return body

    def active_workflow(self):
        row = self._request("GET", self._root + "/actions/workflows/" + WORKFLOW.rsplit("/", 1)[1])
        require(type(row.get("id")) is int and row["id"] > 0 and row.get("path") == WORKFLOW
                and row.get("state") == "active", "WORKFLOW")
