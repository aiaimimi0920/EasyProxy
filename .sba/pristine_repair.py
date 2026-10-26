"""原任务绑定的 pristine 修复前置：只读，不清锁、不接管或回放部分发布。"""
from __future__ import annotations

from coordination import r2_store
from d1_init import D1API, SCHEMA_QUERY, _schema
from github_api import GitHubAPI
from pages_api import PagesAPI
from repair_context import PARENT_ERROR, PARENT_RELEASE, REPAIR_ID
from request import ApplicationRequest
from worker_api import WorkerAPI


class PristineError(RuntimeError):
    pass


def require(value):
    if not value:
        raise PristineError("EASYPROXY_SBA_REPAIR_NOT_PRISTINE")


def assert_pristine(request: ApplicationRequest, credentials: dict) -> None:
    require(type(request) is ApplicationRequest and request.action == "repair"
            and request.previous == PARENT_RELEASE and type(request.context) is dict
            and request.context.get("repairId") == REPAIR_ID and request.context.get("errorCode") == PARENT_ERROR)
    config = request.config
    store = r2_store(config.account_id, config.bucket, credentials["EASYPROXY_R2_ACCESS_KEY_ID"],
                     credentials["EASYPROXY_R2_SECRET_ACCESS_KEY"])
    try:
        require(store.read() is None and store.is_empty())
    finally:
        store.client.close()
    database = D1API(config.account_id, credentials["EASYPROXY_CLOUDFLARE_API_TOKEN"], config.database_id)
    try:
        identity = database.identity()
        require(identity.get("uuid") == config.database_id and identity.get("name") == config.database_name
                and not _schema(database.query(SCHEMA_QUERY)))
    finally:
        database.close()
    worker = WorkerAPI(config.account_id, credentials["EASYPROXY_CLOUDFLARE_API_TOKEN"])
    try:
        require(worker.settings_or_absent(config.worker_name) is None
                and worker.account_subdomain() == config.workers_subdomain)
    finally:
        worker.close()
    pages = PagesAPI(config.account_id, config.pages_name, credentials["EASYPROXY_CLOUDFLARE_API_TOKEN"])
    try:
        require(pages.project() is None)
    finally:
        pages.close()
    github = GitHubAPI(config.repository, config.environment, credentials["EASYPROXY_GITHUB_TOKEN"])
    try:
        github.repository_info()  # A masked repository or missing admin right is not absence.
        require(github.environment_info() is None)
    finally:
        github.close()
