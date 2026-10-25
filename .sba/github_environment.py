"""在同一套件 Permit 下配置实例 environment；不接管旧环境、不自行提交版本。"""
from __future__ import annotations

import json
from pathlib import Path

from coordination import Coordinator, Permit
from github_api import GitHubAPI, require
from sealed_box import Sealer, secret_value
from refresh_registry import REGISTRY_VARIABLE, include_environment, parse_registry
from refresh_context import CONFIG_VARIABLE, OWNER_VARIABLE, REFRESH_SECRETS
from sources import inspect_checkout
from suite_config import SuiteConfig, WORKFLOW


def encode(value: dict) -> str:
    return json.dumps(value, ensure_ascii=True, sort_keys=True, separators=(",", ":"))


def variables(owner: Coordinator, config: SuiteConfig, repository_id: int, environment_id: int) -> dict[str, str]:
    return {OWNER_VARIABLE: encode({"schemaVersion": 1, "repositoryId": repository_id,
                "environmentId": environment_id, "identity": owner.identity, "inventoryDigest": owner.inventory_digest}),
            CONFIG_VARIABLE: encode(config.configuration())}


def verify_policy(environment: dict, policies: list[dict], branch: str):
    require(environment.get("deployment_branch_policy") == {"protected_branches": False, "custom_branch_policies": True},
            "BRANCH_POLICY")
    require(len(policies) == 1 and policies[0].get("name") == branch and policies[0].get("type") == "branch"
            and type(policies[0].get("id")) is int and policies[0]["id"] > 0, "BRANCH_POLICY")
    rules = environment.get("protection_rules")
    require(type(rules) is list and len(rules) == 1 and type(rules[0]) is dict
            and rules[0].get("type") == "branch_policy", "PROTECTION_RULES")


def reviewed_workflow(root: Path) -> bytes:
    path = root / WORKFLOW
    require(root.is_absolute() and path.is_file() and not path.is_symlink()
            and path.resolve(strict=True) == path and 0 < path.stat().st_size <= 65536, "WORKFLOW")
    body = path.read_bytes()
    try:
        body.decode("utf-8", errors="strict")
    except UnicodeError:
        require(False, "WORKFLOW")
    require(not body.startswith(b"\xef\xbb\xbf") and b"\x00" not in body, "WORKFLOW")
    return body


def configure_environment(owner: Coordinator, permit: Permit, config: SuiteConfig, root: Path, *,
                          token: str, secrets: dict, sealer: Sealer | None) -> dict:
    state = owner.assert_owned(permit)
    require(permit.action in ("deploy", "update", "preview") and type(config) is SuiteConfig
            and owner.identity == config.identity(state["identity"]["purpose"])
            and owner.inventory_digest == config.inventory_digest, "IDENTITY")
    production = state["identity"]["purpose"] == "production"
    expected_names = REFRESH_SECRETS if production else set()
    require(type(secrets) is dict and set(secrets) == expected_names
            and all(secret_value(value) for value in secrets.values()), "CREDENTIALS")
    release = state["writer"]["candidate"]
    require(not production or type(sealer) is Sealer and sealer.source_sha == release["sourceSha"], "SEALER")
    inspect_checkout(root, config.repository, release["sourceSha"])
    workflow = reviewed_workflow(root)
    api = GitHubAPI(config.repository, config.environment, token)
    try:
        repository = api.repository_info()  # Establish visibility/admin before interpreting environment 404.
        require(api.workflow_at(release["sourceSha"]) == workflow
                and api.workflow_at(repository["defaultBranch"]) == workflow, "WORKFLOW")
        api.active_workflow()
        existing = api.environment_info()
        registry_raw = api.repository_variable(REGISTRY_VARIABLE) if production else None
        registry = parse_registry(registry_raw, config.repository) if production else None
        if permit.action == "update":
            require(existing is not None, "UNOWNED_ENVIRONMENT")
            require(not production or config.environment in registry["environments"], "REGISTRY_DRIFT")
            expected = variables(owner, config, repository["id"], existing["id"])
            require(api.variables() == expected and api.secret_names() == expected_names, "UNOWNED_ENVIRONMENT")
            verify_policy(existing, api.policies(), repository["defaultBranch"])
        else:
            require(existing is None, "UNOWNED_ENVIRONMENT")
        inspect_checkout(root, config.repository, release["sourceSha"])
        owner.claim_stage(permit, "github-environment")
        if existing is None:
            require(api.environment_info() is None, "UNOWNED_ENVIRONMENT")
            existing = api.create_environment()
            owner.assert_owned(permit)
            require(api.policies() == [] and api.variables() == {} and api.secret_names() == set(), "UNOWNED_ENVIRONMENT")
            api.create_branch_policy(repository["defaultBranch"])
            owner.assert_owned(permit)
            # Ownership variables contain no token, secret, or mutable approved SHA.
            expected = variables(owner, config, repository["id"], existing["id"])
            for name, value in sorted(expected.items()):
                owner.assert_owned(permit)
                api.create_variable(name, value)
        observed = api.environment_info()
        require(observed is not None and observed["id"] == existing["id"], "ENVIRONMENT")
        verify_policy(observed, api.policies(), repository["defaultBranch"])
        require(api.variables() == expected, "UNOWNED_ENVIRONMENT")
        if production:
            public_key = api.public_key()
            for name, plaintext in sorted(secrets.items()):
                ciphertext = sealer.encrypt(public_key["key"], plaintext)
                owner.assert_owned(permit)
                api.put_secret(name, ciphertext, public_key["key_id"], create=permit.action != "update")
            # SpringBok serializes suite writes repository-wide. GitHub variables
            # have no CAS, so out-of-band registry administration must be excluded.
            target_registry = include_environment(registry, config.environment)
            require(api.repository_variable(REGISTRY_VARIABLE) == registry_raw, "REGISTRY_DRIFT")
            if target_registry != registry_raw:
                owner.assert_owned(permit)
                api.put_repository_variable(REGISTRY_VARIABLE, target_registry, create=registry_raw is None)
            require(api.repository_variable(REGISTRY_VARIABLE) == target_registry, "READBACK_UNKNOWN")
        owner.assert_owned(permit)
        final = api.environment_info()
        require(final is not None and final["id"] == existing["id"] and api.variables() == expected
                and api.secret_names() == expected_names and api.repository_info() == repository, "READBACK_UNKNOWN")
        verify_policy(final, api.policies(), repository["defaultBranch"])
        inspect_checkout(root, config.repository, release["sourceSha"])
        return {"checks": {"github-environment-configured": True, "github-environment-verified": True,
                           "github-workflow-verified": True},
                "environmentId": existing["id"], "repositoryId": repository["id"], "refreshEnabled": production}
    finally:
        api.close()  # No cleanup, retry, rollback, or writer release after partial/UNKNOWN writes.
