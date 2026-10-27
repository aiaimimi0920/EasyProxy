"""只销毁批准的独立预升级清单；共享 workflow/repository/正式资源不在删除路径。"""
from __future__ import annotations

from pathlib import Path

from cloudflare_http import api_result
from coordination import Coordinator, r2_store
from d1_init import D1API
from d1_snapshot import capture
from ech import _verify_settings, metadata
from github_api import GitHubAPI
from github_environment import variables, verify_policy
from pages import _verify_project, configuration
from pages_api import PagesAPI
from preview import database_identity, private_record
from preview_terminal import finish_bucket, preflight_bucket
from refresh_context import require
from refresh_registry import REGISTRY_VARIABLE, parse_registry
from request import ApplicationRequest, SECRETS, config_inventory
from sealed_box import secret_value
from sources import inspect_checkout
from suite_config import parse_configuration
from worker_api import WorkerAPI


def destroy_preview(request: ApplicationRequest, root: Path, output: Path, *, credentials: dict) -> dict:
    require(type(request) is ApplicationRequest and request.action == "destroy-preview"
            and request.context is not None and type(credentials) is dict and set(credentials) == SECRETS
            and all(secret_value(value) for value in credentials.values()))
    require(root.is_absolute() and root.resolve(strict=True) == root and output.is_absolute()
            and output.resolve() == output and not output.exists() and root != output and root not in output.parents)
    config = request.config
    require(config.custom_pages_origin is None and config.custom_worker_origin is None)
    inspect_checkout(root, config.repository, request.source_sha)
    clients, stores = [], []
    try:
        def store_for(settings):
            value = r2_store(settings.account_id, settings.bucket, credentials["EASYPROXY_R2_ACCESS_KEY_ID"],
                             credentials["EASYPROXY_R2_SECRET_ACCESS_KEY"])
            stores.append(value)
            return value

        store = store_for(config)
        owner = Coordinator(store, config.identity("preview"), config.inventory_digest)
        require(owner.current_release() == request.candidate)
        record = private_record(store)
        require(set(record) == {"schemaVersion", "previewTaskId", "release", "source", "snapshot"}
                and record["schemaVersion"] == 1 and record["previewTaskId"] == request.context["previewTaskId"]
                and record["release"] == request.candidate)
        source = record["source"]
        original = parse_configuration(source["configuration"], repository=config.repository, environment=source["environment"])
        config_inventory(source["resources"], original)
        require(original.account_id == config.account_id and original.environment != config.environment)
        original_ids = {(row["accountId"], row["kind"], row["remoteId"]) for row in original.platform_inventory}
        require(not any((row["accountId"], row["kind"], row["remoteId"]) in original_ids for row in config.platform_inventory))
        source_owner = Coordinator(store_for(original), original.identity("production"), original.inventory_digest)
        source_release = source_owner.current_release()
        source_api = D1API(original.account_id, credentials["EASYPROXY_CLOUDFLARE_API_TOKEN"], original.database_id)
        target = D1API(config.account_id, credentials["EASYPROXY_CLOUDFLARE_API_TOKEN"], config.database_id)
        edge = WorkerAPI(config.account_id, credentials["EASYPROXY_CLOUDFLARE_API_TOKEN"])
        pages = PagesAPI(config.account_id, config.pages_name, credentials["EASYPROXY_CLOUDFLARE_API_TOKEN"])
        github = GitHubAPI(config.repository, config.environment, credentials["EASYPROXY_GITHUB_TOKEN"])
        clients.extend([source_api, target, edge, pages, github])
        database_identity(source_api, original)
        baseline = capture(source_api)
        database_identity(target, config)
        _verify_settings(edge.settings(config.worker_name), metadata(owner, request.candidate, credentials["EASYPROXY_ECH_TOKEN"]))
        expected_pages = configuration(owner, request.candidate, config.pages_name, config.database_id, {
            "ADMIN_PASSWORD": credentials["EASYPROXY_MISUB_ADMIN_PASSWORD"], "COOKIE_SECRET": credentials["EASYPROXY_MISUB_COOKIE_SECRET"],
            "MANIFEST_TOKEN": credentials["EASYPROXY_MANIFEST_TOKEN"]})
        _verify_project(pages.project(), config.pages_name, expected_pages)
        repo = github.repository_info()
        environment = github.environment_info()
        require(environment is not None and github.variables() == variables(owner, config, repo["id"], environment["id"])
                and github.secret_names() == set()
                and config.environment not in parse_registry(github.repository_variable(REGISTRY_VARIABLE), config.repository)["environments"])
        verify_policy(environment, github.policies(), repo["defaultBranch"])
        permit = owner.begin("destroy-preview", request.candidate, previous=request.previous)
        owner.claim_stage(permit, "preview-resources-deletion")
        preflight_bucket(owner, permit, output / "termination.safe.json")
        # Delete entrypoints before data. No automatic recovery/rollback/retry.
        github._request("DELETE", github._env, status=204, empty=True)
        require(github.environment_info() is None)
        owner.assert_owned(permit)
        edge._request("DELETE", "scripts/" + config.worker_name)
        require(not any(row["id"] == config.worker_name for row in edge.list_workers()))
        owner.assert_owned(permit)
        api_result(pages._session, pages._token, "DELETE", pages.path, lambda value, _code: require(value))
        require(pages.project() is None)
        owner.assert_owned(permit)
        api_result(target.session, target.token, "DELETE", target.path, lambda value, _code: require(value))
        # D1 absence uses the exact account list, not a guessed 404 error code.
        for page in range(1, 101):
            rows = api_result(target.session, target.token, "GET", "/accounts/" + config.account_id + "/d1/database",
                lambda value, _code: require(value), params={"per_page": 100, "page": page})
            require(type(rows) is list and len(rows) <= 100 and all(type(row) is dict and "uuid" in row for row in rows)
                    and not any(row["uuid"] == config.database_id for row in rows))
            if len(rows) < 100:
                break
        else:
            require(False)

        def source_unchanged():
            database_identity(source_api, original)
            require(capture(source_api).digest == baseline.digest and source_owner.current_release() == source_release)
            inspect_checkout(root, config.repository, request.source_sha)
            return {"unchanged": True, "dataSha256": baseline.digest, "release": source_release,
                    "inventoryDigest": original.inventory_digest}

        removed = [{"key": row["key"], "status": "removed"} for row in config.platform_inventory]
        # workers.dev/pages.dev are owned provider origins, not separately-created
        # Custom Domain objects. Their target deletion confirms association removal.
        finish_bucket(owner, permit, output / "termination.safe.json", source_unchanged=source_unchanged, removed=removed)
        return {"checks": {"preview-removed": True, "source-unchanged": True}, "lifecycle": {"resources": removed}}
    finally:
        for client in clients:
            client.close()
        for store in stores:
            store.client.close()
