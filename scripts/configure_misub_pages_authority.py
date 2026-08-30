#!/usr/bin/env python3

from __future__ import annotations

import argparse
import os
from typing import Any
from urllib.parse import quote

import requests


API_BASE_URL = "https://api.cloudflare.com/client/v4"
SOURCE_CONFIG_PASSTHROUGH_FIELDS = (
    "owner",
    "owner_id",
    "path_excludes",
    "path_includes",
    "pr_comments_enabled",
    "preview_branch_excludes",
    "preview_branch_includes",
    "repo_id",
    "repo_name",
    "production_branch",
)


def automatic_deployments_disabled(project: dict[str, Any]) -> bool:
    source = project.get("source")
    if not isinstance(source, dict):
        return True
    config = source.get("config")
    if not isinstance(config, dict):
        return False
    return (
        config.get("production_deployments_enabled") is False
        and config.get("preview_deployment_setting") == "none"
    )


def build_source_patch(project: dict[str, Any]) -> dict[str, Any] | None:
    source = project.get("source")
    if not isinstance(source, dict):
        return None
    source_type = str(source.get("type") or "").strip()
    config = source.get("config")
    if source_type not in {"github", "gitlab"} or not isinstance(config, dict):
        raise RuntimeError("Cloudflare Pages source control identity is incomplete")

    patched_config = {
        key: config[key]
        for key in SOURCE_CONFIG_PASSTHROUGH_FIELDS
        if key in config and config[key] is not None
    }
    patched_config.update(
        {
            "deployments_enabled": False,
            "production_deployments_enabled": False,
            "preview_deployment_setting": "none",
        }
    )
    return {"source": {"type": source_type, "config": patched_config}}


def response_result(response: requests.Response, label: str) -> dict[str, Any]:
    response.raise_for_status()
    payload = response.json()
    if not isinstance(payload, dict) or payload.get("success") is not True:
        raise RuntimeError(f"{label} returned an unsuccessful Cloudflare response")
    result = payload.get("result")
    if not isinstance(result, dict):
        raise RuntimeError(f"{label} did not return a Pages project")
    return result


def main() -> int:
    parser = argparse.ArgumentParser(
        description="Make GitHub Actions the sole deployment authority for a MiSub Pages project."
    )
    parser.add_argument("--account-id", required=True)
    parser.add_argument("--project-name", required=True)
    parser.add_argument("--api-base-url", default=API_BASE_URL)
    args = parser.parse_args()

    token = os.environ.get("CLOUDFLARE_API_TOKEN", "").strip()
    if not token:
        raise RuntimeError("CLOUDFLARE_API_TOKEN is required")

    endpoint = (
        args.api_base_url.rstrip("/")
        + "/accounts/"
        + quote(args.account_id.strip(), safe="")
        + "/pages/projects/"
        + quote(args.project_name.strip(), safe="")
    )
    session = requests.Session()
    session.headers.update(
        {
            "Authorization": f"Bearer {token}",
            "Accept": "application/json",
        }
    )

    project = response_result(session.get(endpoint, timeout=30), "read Pages project")
    patch = build_source_patch(project)
    if patch is None:
        print("MiSub Pages project already uses direct-upload deployment authority")
        return 0
    if automatic_deployments_disabled(project):
        print("MiSub Pages automatic Git deployments are already disabled")
        return 0

    response_result(session.patch(endpoint, json=patch, timeout=30), "update Pages project")
    verified = response_result(session.get(endpoint, timeout=30), "verify Pages project")
    if not automatic_deployments_disabled(verified):
        raise RuntimeError("Cloudflare Pages automatic Git deployments remain enabled")
    print("Disabled MiSub Pages automatic Git deployments; GitHub Actions is authoritative")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
